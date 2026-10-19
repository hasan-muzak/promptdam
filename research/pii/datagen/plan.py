"""Writes the agents' assignments: one file per language (and per code set), one line per text to write.

    uv run python datagen/plan.py pilot     # ~100 texts per language, ~600 code and agent texts
    uv run python datagen/plan.py holdout   # ~20 per language, ~200 code: separate seed, never trained on
    uv run python datagen/plan.py full      # the full set, weighted to the bake-off's weak spots

Each line names what the text is (domain, format, length) and what it must contain, so coverage is decided here
and not left to chance. Lines are balanced per language by dealing every list out in a seeded shuffle."""

import random
import sys
from pathlib import Path

ROOT = Path(__file__).parent.parent / "data" / "plan"

# Language code -> (name as the agent should read it, the ID and IBAN generators of its countries).
LANGS = {
    "ar": ("Arabic (Saudi Arabia, Egypt, UAE)", ["ae_eid", "iban_sa", "iban_ae"]),
    "bg": ("Bulgarian", ["iban_bg"]),
    "bn": ("Bengali (Bangladesh, West Bengal)", ["aadhaar", "pan_in"]),
    "cs": ("Czech", ["iban_cz"]),
    "da": ("Danish", ["dk_cpr", "iban_dk"]),
    "de": ("German (Germany, Austria, Switzerland)", ["de_steuer_id", "iban_de", "iban_at", "iban_ch"]),
    "el": ("Greek", ["iban_gr"]),
    "en": ("English (US, UK, Ireland, Canada, Australia, India, Nigeria)",
           ["us_ssn", "us_itin", "uk_nino", "nhs", "ca_sin", "au_tfn", "au_medicare", "iban_gb", "iban_ie"]),
    "es": ("Spanish (Spain, Mexico, Argentina, Colombia)", ["es_dni", "es_nie", "mx_curp", "mx_rfc", "iban_es"]),
    "et": ("Estonian", ["iban_ee"]),
    "fa": ("Persian (Iran)", ["iban_ir"]),
    "fi": ("Finnish", ["fi_hetu", "iban_fi"]),
    "fr": ("French (France, Belgium, Switzerland)", ["fr_nir", "be_nn", "iban_fr", "iban_be"]),
    "fr-ca": ("Québec French", ["ca_sin"]),
    "he": ("Hebrew (Israel)", ["il_id", "iban_il"]),
    "hi": ("Hindi (India)", ["aadhaar", "pan_in"]),
    "hr": ("Croatian", ["iban_hr"]),
    "hu": ("Hungarian", ["iban_hu"]),
    "id": ("Indonesian", []),
    "it": ("Italian", ["it_cf", "iban_it"]),
    "ja": ("Japanese", ["my_number"]),
    "ko": ("Korean", ["kr_rrn"]),
    "lt": ("Lithuanian", ["iban_lt"]),
    "lv": ("Latvian", ["iban_lv"]),
    "ms": ("Malay (Malaysia, Singapore)", ["sg_nric"]),
    "nl": ("Dutch (Netherlands, Flanders)", ["nl_bsn", "be_nn", "iban_nl", "iban_be"]),
    "no": ("Norwegian", ["no_fnr", "iban_no"]),
    "pl": ("Polish", ["pl_pesel", "iban_pl"]),
    "pt-br": ("Brazilian Portuguese", ["cpf", "cnpj"]),
    "pt-pt": ("European Portuguese", ["pt_nif", "iban_pt"]),
    "ro": ("Romanian", ["iban_ro"]),
    "ru": ("Russian", []),
    "sk": ("Slovak", ["iban_sk"]),
    "sl": ("Slovenian", ["iban_si"]),
    "sr": ("Serbian (Cyrillic and Latin)", ["iban_rs"]),
    "sv": ("Swedish", ["se_pnr", "iban_se"]),
    "sw": ("Swahili (Kenya, Tanzania)", []),
    "ta": ("Tamil (India, Sri Lanka)", ["aadhaar", "pan_in"]),
    "th": ("Thai", []),
    "tl": ("Tagalog / Filipino", []),
    "tr": ("Turkish", ["tckn", "iban_tr"]),
    "uk": ("Ukrainian", ["iban_ua"]),
    "ur": ("Urdu (Pakistan)", []),
    "vi": ("Vietnamese", []),
    "zh-cn": ("Simplified Chinese (mainland China)", ["cn_id"]),
    "zh-tw": ("Traditional Chinese (Taiwan, Hong Kong)", []),
    "af": ("South African English and Afrikaans", ["za_id"]),
}

