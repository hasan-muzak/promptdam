"""Generated values for the agents' ⦃=…⦄ and ⦃~…⦄ marks: ID numbers that pass their country's checksum, secrets
in each vendor's shape, and the look-alikes the model must leave alone.

Nothing generated here is real, and nothing is written to git: build.py fills texts under data/, which is
ignored. Validators for the checksums live beside the generators and test_fill.py checks every generator
against its own validator, so a wrong formula can't teach the model a wrong shape."""

import base64
import json
import string
from datetime import date, timedelta

DIGITS, UPPER, LOWER = string.digits, string.ascii_uppercase, string.ascii_lowercase
ALNUM = DIGITS + UPPER + LOWER
URLSAFE = ALNUM + "-_"
HEX = "0123456789abcdef"


def pick(rng, chars, n):
    return "".join(rng.choice(chars) for _ in range(n))


def birth(rng) -> date:
    return date(1940, 1, 1) + timedelta(days=rng.randrange(0, 365 * 65))


def luhn_digit(payload: str) -> str:
    """The Luhn check digit for payload (the digit appended at the right)."""
    total = 0
    for i, ch in enumerate(reversed(payload)):
        d = int(ch)
        if i % 2 == 0:
            d *= 2
            if d > 9:
                d -= 9
        total += d
    return str((10 - total % 10) % 10)


def luhn_ok(number: str) -> bool:
    return luhn_digit(number[:-1]) == number[-1]


# Verhoeff (Aadhaar).
_VD = [[0, 1, 2, 3, 4, 5, 6, 7, 8, 9], [1, 2, 3, 4, 0, 6, 7, 8, 9, 5], [2, 3, 4, 0, 1, 7, 8, 9, 5, 6],
       [3, 4, 0, 1, 2, 8, 9, 5, 6, 7], [4, 0, 1, 2, 3, 9, 5, 6, 7, 8], [5, 9, 8, 7, 6, 0, 4, 3, 2, 1],
       [6, 5, 9, 8, 7, 1, 0, 4, 3, 2], [7, 6, 5, 9, 8, 2, 1, 0, 4, 3], [8, 7, 6, 5, 9, 3, 2, 1, 0, 4],
       [9, 8, 7, 6, 5, 4, 3, 2, 1, 0]]
_VP = [[0, 1, 2, 3, 4, 5, 6, 7, 8, 9], [1, 5, 7, 6, 2, 8, 3, 0, 9, 4], [5, 8, 0, 3, 7, 9, 6, 1, 4, 2],
       [8, 9, 1, 6, 0, 4, 3, 5, 2, 7], [9, 4, 5, 3, 1, 2, 6, 8, 7, 0], [4, 2, 8, 6, 5, 7, 3, 9, 0, 1],
       [2, 7, 9, 3, 8, 0, 6, 4, 1, 5], [7, 0, 4, 6, 9, 1, 3, 2, 5, 8]]
_VINV = [0, 4, 3, 2, 1, 5, 6, 7, 8, 9]


def verhoeff_digit(payload: str) -> str:
    c = 0
    for i, ch in enumerate(reversed(payload)):
        c = _VD[c][_VP[(i + 1) % 8][int(ch)]]
    return str(_VINV[c])


def verhoeff_ok(number: str) -> bool:
    c = 0
    for i, ch in enumerate(reversed(number)):
        c = _VD[c][_VP[i % 8][int(ch)]]
    return c == 0


def mod11_weights(digits: str, weights) -> int:
    return sum(int(d) * w for d, w in zip(digits, weights)) % 11


# ---- ID numbers: each returns the value as people write it. ----

def tckn(rng):
    d = [rng.randint(1, 9)] + [rng.randint(0, 9) for _ in range(8)]
    d.append(((d[0] + d[2] + d[4] + d[6] + d[8]) * 7 - (d[1] + d[3] + d[5] + d[7])) % 10)
    d.append(sum(d) % 10)
    return "".join(map(str, d))


