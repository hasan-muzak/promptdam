"""The candidates behind one interface: predict(text) -> [(start, end, label)] in code
points. Each runs on the CPU in float32, one document at a time, as a gateway's
detector would see a single new message."""

import json
import re
from pathlib import Path

import numpy as np
import torch

ROOT = Path(__file__).parent / "models"


def trim(text: str, s: int, e: int) -> tuple[int, int]:
    """Drops whitespace a subword's offsets carry at a span's edges."""
    while s < e and text[s].isspace():
        s += 1
    while e > s and text[e - 1].isspace():
        e -= 1
    return s, e


def bioes_viterbi(logp: np.ndarray, labels: list[str]) -> list[str]:
    """The most likely tag path that obeys BIOES (B then I* then E, or S, per
    category), as OpenAI's decoder does at its default operating point (every
    transition bias 0). logp is [T, L] log-probabilities."""
    T, L = logp.shape
    kind = [l[0] if l != "O" else "O" for l in labels]
    cat = [l[2:] if l != "O" else "" for l in labels]
    allowed = np.full((L, L), -np.inf)
    for i in range(L):
        for j in range(L):
            closed = kind[i] in ("O", "E", "S")
            if closed and kind[j] in ("O", "B", "S"):
                allowed[i, j] = 0
            elif kind[i] in ("B", "I") and kind[j] in ("I", "E") and cat[i] == cat[j]:
                allowed[i, j] = 0
    start = np.array([0 if k in ("O", "B", "S") else -np.inf for k in kind])
    end = np.array([0 if k in ("O", "E", "S") else -np.inf for k in kind])
    score = start + logp[0]
    back = np.zeros((T, L), dtype=np.int32)
    for t in range(1, T):
        cand = score[:, None] + allowed
        back[t] = cand.argmax(0)
        score = cand.max(0) + logp[t]
    score = score + end
    path = [int(score.argmax())]
    for t in range(T - 1, 0, -1):
        path.append(int(back[t, path[-1]]))
    return [labels[i] for i in reversed(path)]


def spans_from_tags(text, tags, offsets) -> list[tuple[int, int, str]]:
    """Joins B/I/E/S (or B/I) tags into spans: a span runs while tokens keep its
    category without a new B or S."""
    out, cur = [], None
    for tag, (s, e) in zip(tags, offsets):
        if s == e:
            continue
        if tag == "O":
            if cur:
                out.append(cur)
            cur = None
            continue
        k, c = tag[0], tag[2:]
        if cur and cur[2] == c and k in ("I", "E"):
            cur = (cur[0], e, c)
        else:
            if cur:
                out.append(cur)
            cur = (s, e, c)
        if k in ("E", "S"):
            out.append(cur)
            cur = None
    if cur:
        out.append(cur)
    res = []
    for s, e, c in out:
        s, e = trim(text, s, e)
        if e > s:
            res.append((s, e, c))
    return res


class TokenClassifier:
    """A Hugging Face token classifier, windowed over long texts: each token's
    logits come from the window where it sits furthest from an edge."""

    def __init__(self, name: str, path: Path, window: int, overlap: int, viterbi: bool, **kw):
        from transformers import AutoModelForTokenClassification, AutoTokenizer

        self.name, self.window, self.overlap, self.viterbi = name, window, overlap, viterbi
        self.tok = AutoTokenizer.from_pretrained(path)
        self.model = AutoModelForTokenClassification.from_pretrained(path, dtype=torch.float32, **kw).eval()
        self.labels = [self.model.config.id2label[i] for i in range(len(self.model.config.id2label))]
        self.special = self.tok.all_special_ids

    def predict(self, text: str):
        enc = self.tok(text, return_offsets_mapping=True, add_special_tokens=False)
        ids, offsets = enc["input_ids"], enc["offset_mapping"]
        if not ids:
            return []
        n, step = len(ids), self.window - self.overlap
        best = np.full(n, -1.0)
        logits = np.zeros((n, len(self.labels)), dtype=np.float32)
        start = 0
        while True:
            chunk = ids[start:start + self.window]
            cls, sep = self.tok.cls_token_id, self.tok.sep_token_id
            x = [cls, *chunk, sep] if cls is not None and sep is not None else chunk
            lead = 1 if len(x) > len(chunk) else 0
            out = self.forward(x)
            out = out[lead:lead + len(chunk)]
            for i in range(len(chunk)):
                edge = min(i, len(chunk) - 1 - i)
                if edge > best[start + i]:
                    best[start + i], logits[start + i] = edge, out[i]
            if start + self.window >= n:
                break
            start += step
        if self.viterbi:
            logp = logits - np.logaddexp.reduce(logits, axis=1, keepdims=True)
            tags = bioes_viterbi(logp, self.labels)
        else:
            tags = [self.labels[i] for i in logits.argmax(1)]
        return spans_from_tags(text, tags, offsets)

    def forward(self, x: list[int]) -> np.ndarray:
        with torch.inference_mode():
            return self.model(input_ids=torch.tensor([x])).logits[0].float().numpy()

    def tokens(self, text: str) -> int:
        return len(self.tok(text, add_special_tokens=False)["input_ids"])