DOMAINS = [
    "online shop customer support", "invoices and billing", "shipping and delivery", "banking", "insurance claim",
    "hospital or clinic", "pharmacy", "HR and recruiting", "payroll", "school or university", "legal case",
    "real estate and rentals", "travel booking", "telecom support", "government services", "utility company",
    "car repair or rental", "restaurant or hotel booking", "social services", "IT helpdesk",
]

FORMATS = [
    ("a chat between a customer and a support agent", 6),
    ("a chat between a user and an AI assistant", 6),
    ("an email", 4),
    ("a form or record as plain text (field: value lines)", 3),
    ("a tool result an AI agent received: JSON", 3),
    ("a tool result an AI agent received: CSV or SQL rows", 2),
    ("meeting notes or a case note", 3),
    ("an application log or support ticket history", 2),
    ("a letter or official document", 2),
    ("an agent conversation: the user asks, the assistant calls a tool, the tool answers, the assistant replies", 3),
    ("a short message (SMS or chat app)", 2),
    ("source code or config with comments and strings in this language", 1),
]

LENGTHS = [("short", 3), ("medium", 5), ("long", 2)]

PERSONAL = ["person", "person", "person", "email", "phone", "address", "location", "date_of_birth", "date",
            "government_id", "account", "username", "ip", "url_personal"]
SPECIAL = ["health", "health", "religion", "sexual_orientation", "political_opinion", "trade_union",
           "criminal_record", "ethnicity", "biometric_genetic"]

TWISTS = [
    "a given name later repeated alone", "a surname that is also a common word", "a name that is also a place",
    "a detail said casually, in passing", "a hypothetical or negated mention that must stay unmarked",
    "an organization and an amount next to personal data", "a business date (delivery or due date) beside a "
    "date of birth", "two different people", "a nickname or initials", "an order or tracking number that is not "
    "personal data (use the ~ generators)", "an address split over several lines", "a value inside quotes or "
    "brackets", "a public figure mentioned in passing", "a typo or informal spelling", "mixed with a few English "
    "words (code-switching)",
]

CODE_LANGS = ["Python", "TypeScript", "JavaScript (Node)", "Go", "Java", "C#", "Ruby", "PHP", "Rust", "Kotlin",
              "Swift", "Bash", "YAML (Kubernetes, CI)", "Terraform", "Dockerfile and docker-compose", ".env file",
              "JSON config", "SQL migration", "Jupyter notebook cell"]
CODE_KINDS = [
    "a source file", "a config file", "a git diff an agent is reviewing", "a stack trace or error log",
    "a curl command and its output", "shell history", "a test file with fixtures", "a README with setup steps",
    "an agent's tool result: the contents of a file it read", "an agent's tool result: command output",
]
SECRETS = ["stripe_secret", "stripe_restricted", "aws_access_key", "aws_secret", "gcp_api_key", "github_token",
           "github_pat", "gitlab_token", "slack_token", "slack_webhook", "openai_key", "anthropic_key",
           "huggingface_token", "npm_token", "twilio_auth", "sendgrid_key", "mailgun_key", "shopify_token",
           "discord_token", "telegram_token", "datadog_key", "azure_key", "jwt", "private_key", "password",
           "password", "generic_token", "generic_token", "basic_auth", "bearer"]
DECOYS = ["uuid", "sha1", "sha256", "git_sha", "stripe_publishable", "example_aws_key", "placeholder_key",
          "public_key", "base64_blob", "timestamp"]
CODE_COMMENT_LANGS = ["English"] * 6 + ["Turkish", "German", "Spanish", "Portuguese", "French", "Japanese",
                                        "Chinese", "Russian", "Hindi", "Korean"]


