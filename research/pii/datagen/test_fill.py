"""Every generator against a check written independently of it, and the markup round trip.

    uv run python -m pytest datagen -q     (or: uv run python datagen/test_fill.py)"""

import random
import re

import build
import fill


def digits(s):
    return re.sub(r"\D", "", s)


def test_checksums():
    rng = random.Random(0)
    for _ in range(500):
        assert fill.luhn_ok(digits(fill.card(rng)))
        assert fill.luhn_ok(digits(fill.ca_sin(rng)))
        assert fill.luhn_ok(digits(fill.za_id(rng)))
        assert fill.luhn_ok(digits(fill.ae_eid(rng)))
        assert fill.luhn_ok(digits(fill.se_pnr(rng))[-10:])
        assert fill.verhoeff_ok(digits(fill.aadhaar(rng)))
        for cc in fill.IBAN_LEN:
            v = fill.iban(cc)(rng).replace(" ", "")
            assert len(v) == fill.IBAN_LEN[cc] and fill.iban_ok(v), (cc, v)
        t = fill.tckn(rng)
        d = list(map(int, t))
        assert d[0] != 0 and d[10] == sum(d[:10]) % 10
        assert d[9] == ((d[0] + d[2] + d[4] + d[6] + d[8]) * 7 - (d[1] + d[3] + d[5] + d[7])) % 10
        b = digits(fill.nl_bsn(rng))
        assert (sum(int(x) * w for x, w in zip(b[:8], range(9, 1, -1))) - int(b[8])) % 11 == 0
        n = digits(fill.nhs(rng))
        assert (sum(int(x) * w for x, w in zip(n, range(10, 0, -1)))) % 11 == 0 or n[-1] == "0"
        dni = fill.es_dni(rng)
        assert "TRWAGMYFPDXBNJZSQVHLCKE"[int(dni[:-1]) % 23] == dni[-1]
        cpf = digits(fill.cpf(rng))
        assert len(cpf) == 11
        cn = fill.cn_id(rng)
        w = [7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2]
        assert "10X98765432"[sum(int(x) * y for x, y in zip(cn[:17], w)) % 11] == cn[17]
        p = fill.pl_pesel(rng)
        assert sum(int(x) * y for x, y in zip(p, [1, 3, 7, 9, 1, 3, 7, 9, 1, 3, 1])) % 10 == 0
        tfn = digits(fill.au_tfn(rng))
        assert sum(int(x) * y for x, y in zip(tfn, [1, 4, 3, 7, 5, 8, 6, 9, 10])) % 11 == 0
        il = fill.il_id(rng)
        assert sum(sum(divmod(int(x) * (1 + i % 2), 10)) for i, x in enumerate(il)) % 10 == 0


def test_every_generator_runs():
    rng = random.Random(1)
    for name in [*fill.IDS, *fill.SECRETS, "card", "iban_de"]:
        g = fill.labeled(name)
        assert g and g[0](rng), name
    for name, g in fill.DECOYS.items():
        assert g(rng), name


def test_render_marks():
    rng = random.Random(2)
    text, spans = build.render("Ben ⦃person|Ayşe Yılmaz⦄, TC ⦃=tckn⦄, iz ⦃~tracking_number⦄.", rng)
    assert text.startswith("Ben Ayşe Yılmaz, TC ")
    assert [s[2] for s in spans] == ["person", "government_id"]
    s, e, _ = spans[0]
    assert text[s:e] == "Ayşe Yılmaz"
    assert len(digits(text[spans[1][0]:spans[1][1]])) == 11


def test_render_rejects():
    rng = random.Random(3)
    for bad in ["⦃nope|x⦄", "⦃person|⦄", "⦃=nope⦄", "⦃person|a", "⦃~uuid|x⦄"]:
        try:
            build.render(bad, rng)
        except build.Reject:
            continue
        raise AssertionError(bad)


if __name__ == "__main__":
    for t in [test_checksums, test_every_generator_runs, test_render_marks, test_render_rejects]:
        t()
    print("ok")
