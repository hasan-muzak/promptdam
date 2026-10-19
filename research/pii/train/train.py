"""Trains the firewall's model: bardsai's checkpoint (XLM-R base) with a new BIOES head over Promptdam's labels,
on the training set datagen/build.py writes.

    uv run python train/train.py --data data/built/pilot.jsonl --eval data/built/holdout.jsonl --out models/student-pilot
    uv run python train/train.py ... --epochs 3 --batch 16 --lr 3e-5      # the full set, on a Verda GPU (bf16)

Writes a model directory the detector service reads (engine.Load): model.onnx comes from export.py, the rest from
here (config.json with id2label, tokenizer.json, labels.json mapping each label to itself)."""

import argparse
import json
import math
import random
from collections import Counter
from pathlib import Path

import torch
from torch.utils.data import Dataset
from transformers import (AutoModelForTokenClassification, AutoTokenizer, DataCollatorForTokenClassification, Trainer,
                          TrainingArguments)

ROOT = Path(__file__).parent.parent
BASE = ROOT / "models" / "bardsai-eu-pii"

# The labels the model learns, as the firewall names them (server/features/firewall/model.go), and the training
# set's names for them. Context labels are learnt so the model knows the boundary, and never reported.
LABELS = {
    "person": "person", "username": "username", "email": "email", "phone": "phone", "address": "address",
    "location": "location", "date_of_birth": "date_of_birth", "date": "personal_date", "government_id": "government_id",
    "account": "account_number", "ip": "ipv4", "url_personal": "url_personal", "health": "health", "religion": "religion",
    "sexual_orientation": "sexual_orientation", "political_opinion": "political_opinion", "trade_union": "trade_union",
    "criminal_record": "criminal_record", "ethnicity": "ethnicity", "biometric_genetic": "biometric_genetic",
    "secret": "credential", "organization": "organization", "job_title": "job_title", "money": "money",
    "public_figure": "public_figure",
}
CONTEXT = {"organization", "job_title", "money", "public_figure"}
TAGS = ["O"] + [f"{t}-{n}" for n in LABELS.values() for t in "BIES"]
MAX_LEN, STRIDE = 512, 128


def bioes(spans_by_token: list[tuple[int, str] | None]) -> list[str]:
    """Each token's tag from the span it lies in (its index and label), BIOES."""
    out = []
    for i, cur in enumerate(spans_by_token):
        if cur is None:
            out.append("O")
            continue
        prev = spans_by_token[i - 1] if i > 0 else None
        nxt = spans_by_token[i + 1] if i + 1 < len(spans_by_token) else None
        first, last = prev != cur, nxt != cur
        tag = "S" if first and last else "B" if first else "E" if last else "I"
        out.append(f"{tag}-{cur[1]}")
    return out


class Windows(Dataset):
    """Tokenized texts in windows of MAX_LEN tokens overlapping by STRIDE, each token tagged."""

    def __init__(self, path: Path, tok, limit: int = 0):
        self.items = []
        recs = [json.loads(l) for l in path.open(encoding="utf-8")]
        if limit:
            recs = recs[:limit]
        tag_id = {t: i for i, t in enumerate(TAGS)}
        self.counts = Counter()
        for r in recs:
            text = r["text"]
            spans = sorted((s, e, LABELS[l]) for s, e, l in r["spans"] if l in LABELS)
            enc = tok(text, return_offsets_mapping=True, add_special_tokens=False, truncation=False)
            ids, offsets = enc["input_ids"], enc["offset_mapping"]
            # A token is in a span when it overlaps it. Tokens and spans are in order and spans apart (marks don't
            # nest), so one pass with a pointer into the spans tags every token.
            by_token: list[tuple[int, str] | None] = []
            k = 0
            for a, b in offsets:
                while k < len(spans) and spans[k][1] <= a:
                    k += 1
                hit = None
                if k < len(spans) and b > a and spans[k][0] < b:
                    hit = (k, spans[k][2])
                by_token.append(hit)
            tags = [tag_id[t] for t in bioes(by_token)]
            self.counts.update(tags)
            step = MAX_LEN - 2 - STRIDE
            for start in range(0, max(1, len(ids)), step):
                chunk = ids[start:start + MAX_LEN - 2]
                labels = tags[start:start + MAX_LEN - 2]
                self.items.append({
                    "input_ids": [tok.cls_token_id] + chunk + [tok.sep_token_id],
                    "labels": [-100] + labels + [-100],
                })
                if start + MAX_LEN - 2 >= len(ids):
                    break

    def __len__(self):
        return len(self.items)

    def __getitem__(self, i):
        return self.items[i]