class Dealer:
    """Deals a list out in shuffled rounds, so each item comes up evenly."""

    def __init__(self, items, rng):
        self.items, self.rng, self.queue = list(items), rng, []

    def __call__(self):
        if not self.queue:
            self.queue = self.items[:]
            self.rng.shuffle(self.queue)
        return self.queue.pop()


def weighted(pairs):
    return [x for x, w in pairs for _ in range(w)]


def text_lines(lang: str, n: int, rng: random.Random, prefix: str) -> list[str]:
    name, ids = LANGS[lang]
    domain, fmt, length = Dealer(DOMAINS, rng), Dealer(weighted(FORMATS), rng), Dealer(weighted(LENGTHS), rng)
    personal, special, twist = Dealer(PERSONAL, rng), Dealer(SPECIAL, rng), Dealer(TWISTS, rng)
    idgen = Dealer([g for g in ids if not g.startswith("iban_")] or ["passport"], rng)
    out = []
    for i in range(1, n + 1):
        f, ln = fmt(), length()
        if i % 12 == 0:
            want = "nothing personal at all (organizations, amounts, order numbers, business dates only)"
        else:
            labels = {personal() for _ in range(rng.choice([1, 2, 3]))}
            if rng.random() < 0.45:
                labels.add(special())
            if "government_id" in labels:
                labels.discard("government_id")
                labels.add(f"government_id via ={idgen()}")
            if "account" in labels:
                labels.discard("account")
                ibans = [g for g in ids if g.startswith("iban_")]
                labels.add(f"account via ={rng.choice(ibans + ['card'])}")
            if rng.random() < 0.15:
                labels.add(f"secret via ={rng.choice(['password', 'generic_token', 'bearer'])}")
            want = ", ".join(sorted(labels))
        out.append(f"{prefix}{i:04d} | {domain()} | {f} | {ln} | contains: {want} | twist: {twist()}")
    return out


def code_lines(n: int, rng: random.Random, prefix: str) -> list[str]:
    lang, kind, secret = Dealer(CODE_LANGS, rng), Dealer(CODE_KINDS, rng), Dealer(SECRETS, rng)
    decoy, comments, length = Dealer(DECOYS, rng), Dealer(CODE_COMMENT_LANGS, rng), Dealer(weighted(LENGTHS), rng)
    out = []
    for i in range(1, n + 1):
        parts = []
        if i % 5 != 0:
            parts.append(", ".join(sorted({f"={secret()}" for _ in range(rng.choice([1, 1, 2, 3]))})))
        else:
            parts.append("no secret at all (code that reads secrets from the environment, placeholders)")
        parts.append("decoys: " + ", ".join(sorted({f"~{decoy()}" for _ in range(rng.choice([1, 2]))})))
        if rng.random() < 0.3:
            parts.append("personal data in fixtures or logs: person, email")
        out.append(f"{prefix}{i:04d} | {lang()} | {kind()} | comments and strings in {comments()} | {length()} | "
                   f"contains: {'; '.join(parts)}")
    return out


def main(kind: str) -> None:
    sizes = {"pilot": (100, 100, 6), "holdout": (20, 100, 2), "full": (1200, 400, 30)}
    per_lang, per_code, code_files = sizes[kind]
    seed = {"pilot": 1, "holdout": 7, "full": 3}[kind]
    out = ROOT / kind
    out.mkdir(parents=True, exist_ok=True)
    for lang in LANGS:
        rng = random.Random(f"{seed}-{lang}")
        tag = "h" if kind == "holdout" else ""
        lines = text_lines(lang, per_lang, rng, f"{lang}-{tag}")
        (out / f"{lang}.txt").write_text(f"# {LANGS[lang][0]}: {len(lines)} texts\n" + "\n".join(lines) + "\n")
    for k in range(1, code_files + 1):
        rng = random.Random(f"{seed}-code-{k}")
        tag = "h" if kind == "holdout" else ""
        lines = code_lines(per_code, rng, f"code{k}-{tag}")
        (out / f"code{k}.txt").write_text(f"# Code and agent formats: {len(lines)} texts\n" + "\n".join(lines) + "\n")
    print(f"{kind}: {len(LANGS)} languages × {per_lang}, {code_files} code sets × {per_code} -> {out}")


if __name__ == "__main__":
    main(sys.argv[1] if len(sys.argv) > 1 else "pilot")