def aadhaar(rng):
    p = str(rng.randint(2, 9)) + pick(rng, DIGITS, 10)
    n = p + verhoeff_digit(p)
    return rng.choice([n, f"{n[:4]} {n[4:8]} {n[8:]}"])


def pan_in(rng):
    return pick(rng, UPPER, 3) + "P" + pick(rng, UPPER, 1) + pick(rng, DIGITS, 4) + pick(rng, UPPER, 1)


def _cpf_digit(nums):
    s = sum(n * w for n, w in zip(nums, range(len(nums) + 1, 1, -1)))
    r = s * 10 % 11
    return 0 if r == 10 else r


def cpf(rng):
    n = [rng.randint(0, 9) for _ in range(9)]
    if len(set(n)) == 1:
        n[0] = (n[0] + 1) % 10
    n.append(_cpf_digit(n))
    n.append(_cpf_digit(n))
    s = "".join(map(str, n))
    return rng.choice([s, f"{s[:3]}.{s[3:6]}.{s[6:9]}-{s[9:]}"])


def _cnpj_digit(nums, weights):
    r = sum(n * w for n, w in zip(nums, weights)) % 11
    return 0 if r < 2 else 11 - r


def cnpj(rng):
    n = [rng.randint(0, 9) for _ in range(8)] + [0, 0, 0, 1]
    n.append(_cnpj_digit(n, [5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2]))
    n.append(_cnpj_digit(n, [6, 5, 4, 3, 2, 9, 8, 7, 6, 5, 4, 3, 2]))
    s = "".join(map(str, n))
    return f"{s[:2]}.{s[2:5]}.{s[5:8]}/{s[8:12]}-{s[12:]}"


def cn_id(rng):
    b = birth(rng)
    body = rng.choice(["110101", "310104", "440305", "510107", "330106"]) + b.strftime("%Y%m%d") + pick(rng, DIGITS, 3)
    w = [7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2]
    return body + "10X98765432"[sum(int(d) * x for d, x in zip(body, w)) % 11]


def my_number(rng):
    p = pick(rng, DIGITS, 11)
    s = sum(int(d) * (n + 1 if n <= 6 else n - 5) for n, d in enumerate(reversed(p), start=1))
    r = s % 11
    n = p + str(0 if r <= 1 else 11 - r)
    return rng.choice([n, f"{n[:4]} {n[4:8]} {n[8:]}"])


def kr_rrn(rng):
    b = birth(rng)
    g = rng.choice("12") if b.year < 2000 else rng.choice("34")
    body = b.strftime("%y%m%d") + g + pick(rng, DIGITS, 5)
    w = [2, 3, 4, 5, 6, 7, 8, 9, 2, 3, 4, 5]
    c = (11 - sum(int(d) * x for d, x in zip(body, w)) % 11) % 10
    return f"{body[:6]}-{body[6:]}{c}"


def sg_nric(rng):
    first = rng.choice("STFG")
    d = pick(rng, DIGITS, 7)
    s = sum(int(x) * w for x, w in zip(d, [2, 7, 6, 5, 4, 3, 2])) + (4 if first in "TG" else 0)
    table = "JZIHGFEDCBA" if first in "ST" else "XWUTRQPNMLK"
    return first + d + table[s % 11]


def au_tfn(rng):
    w = [1, 4, 3, 7, 5, 8, 6, 9, 10]
    while True:
        d = pick(rng, DIGITS, 8)
        s = sum(int(x) * y for x, y in zip(d, w))
        last = s % 11  # 10·d9 ≡ −d9 (mod 11), so d9 ≡ s
        if last <= 9:
            n = d + str(last)
            return rng.choice([n, f"{n[:3]} {n[3:6]} {n[6:]}"])


