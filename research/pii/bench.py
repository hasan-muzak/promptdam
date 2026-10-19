"""Runs one candidate over one benchmark: predictions to results/raw, the score and
the CPU cost to results/<model>-<set>.json.

    uv run python bench.py <model> <set> [limit] [--union patterns]

--union adds another run's saved predictions (e.g. the patterns') before scoring,
to measure the hybrid."""

import json
import os
import sys
import time
from pathlib import Path

import torch

import data
import models
from score import score

OUT = Path(__file__).parent / "results"

# The M1 Pro's 8 performance cores; a deployment would run several detectors with
# fewer threads each, so per-core figures are derived from this.
torch.set_num_threads(int(os.environ.get("THREADS", "8")))


def run(name: str, set_: str, limit: int) -> dict:
    docs = data.SETS[set_](limit)
    raw = OUT / "raw" / f"{name}-{set_}.jsonl"
    raw.parent.mkdir(parents=True, exist_ok=True)
    m = models.load(name)
    timing = {}
    if hasattr(m, "predict_all"):
        t0 = time.perf_counter()
        preds = m.predict_all(docs)
        timing = {"seconds": round(time.perf_counter() - t0, 3)}
    else:
        m.predict(docs[0].text)  # warm up
        preds, secs, toks, chars = {}, [], 0, 0
        for i, d in enumerate(docs):
            t0 = time.perf_counter()
            preds[d.id] = m.predict(d.text)
            secs.append(time.perf_counter() - t0)
            toks += m.tokens(d.text)
            chars += len(d.text)
            if i % 50 == 0:
                print(f"{name} {set_} {i}/{len(docs)} {sum(secs):.0f}s", file=sys.stderr, flush=True)
        secs.sort()
        timing = {
            "threads": torch.get_num_threads(),
            "seconds": round(sum(secs), 2),
            "tokens_per_s": round(toks / sum(secs)),
            "ms_per_1k_chars": round(1000 * sum(secs) / (chars / 1000), 1),
            "p50_ms": round(1000 * secs[len(secs) // 2], 1),
            "p99_ms": round(1000 * secs[int(len(secs) * 0.99)], 1),
            "avg_chars": round(chars / len(docs)),
        }
    with raw.open("w") as f:
        for d in docs:
            f.write(json.dumps({"id": d.id, "spans": preds.get(d.id, [])}, ensure_ascii=False) + "\n")
    return {"model": name, "set": set_, "docs": len(docs), "timing": timing, "score": score(docs, preds)}


def union(name: str, set_: str, other: str, limit: int) -> dict:
    """Scores the masks of several saved runs together; other is one run or
    several, comma-separated."""
    docs = data.SETS[set_](limit)
    preds = {}
    for n in (name, *other.split(",")):
        for line in (OUT / "raw" / f"{n}-{set_}.jsonl").open():
            o = json.loads(line)
            preds.setdefault(o["id"], []).extend(tuple(s) for s in o["spans"])
    return {"model": "+".join([name, *other.split(",")]), "set": set_, "docs": len(docs), "score": score(docs, preds)}


if __name__ == "__main__":
    argv = sys.argv[1:]
    other = None
    if "--union" in argv:
        i = argv.index("--union")
        other, argv = argv[i + 1], argv[:i] + argv[i + 2:]
    name, set_ = argv[0], argv[1]
    limit = int(argv[2]) if len(argv) > 2 else 0
    if other:
        res = union(name, set_, other, limit)
    else:
        res = run(name, set_, limit)
    OUT.mkdir(exist_ok=True)
    (OUT / f"{res['model']}-{set_}.json").write_text(json.dumps(res, indent=1, ensure_ascii=False))
    print(json.dumps({k: res[k] for k in ("model", "set", "docs")} | {"timing": res.get("timing"),
          "recall": res["score"]["recall"], "precision": res["score"]["precision"],
          "groups": res["score"]["groups"], "clean": res["score"]["clean_flag_rate"]}, ensure_ascii=False))
