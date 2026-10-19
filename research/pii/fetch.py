"""Downloads the bake-off's benchmarks and candidate models into data/ and models/,
each in its own directory. Weights are fetched as safetensors only: no pickles."""

import sys
import time
from pathlib import Path

import requests
from huggingface_hub import hf_hub_download, snapshot_download

ROOT = Path(__file__).parent

DATASETS = {
    # REDACT (2026-06, ServiceNow): 25 languages, 9 scripts, 51 types. Its terms allow
    # benchmarking and building PII detectors.
    "redact": ("guneeshv/REDACT-PII-Benchmark", ["data/pii_benchmark_sample1000.json", "DATASET_TERMS.md"]),
    # PII-TRACE (2026-08, Perplexity): multi-turn conversations. MIT.
    "piitrace": ("perplexity-ai/PII-TRACE", ["data/train.parquet", "LICENSE"]),
}

MODELS = {
    "openai-privacy-filter": ("openai/privacy-filter", ["*.json", "model.safetensors", "LICENSE*", "README.md"]),
    "pii-tracer": ("perplexity-ai/PII-Tracer", ["*.json", "*.py", "model.safetensors", "LICENSE", "README.md"]),
    "gliner2-pii": ("fastino/gliner2-privacy-filter-PII-multi", None),
    "bardsai-eu-pii": ("bardsai/eu-pii-anonimization-multilang", ["*.json", "model.safetensors", "onnx/*", "README.md"]),
}


# The Hugging Face mirror of REDACT is gated; GitHub serves the same files through LFS.
GITHUB = {"redact": "guneeshvats/REDACT-PII-Benchmark"}


def github(name: str, f: str) -> bool:
    url = f"https://media.githubusercontent.com/media/{GITHUB[name]}/main/{f}"
    if not f.startswith("data/"):
        url = f"https://raw.githubusercontent.com/{GITHUB[name]}/main/{f}"
    dest = ROOT / "data" / name / f
    dest.parent.mkdir(parents=True, exist_ok=True)
    try:
        r = requests.get(url, timeout=120)
        r.raise_for_status()
        dest.write_bytes(r.content)
    except Exception as e:
        print("dataset", name, f, "GitHub FAILED:", e)
        return False
    print("dataset", name, dest, "(GitHub)")
    return True


def main(which: list[str]) -> None:
    for name, (repo, files) in DATASETS.items():
        if which and name not in which:
            continue
        for f in files:
            try:
                p = hf_hub_download(repo, f, repo_type="dataset", local_dir=ROOT / "data" / name)
                print("dataset", name, p)
            except Exception as e:  # gated or moved: try GitHub, else say so and go on
                if name in GITHUB and github(name, f):
                    continue
                print("dataset", name, f, "FAILED:", type(e).__name__, str(e)[:200])
    for name, (repo, patterns) in MODELS.items():
        if which and name not in which:
            continue
        # Big files over a flaky line: connections get reset; each retry resumes.
        for attempt in range(1, 31):
            try:
                p = snapshot_download(repo, allow_patterns=patterns,
                                      ignore_patterns=["*.bin", "*.pt", "*.pth", "*.pkl"],
                                      local_dir=ROOT / "models" / name)
                print("model", name, p, flush=True)
                break
            except Exception as e:
                print("retry", name, attempt, type(e).__name__, str(e)[:120], flush=True)
                time.sleep(min(60, 5 * attempt))
        else:
            print("model", name, "FAILED after 30 attempts", flush=True)


if __name__ == "__main__":
    main(sys.argv[1:])