def au_medicare(rng):
    d = str(rng.randint(2, 6)) + pick(rng, DIGITS, 7)
    c = sum(int(x) * w for x, w in zip(d, [1, 3, 7, 9, 1, 3, 7, 9])) % 10
    issue = str(rng.randint(1, 9))
    return rng.choice([d + str(c) + issue, f"{d[:4]} {d[4:]}{c} {issue}"])


def uk_nino(rng):
    first, second = "ABCEGHJKLMNOPRSTWXYZ", "ABCEGHJKLMNPRSTWXYZ"
    while True:
        p = rng.choice(first) + rng.choice(second)
        if p not in {"BG", "GB", "NK", "KN", "TN", "NT", "ZZ"}:
            break
    d = pick(rng, DIGITS, 6)
    s = rng.choice("ABCD")
    return rng.choice([f"{p}{d}{s}", f"{p} {d[:2]} {d[2:4]} {d[4:]} {s}"])


def nhs(rng):
    while True:
        d = pick(rng, DIGITS, 9)
        r = 11 - sum(int(x) * w for x, w in zip(d, range(10, 1, -1))) % 11
        if r == 10:
            continue
        n = d + str(0 if r == 11 else r)
        return rng.choice([n, f"{n[:3]} {n[3:6]} {n[6:]}"])


def de_steuer_id(rng):
    while True:
        pool = rng.sample(DIGITS, 9)
        twice = rng.choice(pool)
        d = pool + [twice]
        rng.shuffle(d)
        if d[0] != "0":
            break
    product = 10
    for x in d:
        s = (int(x) + product) % 10 or 10
        product = (2 * s) % 11
    c = 11 - product
    n = "".join(d) + str(0 if c == 10 else c)
    return rng.choice([n, f"{n[:2]} {n[2:5]} {n[5:8]} {n[8:]}"])


def fr_nir(rng):
    b = birth(rng)
    body = rng.choice("12") + b.strftime("%y%m") + f"{rng.randint(1, 95):02d}" + pick(rng, DIGITS, 6)
    key = 97 - int(body) % 97
    return rng.choice([f"{body}{key:02d}", f"{body[0]} {body[1:3]} {body[3:5]} {body[5:7]} {body[7:10]} "
                                           f"{body[10:]} {key:02d}"])


_DNI = "TRWAGMYFPDXBNJZSQVHLCKE"


def es_dni(rng):
    n = rng.randint(10_000_000, 99_999_999)
    return f"{n}{_DNI[n % 23]}"


def es_nie(rng):
    p = rng.choice("XYZ")
    d = pick(rng, DIGITS, 7)
    return f"{p}{d}{_DNI[int(str('XYZ'.index(p)) + d) % 23]}"


_CF_ODD = dict(zip("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ",
                   [1, 0, 5, 7, 9, 13, 15, 17, 19, 21, 1, 0, 5, 7, 9, 13, 15, 17, 19, 21, 2, 4, 18, 20, 11, 3, 6,
                    8, 12, 14, 16, 10, 22, 25, 24, 23]))


def it_cf(rng):
    b = birth(rng)
    day = b.day + (40 if rng.random() < 0.5 else 0)
    body = (pick(rng, "BCDFGLMNPRSTVZ", 3) + pick(rng, "BCDFGLMNPRSTVZAEIOU", 3) + b.strftime("%y")
            + "ABCDEHLMPRST"[b.month - 1] + f"{day:02d}" + rng.choice(["H501", "F205", "L219", "G273", "A944"]))
    s = 0
    for i, ch in enumerate(body):
        s += _CF_ODD[ch] if i % 2 == 0 else (int(ch) if ch.isdigit() else ord(ch) - 65)
    return body + chr(65 + s % 26)


def nl_bsn(rng):
    while True:
        d = pick(rng, DIGITS, 8)
        s = sum(int(x) * w for x, w in zip(d, range(9, 1, -1)))
        last = s % 11
        if last <= 9 and d[0] != "0":
            return d + str(last)


