"""The teacher check: bardsai reads every built text and is compared with our marks. Where it finds personal data
we didn't mark, the mark is probably missing (or bardsai is wrong: a reviewer decides); where it misses what we
marked, that is what our model has to learn beyond it.

    uv run python datagen/teach.py pilot [--per-lang 8]

Prints, per language and label, how much of what we marked bardsai catches, and how many of its spans touch no
mark of ours. Writes those spans with their context to data/built/<set>.suspects.txt, a sample per language, for
the reviewers."""

import argparse
import json
import random
import sys
from collections import defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent))
sys.path.insert(0, str(Path(__file__).parent.parent / "train"))

import models  # noqa: E402
from train import CONTEXT, LABELS  # noqa: E402

DATA = Path(__file__).parent.parent / "data"
V0 = Path(__file__).parent.parent.parent.parent / "detector" / "labels" / "bardsai-v0.json"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("set")
    ap.add_argument("--per-lang", type=int, default=8)
    args = ap.parse_args()
    # bardsai's labels -> the firewall's (the service's map) -> ours (train.py's map, inverted).
    ours = {fw: o for o, fw in LABELS.items()}
    teacher = {b: ours[fw] for b, fw in json.loads(V0.read_text())["labels"].items()}
    m = models.load("bardsai-eu-pii-int8")

    recs = [json.loads(line) for line in (DATA / "built" / f"{args.set}.jsonl").open(encoding="utf-8")]
    caught = defaultdict(lambda: [0, 0])  # (lang, label) -> caught, marked
    extra = defaultdict(int)  # (lang, label) -> teacher spans touching no mark
    suspects = defaultdict(list)
    for i, r in enumerate(recs):
        text, lang = r["text"], r["lang"]
        marks = r["spans"]
        hit = bytearray(len(text) + 1)
        for s, e, _ in marks:
            hit[s:e] = b"\x01" * (e - s)
        found = [(s, e, teacher[c]) for s, e, c in m.predict(text) if c in teacher]
        mine = bytearray(len(text) + 1)
        for s, e, _ in found:
            mine[s:e] = b"\x01" * (e - s)
        for s, e, label in marks:
            if LABELS.get(label) and label not in CONTEXT:
                k = caught[lang, label]
                k[0] += sum(mine[s:e]) * 2 >= e - s
                k[1] += 1
        for s, e, label in found:
            if not any(hit[s:e]):
                extra[lang, label] += 1
                lo, hi = max(0, s - 60), min(len(text), e + 60)
                ctx = (text[lo:s] + "[[" + text[s:e] + "]]" + text[e:hi]).replace("\n", " ")
                suspects[lang].append(f"{r['id']}  {label}: {ctx}")
        if i % 500 == 0:
            print(f"{i}/{len(recs)}", file=sys.stderr, flush=True)

    labels = sorted({k[1] for k in caught} | {k[1] for k in extra})
    langs = sorted({k[0] for k in caught} | {k[0] for k in extra})
    print(f"{'label':20} {'marked':>7} {'bardsai caught':>15} {'unmarked finds':>15}")
    for label in labels:
        c = [sum(caught[lang, label][0] for lang in langs), sum(caught[lang, label][1] for lang in langs)]
        x = sum(extra[lang, label] for lang in langs)
        print(f"{label:20} {c[1]:7} {c[0] / c[1] if c[1] else 0:15.2f} {x:15}")
    print(f"\n{'lang':8} {'caught':>7} {'unmarked finds per text':>24}")
    texts = defaultdict(int)
    for r in recs:
        texts[r["lang"]] += 1
    for lang in langs:
        c = [sum(caught[lang, label][0] for label in labels), sum(caught[lang, label][1] for label in labels)]
        x = sum(extra[lang, label] for label in labels)
        print(f"{lang:8} {c[0] / c[1] if c[1] else 0:7.2f} {x / texts[lang]:24.2f}")

    out = DATA / "built" / f"{args.set}.suspects.txt"
    rng = random.Random(13)
    with out.open("w", encoding="utf-8") as f:
        for lang in langs:
            pick = suspects[lang] if len(suspects[lang]) <= args.per_lang else rng.sample(suspects[lang], args.per_lang)
            f.write(f"## {lang} ({len(suspects[lang])} unmarked finds)\n" + "".join(p + "\n" for p in sorted(pick)) + "\n")
    print(f"\nsamples: {out}")


if __name__ == "__main__":
    main()
