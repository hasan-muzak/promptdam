"""The bake-off's benchmarks as one shape: documents with gold spans, each span in a group.

Groups are what the firewall would act on; a model's own label doesn't matter for
masking, so scoring ignores it (a name tagged as an address is still masked)."""

import json
from dataclasses import dataclass, field
from pathlib import Path

import pyarrow.parquet as pq

ROOT = Path(__file__).parent / "data"


@dataclass
class Span:
    start: int
    end: int
    group: str
    kind: str = ""  # the benchmark's own type
    tier: str = ""  # REDACT's sensitivity tier


@dataclass
class Doc:
    id: str
    lang: str
    text: str
    gold: list[Span] = field(default_factory=list)
    clean: bool = False  # a control with no personal data at all


# REDACT's 51 types in the groups the firewall would act on. "special" is GDPR
# Article 9 and 10 data (health, beliefs, orientation, union, crime).
REDACT_GROUPS = {
    "name": ["Full_Name", "Last_Family_Name", "First_Given_Name", "Preferred_Name"],
    "contact": ["Work_Email_Address", "Personal_Email_Address", "Telephone_Numbers_Personal",
                "Telephone_Numbers_Work", "Social_Media_Identifiers", "Emergency_Contact_Details"],
    "address": ["Address_Personal", "Address_Work", "City", "State", "Location", "Geolocation_Data",
                "Place_of_Birth", "Country_of_Residence"],
    "id": ["Customer_Reference_Number", "National_Identification_Number", "Employee_ID_Number",
           "Passport_Number", "Driving_License_Number", "Tax_Reference_Number", "Building_Badge_Card_Number",
           "Credit_Card_Numbers", "Account_Statements", "Static_IP_Address"],
    "date": ["Date_Time", "Date_of_Birth", "Age"],
    "special": ["Medical_Information", "Allergy_Information", "Sickness_Day_Records", "Religion",
                "Sex_Orientation", "Political_Party", "Trade_Union_Membership", "Crime"],
    "secret": ["Password"],
}
_REDACT_GROUP = {t: g for g, ts in REDACT_GROUPS.items() for t in ts}

PIITRACE_GROUPS = {
    "private_person": "name", "private_email": "contact", "private_phone": "contact",
    "private_address": "address", "private_url": "contact", "account_number": "id",
    "private_date": "date", "secret": "secret", "other_pii": "other",
}

GROUPS = ["name", "contact", "address", "id", "date", "special", "secret", "other"]


def redact(limit: int = 0) -> list[Doc]:
    """REDACT's locked 1,000-record split, 40 records per language. Only disclosed
    entities count: a hypothetical or negated mention isn't personal data. 89 of the
    24,199 entities have no offsets (alignment failed when the set was built): skipped."""
    rows = json.loads((ROOT / "redact/data/pii_benchmark_sample1000.json").read_text())
    docs = []
    for r in rows:
        gold = [Span(e["start"], e["end"], _REDACT_GROUP.get(e["entity_type"], "other"), e["entity_type"],
                     e.get("sensitivity_tier", ""))
                for e in r["entities"]
                if e.get("disclosed", True) and e.get("start") is not None and e["end"] > e["start"]]
        docs.append(Doc(f"redact-{r['record_id']}", r["axes"]["language"], r["text"], gold))
    return docs[:limit] if limit else docs


def piitrace(limit: int = 0) -> list[Doc]:
    """PII-TRACE's 500 conversations, one document per message: the gateway scans
    each new message as it arrives. Conversations without spans are clean controls."""
    docs = []
    for r in pq.read_table(ROOT / "piitrace/data/train.parquet").to_pylist():
        clean = not r["spans"]
        for t in r["turns"]:
            for role in ("user", "assistant"):
                text = t.get(role) or ""
                if not text:
                    continue
                gold = [Span(s["start"], s["end"], PIITRACE_GROUPS.get(s["label"], "other"), s["label"])
                        for s in r["spans"] if s["turn"] == t["turn"] and s["source"] == role]
                docs.append(Doc(f"{r['id']}/{t['turn']}/{role}", "EN", text, gold, clean))
    return docs[:limit] if limit else docs


SETS = {"redact": redact, "piitrace": piitrace}