def pl_pesel(rng):
    b = birth(rng)
    month = b.month + (20 if b.year >= 2000 else 0)
    body = f"{b.year % 100:02d}{month:02d}{b.day:02d}" + pick(rng, DIGITS, 4)
    w = [1, 3, 7, 9, 1, 3, 7, 9, 1, 3]
    return body + str((10 - sum(int(x) * y for x, y in zip(body, w)) % 10) % 10)


def se_pnr(rng):
    b = birth(rng)
    body = b.strftime("%y%m%d") + pick(rng, DIGITS, 3)
    n = body + luhn_digit(body)
    return rng.choice([f"{n[:6]}-{n[6:]}", f"{b.strftime('%Y')[:2]}{n[:6]}-{n[6:]}"])


def fi_hetu(rng):
    b = birth(rng)
    ind = f"{rng.randint(2, 899):03d}"
    c = "0123456789ABCDEFHJKLMNPRSTUVWXY"[int(b.strftime("%d%m%y") + ind) % 31]
    return f"{b.strftime('%d%m%y')}{'A' if b.year >= 2000 else '-'}{ind}{c}"


def dk_cpr(rng):
    return f"{birth(rng).strftime('%d%m%y')}-{pick(rng, DIGITS, 4)}"


def no_fnr(rng):
    while True:
        b = birth(rng)
        body = b.strftime("%d%m%y") + f"{rng.randint(0, 499):03d}"
        k1 = 11 - mod11_weights(body, [3, 7, 6, 1, 8, 9, 4, 5, 2])
        k1 = 0 if k1 == 11 else k1
        if k1 == 10:
            continue
        k2 = 11 - mod11_weights(body + str(k1), [5, 4, 3, 2, 7, 6, 5, 4, 3, 2])
        k2 = 0 if k2 == 11 else k2
        if k2 != 10:
            return f"{body}{k1}{k2}"


def be_nn(rng):
    b = birth(rng)
    body = b.strftime("%y%m%d") + f"{rng.randint(1, 997):03d}"
    key = 97 - int(("2" if b.year >= 2000 else "") + body) % 97
    return f"{body[:2]}.{body[2:4]}.{body[4:6]}-{body[6:]}.{key:02d}"


def pt_nif(rng):
    d = rng.choice("123") + pick(rng, DIGITS, 7)
    r = sum(int(x) * w for x, w in zip(d, range(9, 1, -1))) % 11
    return d + str(0 if r < 2 else 11 - r)


_CURP = "0123456789ABCDEFGHIJKLMNÑOPQRSTUVWXYZ"


def mx_curp(rng):
    b = birth(rng)
    body = (pick(rng, UPPER, 1) + rng.choice("AEIOU") + pick(rng, UPPER, 2) + b.strftime("%y%m%d") + rng.choice("HM")
            + rng.choice(["DF", "JC", "NL", "MC", "PL", "VZ"]) + pick(rng, "BCDFGHJKLMNPQRSTVWXYZ", 3)
            + (rng.choice(DIGITS) if b.year < 2000 else rng.choice(UPPER)))
    s = sum(_CURP.index(ch) * (18 - i) for i, ch in enumerate(body))
    return body + str((10 - s % 10) % 10)


def mx_rfc(rng):
    return pick(rng, UPPER, 4) + birth(rng).strftime("%y%m%d") + pick(rng, UPPER + DIGITS, 3)


def ca_sin(rng):
    body = rng.choice("1234567") + pick(rng, DIGITS, 7)
    n = body + luhn_digit(body)
    return rng.choice([n, f"{n[:3]} {n[3:6]} {n[6:]}", f"{n[:3]}-{n[3:6]}-{n[6:]}"])


def za_id(rng):
    body = birth(rng).strftime("%y%m%d") + pick(rng, DIGITS, 4) + rng.choice("01") + "8"
    return body + luhn_digit(body)