class OnnxClassifier(TokenClassifier):
    """The same classifier through ONNX Runtime from its exported file (int8 when
    quantized): how a production detector would run it."""

    def __init__(self, name: str, path: Path, onnx_file: str, window: int, overlap: int, viterbi: bool):
        import onnxruntime as ort
        from transformers import AutoConfig, AutoTokenizer

        self.name, self.window, self.overlap, self.viterbi = name, window, overlap, viterbi
        self.tok = AutoTokenizer.from_pretrained(path)
        cfg = AutoConfig.from_pretrained(path)
        self.labels = [cfg.id2label[i] for i in range(len(cfg.id2label))]
        opts = ort.SessionOptions()
        opts.intra_op_num_threads = torch.get_num_threads()
        self.sess = ort.InferenceSession(str(path / onnx_file), opts, providers=["CPUExecutionProvider"])
        self.inputs = {i.name for i in self.sess.get_inputs()}

    def forward(self, x: list[int]) -> np.ndarray:
        ids = np.array([x], dtype=np.int64)
        feed = {"input_ids": ids}
        if "attention_mask" in self.inputs:
            feed["attention_mask"] = np.ones_like(ids)
        if "token_type_ids" in self.inputs:
            feed["token_type_ids"] = np.zeros_like(ids)
        return self.sess.run(None, feed)[0][0].astype(np.float32)


class Tracer:
    """Perplexity's PII-Tracer through its own predict (its repository's code,
    read before running: a bidirectional Qwen3 encoder, two heads, a Viterbi)."""

    def __init__(self, path: Path):
        from transformers import AutoModel

        self.name = "pii-tracer"
        self.model = AutoModel.from_pretrained(path, trust_remote_code=True, dtype=torch.float32).eval()
        self.tok = self.model.tokenizer if hasattr(self.model, "tokenizer") else None

    def predict(self, text: str):
        with torch.inference_mode():
            spans, _ = self.model.predict(text)
        return [(s.start, s.end, s.label) for s in spans]

    def tokens(self, text: str) -> int:
        return len(self.tok(text, add_special_tokens=False)["input_ids"]) if self.tok else len(text) // 4


# GLiNER2-PII's own 42 labels, and the special categories it can be asked for by name.
GLINER_LABELS = ["person", "full_name", "first_name", "middle_name", "last_name", "date_of_birth", "email",
                 "phone_number", "address", "street_address", "city", "state_or_region", "postal_code", "country",
                 "government_id", "national_id_number", "passport_number", "drivers_license_number",
                 "license_number", "tax_id", "tax_number", "bank_account", "account_number", "routing_number",
                 "iban", "payment_card", "card_number", "card_expiry", "card_cvv", "username", "ip_address",
                 "account_id", "sensitive_account_id", "password", "secret", "api_key", "access_token",
                 "recovery_code", "sensitive_date", "document_date", "expiration_date", "transaction_date"]
GLINER_SPECIAL = ["medical_condition", "health_information", "religion", "sexual_orientation",
                  "political_affiliation", "trade_union_membership", "criminal_record"]


class Gliner:
    def __init__(self, path: Path, special: bool, threshold: float = 0.5):
        from gliner2 import GLiNER2

        self.name = "gliner2-pii" + ("+special" if special else "")
        self.model = GLiNER2.from_pretrained(str(path))
        self.labels = GLINER_LABELS + (GLINER_SPECIAL if special else [])
        self.threshold = threshold

    def predict(self, text: str):
        with torch.inference_mode():
            r = self.model.extract_entities_long(text, self.labels, threshold=self.threshold, include_spans=True)
        out = []
        for label, items in (r.get("entities") or {}).items():
            for it in items:
                if isinstance(it, dict) and "start" in it:
                    out.append((it["start"], it["end"], label))
        return out

    def tokens(self, text: str) -> int:
        return len(re.findall(r"\w+|[^\w\s]", text))


class Patterns:
    """The firewall's current detectors, through research/pii/patterns."""

    def __init__(self):
        self.name = "patterns"

    def predict_all(self, docs) -> dict:
        import subprocess

        bin_ = Path(__file__).parent / ".venv/patterns"
        lines = "".join(json.dumps({"id": d.id, "text": d.text}) + "\n" for d in docs)
        res = subprocess.run([str(bin_)], input=lines.encode(), capture_output=True, check=True)
        return {o["id"]: [tuple(s) for s in o["spans"]] for o in map(json.loads, res.stdout.decode().splitlines())}


def load(name: str):
    if name == "openai-privacy-filter":
        return TokenClassifier(name, ROOT / name, window=4096, overlap=256, viterbi=True)
    if name == "bardsai-eu-pii":
        return TokenClassifier(name, ROOT / name, window=510, overlap=64, viterbi=False)
    if name == "bardsai-eu-pii-int8":
        return OnnxClassifier(name, ROOT / "bardsai-eu-pii", "onnx/model_quantized.onnx", window=510, overlap=64,
                              viterbi=False)
    if name == "pii-tracer":
        return Tracer(ROOT / name)
    if name == "gliner2-pii":
        return Gliner(ROOT / name, special=False)
    if name == "gliner2-pii+special":
        return Gliner(ROOT / "gliner2-pii", special=True)
    if name == "patterns":
        return Patterns()
    raise SystemExit(f"unknown model {name}")
