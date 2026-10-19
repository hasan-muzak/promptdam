"""Turns the agents' texts into training records: marks stripped, generators filled, exact span offsets.

    uv run python datagen/build.py pilot [--variants 2]
    uv run python datagen/build.py holdout

Reads data/gen/<set>/*.txt (written per SPEC.md), writes data/built/<set>.jsonl, one record per text and variant:
{id, lang, text, spans: [[start, end, label]]} with offsets in code points, and prints what it rejected and why.
Variants refill only the generated values, so labels stay exact."""

import json
import random
import re
import sys
from collections import Counter, defaultdict
from pathlib import Path

import fill

DATA = Path(__file__).parent.parent / "data"

LABELS = {
    "person", "username", "email", "phone", "address", "location", "date_of_birth", "date", "government_id",
    "account", "ip", "url_personal", "health", "religion", "sexual_orientation", "political_opinion",
    "trade_union", "criminal_record", "ethnicity", "biometric_genetic", "secret", "organization", "job_title",
    "money", "public_figure",
}

MARK = re.compile(r"⦃([=~]?)([^|⦃⦄]+?)(?:\|([^⦃⦄]*))?⦄")
HEADER = re.compile(r"^@@@\s+(\S+)\s*$")


class Reject(Exception):
    pass


def texts(path: Path):
    """Yields (id, raw text) for each text in an agent's file."""
    cur, lines = None, []
    for line in path.read_text(encoding="utf-8").splitlines():
        m = HEADER.match(line)
        if m:
            if cur:
                yield cur, "\n".join(lines).strip("\n")
            cur, lines = m.group(1), []
        elif cur:
            lines.append(line)
    if cur:
        yield cur, "\n".join(lines).strip("\n")


def render(raw: str, rng: random.Random) -> tuple[str, list[list]]:
    """The text with marks replaced by their values, and the labeled spans."""
    out, spans, last = [], [], 0
    pos = 0
    for m in MARK.finditer(raw):
        out.append(raw[last:m.start()])
        pos += m.start() - last
        last = m.end()
        kind, name, value = m.group(1), m.group(2).strip(), m.group(3)
        if kind in ("=", "~"):
            if value is not None:
                raise Reject(f"a generator takes no value: {m.group(0)[:40]}")
            if kind == "=":
                g = fill.labeled(name)
                if not g:
                    raise Reject(f"unknown generator ={name}")
                value, label = g[0](rng), g[1]
            else:
                if name not in fill.DECOYS:
                    raise Reject(f"unknown look-alike ~{name}")
                value, label = fill.DECOYS[name](rng), None
        else:
            if name not in LABELS:
                raise Reject(f"unknown label {name!r}")
            if value is None or not value.strip():
                raise Reject(f"empty value for {name}")
            label = name
        if label:
            lead = len(value) - len(value.lstrip())
            core = value.strip()
            spans.append([pos + lead, pos + lead + len(core), label])
        out.append(value)
        pos += len(value)
    out.append(raw[last:])
    text = "".join(out)
    if "⦃" in text or "⦄" in text:
        raise Reject("a broken mark is left in the text")
    for s, e, label in spans:
        if not text[s:e].strip():
            raise Reject(f"empty span for {label}")
    return text, spans


def main(set_: str, variants: int) -> None:
    src = DATA / "gen" / set_
    files = sorted(src.glob("*.txt"))
    if not files:
        raise SystemExit(f"no texts under {src}")
    out = DATA / "built" / f"{set_}.jsonl"
    out.parent.mkdir(parents=True, exist_ok=True)
    seen, rejects, labels, langs = set(), Counter(), Counter(), Counter()
    examples = defaultdict(list)
    n = 0
    with out.open("w", encoding="utf-8") as f:
        for path in files:
            for tid, raw in texts(path):
                if tid in seen:
                    rejects["duplicate id"] += 1
                    continue
                seen.add(tid)
                # The id is the language (or code set) and a number: tr-0001,
                # pt-br-0042, code3-0007, holdout's tr-h0001.
                lang = "code" if tid.startswith("code") else re.sub(r"-h?\d+$", "", tid)
                for v in range(variants):
                    rng = random.Random(f"{tid}/{v}")
                    try:
                        text, spans = render(raw, rng)
                    except Reject as e:
                        rejects[str(e).split(":")[0]] += 1
                        if len(examples[str(e)]) < 2:
                            examples[str(e)].append(tid)
                        break
                    f.write(json.dumps({"id": f"{tid}/{v}", "lang": lang, "text": text, "spans": spans},
                                       ensure_ascii=False) + "\n")
                    n += 1
                    if v == 0:
                        langs[lang] += 1
                        labels.update(s[2] for s in spans)
    print(f"{set_}: {n} records from {sum(langs.values())} texts in {len(langs)} languages -> {out}")
    print("labels:", dict(labels.most_common()))
    print("texts per language:", dict(sorted(langs.items())))
    if rejects:
        print("rejected:", dict(rejects))
        for reason, ids in list(examples.items())[:20]:
            print("  ", reason[:90], ids)


if __name__ == "__main__":
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    variants = int(sys.argv[sys.argv.index("--variants") + 1]) if "--variants" in sys.argv else 1
    main(args[0] if args else "pilot", variants)