class WeightedTrainer(Trainer):
    """Cross-entropy with rare tags weighted up (a tag's weight: the inverse square root of its share, capped), so
    special categories aren't drowned by names and O."""

    def __init__(self, *a, weights=None, **kw):
        super().__init__(*a, **kw)
        self.weights = weights

    def compute_loss(self, model, inputs, return_outputs=False, **kw):
        labels = inputs.pop("labels")
        out = model(**inputs)
        loss = torch.nn.functional.cross_entropy(out.logits.view(-1, out.logits.size(-1)), labels.view(-1),
                                                 weight=self.weights.to(out.logits.device), ignore_index=-100)
        return (loss, out) if return_outputs else loss


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", required=True)
    ap.add_argument("--eval")
    ap.add_argument("--out", required=True)
    ap.add_argument("--base", default=str(BASE))
    ap.add_argument("--epochs", type=float, default=2)
    ap.add_argument("--batch", type=int, default=8)
    ap.add_argument("--lr", type=float, default=3e-5)
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--seed", type=int, default=13)
    ap.add_argument("--freeze-embeddings", action=argparse.BooleanOptionalAction, default=None,
                    help="keep the word embeddings (192M of XLM-R's 278M parameters) as pretrained; default: on "
                         "without a GPU, where their gradients and optimizer state don't fit beside everything else")
    args = ap.parse_args()
    random.seed(args.seed)
    torch.manual_seed(args.seed)

    tok = AutoTokenizer.from_pretrained(args.base)
    train = Windows(Path(args.data), tok, args.limit)
    evals = Windows(Path(args.eval), tok) if args.eval else None
    total = sum(train.counts.values())
    weights = torch.ones(len(TAGS))
    for t, n in train.counts.items():
        weights[t] = min(10.0, math.sqrt(total / max(n, 1)) / math.sqrt(len(TAGS)))
    weights[0] = min(weights[0], 1.0)
    print(f"{len(train)} training windows, {len(evals) if evals else 0} held out; tags seen: {len(train.counts)}")

    model = AutoModelForTokenClassification.from_pretrained(
        args.base, num_labels=len(TAGS), id2label=dict(enumerate(TAGS)), label2id={t: i for i, t in enumerate(TAGS)},
        ignore_mismatched_sizes=True)  # a new head over Promptdam's labels; the encoder is bardsai's
    gpu = torch.cuda.is_available()
    if args.freeze_embeddings if args.freeze_embeddings is not None else not gpu:
        model.get_input_embeddings().weight.requires_grad_(False)
    targs = TrainingArguments(
        output_dir=args.out + "/run", num_train_epochs=args.epochs, per_device_train_batch_size=args.batch,
        per_device_eval_batch_size=args.batch, learning_rate=args.lr, warmup_steps=0.06, weight_decay=0.01,
        lr_scheduler_type="linear", logging_steps=50, save_strategy="epoch", eval_strategy="epoch" if evals else "no",
        load_best_model_at_end=bool(evals), metric_for_best_model="eval_loss", greater_is_better=False,
        save_total_limit=1, bf16=gpu, report_to=[], seed=args.seed, dataloader_num_workers=0)
    trainer = WeightedTrainer(model=model, args=targs, train_dataset=train, eval_dataset=evals,
                              data_collator=DataCollatorForTokenClassification(tok, pad_to_multiple_of=64), weights=weights)
    trainer.train()

    out = Path(args.out)
    trainer.save_model(out)
    tok.save_pretrained(out)
    cfg = json.loads((out / "config.json").read_text())
    for k, v in (("bos_token_id", tok.cls_token_id), ("eos_token_id", tok.sep_token_id), ("pad_token_id", tok.pad_token_id)):
        cfg[k] = v
    (out / "config.json").write_text(json.dumps(cfg, indent=1))
    reported = {n: n for n in LABELS.values() if n not in CONTEXT}
    (out / "labels.json").write_text(json.dumps({"labels": reported, "min_score": 0.3}, indent=1))
    print("saved", out)


if __name__ == "__main__":
    main()