def il_id(rng):
    body = pick(rng, DIGITS, 8)
    for c in DIGITS:  # Israel's check: alternating 1,2 weights with digit sums, total ≡ 0 (mod 10)
        n = body + c
        if sum(sum(divmod(int(x) * (1 + i % 2), 10)) for i, x in enumerate(n)) % 10 == 0:
            return n
    raise AssertionError


def ae_eid(rng):
    body = "784" + str(birth(rng).year) + pick(rng, DIGITS, 7)
    n = body + luhn_digit(body)
    return f"{n[:3]}-{n[3:7]}-{n[7:14]}-{n[14]}"


def us_ssn(rng):
    area = rng.choice([x for x in range(1, 900) if x != 666])
    return f"{area:03d}-{rng.randint(1, 99):02d}-{rng.randint(1, 9999):04d}"


def us_itin(rng):
    group = rng.choice(list(range(70, 89)) + [90, 91, 92] + list(range(94, 100)))
    return f"9{rng.randint(0, 99):02d}-{group}-{rng.randint(0, 9999):04d}"


def passport(rng):
    return rng.choice([pick(rng, UPPER, 1) + pick(rng, DIGITS, 8), pick(rng, UPPER, 2) + pick(rng, DIGITS, 7)])


IBAN_LEN = {"at": 20, "be": 16, "bg": 22, "ch": 21, "cz": 24, "de": 22, "dk": 18, "ee": 20, "es": 24, "fi": 18,
            "fr": 27, "gb": 22, "gr": 27, "hr": 21, "hu": 28, "ie": 22, "il": 23, "ir": 26, "it": 27, "lt": 20,
            "lv": 21, "nl": 18, "no": 15, "pl": 28, "pt": 25, "ro": 24, "rs": 22, "sa": 24, "ae": 23, "se": 24,
            "si": 19, "sk": 24, "tr": 26, "ua": 29}
IBAN_LETTERS = {"gb": 4, "ie": 4, "nl": 4, "lv": 4, "ro": 4, "bg": 4}


def iban_check(cc: str, bban: str) -> str:
    num = "".join(str(int(c, 36)) for c in bban + cc.upper() + "00")
    return f"{98 - int(num) % 97:02d}"


def iban_ok(iban: str) -> bool:
    s = iban.replace(" ", "")
    return int("".join(str(int(c, 36)) for c in s[4:] + s[:4])) % 97 == 1


def iban(cc):
    def gen(rng):
        n = IBAN_LEN[cc] - 4
        letters = IBAN_LETTERS.get(cc, 0)
        bban = pick(rng, UPPER, letters) + pick(rng, DIGITS, n - letters)
        s = cc.upper() + iban_check(cc, bban) + bban
        return rng.choice([s, " ".join(s[i:i + 4] for i in range(0, len(s), 4))])
    return gen


def card(rng):
    kind = rng.choice(["visa", "mc", "amex"])
    if kind == "amex":
        body = rng.choice(["34", "37"]) + pick(rng, DIGITS, 12)
        n = body + luhn_digit(body)
        return rng.choice([n, f"{n[:4]} {n[4:10]} {n[10:]}"])
    body = ("4" if kind == "visa" else str(rng.randint(51, 55))) + pick(rng, DIGITS, 15 - (1 if kind == "visa" else 2))
    n = body + luhn_digit(body)
    return rng.choice([n, " ".join(n[i:i + 4] for i in range(0, 16, 4)), "-".join(n[i:i + 4] for i in range(0, 16, 4))])


# ---- Secrets ----

