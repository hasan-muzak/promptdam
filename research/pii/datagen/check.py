"""Checks one agent's file against SPEC.md and its assignment: every text present, every mark valid.

    uv run python datagen/check.py data/gen/pilot/tr.part1.txt [data/plan/pilot/tr.txt]

Prints each problem with the text's id, then a summary line; exits 1 when anything is wrong, so an agent can
fix its file before handing it back."""

import random
import sys
from collections import Counter
from pathlib import Path

import build


def main(path: str, plan: str | None) -> int:
    problems, labels, ids = [], Counter(), []
    for tid, raw in build.texts(Path(path)):
        ids.append(tid)
        if not raw.strip():
            problems.append(f"{tid}: empty text")
            continue
        try:
            text, spans = build.render(raw, random.Random(tid))
        except build.Reject as e:
            problems.append(f"{tid}: {e}")
            continue
        labels.update(s[2] for s in spans)
    if plan:
        want = [line.split("|")[0].strip() for line in Path(plan).read_text().splitlines()
                if line.strip() and not line.startswith("#")]
        have = set(ids)
        part = [w for w in want if w in have]
        if part:
            lo, hi = want.index(part[0]), want.index(part[-1])
            missing = [w for w in want[lo:hi + 1] if w not in have]
            problems += [f"{m}: missing between {part[0]} and {part[-1]}" for m in missing]
        dupes = [i for i, c in Counter(ids).items() if c > 1]
        problems += [f"{d}: written twice" for d in dupes]
    for p in problems:
        print(p)
    print(f"{path}: {len(ids)} texts, {len(problems)} problems, labels {dict(labels.most_common())}")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1], sys.argv[2] if len(sys.argv) > 2 else None))
