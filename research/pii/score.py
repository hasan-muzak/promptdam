"""Scores predictions the way a firewall needs them: was each piece of personal data
covered by some mask, and how much that wasn't personal data got masked.

- recall: a gold entity is caught when masks cover at least half of its characters
  (a mask over "Ayşe" alone leaves "Yılmaz" readable, but half of a name is the
  threshold most benchmarks call a partial hit).
- char recall: the share of gold characters under a mask.
- precision: the share of predicted spans that overlap any gold entity. Benchmarks
  don't annotate everything, so this is a lower bound.
- clean flag rate: the share of clean control messages with any mask at all.
"""

from collections import defaultdict

from data import GROUPS, Doc

HIT = 0.5


def covered(mask: bytearray, s, e) -> int:
    return sum(mask[s:e])


def score(docs: list[Doc], preds: dict[str, list[tuple[int, int, str]]]) -> dict:
    by_group = defaultdict(lambda: [0, 0])  # caught, total
    by_lang = defaultdict(lambda: [0, 0])
    by_tier = defaultdict(lambda: [0, 0])
    chars_hit = chars = 0
    p_ok = p_all = 0
    clean_flagged = clean = 0
    for d in docs:
        spans = preds.get(d.id, [])
        mask = bytearray(len(d.text) + 1)
        for s, e, _ in spans:
            mask[max(0, s):max(0, e)] = b"\x01" * (max(0, e) - max(0, s))
        gold_mask = bytearray(len(d.text) + 1)
        for g in d.gold:
            gold_mask[g.start:g.end] = b"\x01" * (g.end - g.start)
            c = covered(mask, g.start, g.end)
            hit = c * 2 >= (g.end - g.start)
            for bucket in (by_group[g.group], by_lang[d.lang], by_tier[g.tier or "-"]):
                bucket[0] += hit
                bucket[1] += 1
            chars_hit += c
            chars += g.end - g.start
        for s, e, _ in spans:
            p_all += 1
            p_ok += covered(gold_mask, s, e) > 0
        if d.clean:
            clean += 1
            clean_flagged += bool(spans)
    total = [sum(v[0] for v in by_group.values()), sum(v[1] for v in by_group.values())]
    rate = lambda v: round(v[0] / v[1], 3) if v[1] else None
    return {
        "recall": rate(total),
        "char_recall": round(chars_hit / chars, 3) if chars else None,
        "precision": round(p_ok / p_all, 3) if p_all else None,
        "entities": total[1],
        "predicted": p_all,
        "clean_flag_rate": round(clean_flagged / clean, 3) if clean else None,
        "groups": {g: rate(by_group[g]) for g in GROUPS if by_group[g][1]},
        "langs": {l: rate(v) for l, v in sorted(by_lang.items())},
        "tiers": {t: rate(v) for t, v in sorted(by_tier.items())},
    }