def b64url(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def jwt(rng):
    head = b64url(json.dumps({"alg": rng.choice(["HS256", "RS256"]), "typ": "JWT"}).encode())
    claims = {"sub": pick(rng, DIGITS, 8), "iat": 1700000000 + rng.randrange(10 ** 8), "role": rng.choice(["user",
              "admin", "service"])}
    return f"{head}.{b64url(json.dumps(claims).encode())}.{pick(rng, URLSAFE, 43)}"


def private_key(rng):
    kind = rng.choice(["RSA PRIVATE KEY", "PRIVATE KEY", "OPENSSH PRIVATE KEY", "EC PRIVATE KEY"])
    body = base64.b64encode(bytes(rng.randrange(256) for _ in range(rng.choice([96, 384, 900])))).decode()
    lines = "\n".join(body[i:i + 64] for i in range(0, len(body), 64))
    return f"-----BEGIN {kind}-----\n{lines}\n-----END {kind}-----"


_WORDS = ["summer", "winter", "dragon", "monkey", "sunshine", "princess", "football", "shadow", "master", "galaxy",
          "kartal", "istanbul", "berlin", "tigre", "amore", "sakura", "lucky", "falcon", "rocket", "coffee"]


def password(rng):
    style = rng.random()
    if style < 0.4:
        return rng.choice(_WORDS).capitalize() + str(rng.randint(1, 2025)) + rng.choice(["!", "#", "?", "", "*"])
    if style < 0.7:
        return pick(rng, ALNUM + "!@#$%^&*", rng.randint(10, 24))
    return "-".join(rng.choice(_WORDS) for _ in range(rng.randint(3, 4))) + str(rng.randint(0, 99))


def generic_token(rng):
    style = rng.random()
    if style < 0.35:
        return pick(rng, ALNUM, rng.choice([32, 40, 48, 64]))
    if style < 0.6:
        return pick(rng, HEX, rng.choice([32, 40, 64]))
    prefix = rng.choice(["tok_", "key_", "acme_sk_", "pk_srv_", "live_", "vk_", "api-", "zz_"])
    return prefix + pick(rng, URLSAFE, rng.choice([24, 32, 40]))


SECRETS = {
    "stripe_secret": lambda r: "sk_live_" + pick(r, ALNUM, r.choice([24, 99])),
    "stripe_restricted": lambda r: "rk_live_" + pick(r, ALNUM, 24),
    "aws_access_key": lambda r: r.choice(["AKIA", "ASIA"]) + pick(r, UPPER + "234567", 16),
    "aws_secret": lambda r: pick(r, ALNUM + "/+", 40),
    "gcp_api_key": lambda r: "AIza" + pick(r, URLSAFE, 35),
    "github_token": lambda r: r.choice(["ghp_", "gho_", "ghs_"]) + pick(r, ALNUM, 36),
    "github_pat": lambda r: "github_pat_" + pick(r, ALNUM, 22) + "_" + pick(r, ALNUM, 59),
    "gitlab_token": lambda r: "glpat-" + pick(r, URLSAFE, 20),
    "slack_token": lambda r: r.choice(["xoxb-", "xoxp-"]) + pick(r, DIGITS, 12) + "-" + pick(r, DIGITS, 13) + "-"
    + pick(r, ALNUM, 24),
    "slack_webhook": lambda r: "https://hooks.slack.com/services/T" + pick(r, UPPER + DIGITS, 8) + "/B"
    + pick(r, UPPER + DIGITS, 10) + "/" + pick(r, ALNUM, 24),
    "openai_key": lambda r: "sk-proj-" + pick(r, URLSAFE, r.choice([48, 120, 156])),
    "anthropic_key": lambda r: "sk-ant-api03-" + pick(r, URLSAFE, 93) + "AA",
    "huggingface_token": lambda r: "hf_" + pick(r, ALNUM, 34),
    "npm_token": lambda r: "npm_" + pick(r, ALNUM, 36),
    "twilio_auth": lambda r: pick(r, HEX, 32),
    "sendgrid_key": lambda r: "SG." + pick(r, URLSAFE, 22) + "." + pick(r, URLSAFE, 43),
    "mailgun_key": lambda r: "key-" + pick(r, HEX, 32),
    "shopify_token": lambda r: r.choice(["shpat_", "shpss_"]) + pick(r, HEX, 32),
    "discord_token": lambda r: pick(r, ALNUM, 24) + "." + pick(r, URLSAFE, 6) + "." + pick(r, URLSAFE, 27),
    "telegram_token": lambda r: str(r.randint(10 ** 8, 10 ** 10)) + ":" + pick(r, URLSAFE, 35),
    "datadog_key": lambda r: pick(r, HEX, 32),
    "azure_key": lambda r: base64.b64encode(bytes(r.randrange(256) for _ in range(64))).decode(),
    "jwt": jwt,
    "private_key": private_key,
    "password": password,
    "generic_token": generic_token,
    "basic_auth": lambda r: base64.b64encode(f"{r.choice(['admin', 'deploy', 'svc', 'api'])}:{password(r)}".encode())
    .decode(),
    "bearer": lambda r: r.choice([jwt(r), pick(r, URLSAFE + ".", 40)]),
}

IDS = {f.__name__: f for f in [tckn, aadhaar, pan_in, cpf, cnpj, cn_id, my_number, kr_rrn, sg_nric, au_tfn,
                               au_medicare, uk_nino, nhs, de_steuer_id, fr_nir, es_dni, es_nie, it_cf, nl_bsn,
                               pl_pesel, se_pnr, fi_hetu, dk_cpr, no_fnr, be_nn, pt_nif, mx_curp, mx_rfc, ca_sin,
                               za_id, il_id, ae_eid, us_ssn, us_itin, passport]}

# The AWS documentation's own example credentials, assembled so no scanner reads the source as a leak.
_AWS_EXAMPLE = ["AKIA" + "IOSFODNN7" + "EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCY" + "EXAMPLEKEY"]

DECOYS = {
    "uuid": lambda r: "-".join(pick(r, HEX, n) for n in (8, 4, 4, 4, 12)),
    "sha1": lambda r: pick(r, HEX, 40),
    "sha256": lambda r: pick(r, HEX, 64),
    "git_sha": lambda r: pick(r, HEX, r.choice([7, 8, 40])),
    "stripe_publishable": lambda r: "pk_live_" + pick(r, ALNUM, 24),
    "example_aws_key": lambda r: r.choice(_AWS_EXAMPLE),
    "placeholder_key": lambda r: r.choice(["your-api-key-here", "<YOUR_API_KEY>", "xxxxxxxxxxxxxxxxxxxx",
                                           "sk_test_YOUR_KEY", "REPLACE_ME", "${API_KEY}", "<token>", "***"]),
    "public_key": lambda r: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI" + pick(r, ALNUM + "+/", 43) + " "
    + r.choice(["deploy@ci", "dev@laptop", "git@build"]),
    "base64_blob": lambda r: "data:image/png;base64," + base64.b64encode(bytes(r.randrange(256) for _ in range(150)))
    .decode(),
    "order_number": lambda r: r.choice(["ORD-", "#", "SO-", ""]) + str(r.randint(10 ** 5, 10 ** 9)),
    "tracking_number": lambda r: r.choice(["1Z" + pick(r, UPPER + DIGITS, 16), pick(r, DIGITS, 12),
                                           pick(r, UPPER, 2) + pick(r, DIGITS, 9) + pick(r, UPPER, 2)]),
    "invoice_number": lambda r: r.choice(["INV-", "FTR-", "RE-", "F"]) + str(r.randint(2020, 2026)) + "-"
    + pick(r, DIGITS, 5),
    "timestamp": lambda r: r.choice([str(1700000000 + r.randrange(10 ** 8)), "2026-0" + str(r.randint(1, 9)) + "-"
                                     + f"{r.randint(10, 28)}T{r.randint(10, 23)}:{r.randint(10, 59)}:00Z"]),
}


def labeled(name: str):
    """The generator and label for a ⦃=name⦄ mark, or None when unknown."""
    if name in IDS:
        return IDS[name], "government_id"
    if name.startswith("iban_") and name[5:] in IBAN_LEN:
        return iban(name[5:]), "account"
    if name == "card":
        return card, "account"
    if name in SECRETS:
        return SECRETS[name], "secret"
    return None
