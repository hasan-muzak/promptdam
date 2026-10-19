# PII detector bake-off (2026-10-19)

Which small model should Promptdam's firewall use to find personal data that patterns can't: names, addresses,
dates, health and other special categories, in any language? Four 2026 candidates were run on two 2026 benchmarks
on the CPU, next to the firewall's current detectors.

## Setup

- **Benchmarks:**
  - [REDACT](https://github.com/guneeshvats/REDACT-PII-Benchmark) (ServiceNow, June 2026): the locked
    1,000-record split, 40 records in each of 25 languages across 9 scripts; 51 entity types, GDPR special
    categories included. Synthetic; its terms allow benchmarking and building detectors.
  - [PII-TRACE](https://huggingface.co/datasets/perplexity-ai/PII-TRACE) (Perplexity, September 2026, MIT): 500
    multi-turn conversations, 4,500 messages, 50 conversations with no personal data at all. English only in the
    release. Its authors made PII-Tracer, so it has a home advantage there.
- **Candidates:**

  | Model | Size | License | Notes |
  |---|---|---|---|
  | [bardsai/eu-pii-anonimization-multilang](https://huggingface.co/bardsai/eu-pii-anonimization-multilang) | 280M (XLM-R base) | Apache 2.0 | 34 labels with GDPR Article 9 ones; INT8 ONNX shipped |
  | [perplexity-ai/PII-Tracer](https://huggingface.co/perplexity-ai/PII-Tracer) | 600M (Qwen3 encoder) | MIT | 9 labels + a sensitivity head; trained on conversations |
  | [fastino/gliner2-privacy-filter-PII-multi](https://huggingface.co/fastino/gliner2-privacy-filter-PII-multi) | 300M | Apache 2.0 | labels given at run time; run with its 42 and with 7 special categories added |
  | [openai/privacy-filter](https://huggingface.co/openai/privacy-filter) | 1.5B MoE, 50M active | Apache 2.0 | 8 labels, 128k context; decoded with a constrained BIOES Viterbi at its default operating point |
  | patterns | | | the firewall today: core `redact`, every default detector and IPv4 |

- **Scoring** (`score.py`): labels are ignored, since a mask is a mask. A personal-data entity is **caught** when
  masks cover at least half its characters. **Precision** is the share of masks that touch any annotated entity;
  benchmarks don't annotate everything, so it is a lower bound. **Clean flag rate** is the share of messages in
  PII-free conversations that got any mask.
- **Firewall policy:** a firewall shouldn't mask company names, job titles or amounts (answers need them), so the
  headline REDACT table drops those labels from the models and `Org_Name`, `Business_Title`,
  `Compensation_and_Salary` from the gold set (`results/redact-firewall-policy.json`). Raw scores, with every label
  on, are in `results/<model>-<set>.json`.
- **Machine:** M1 Pro, 8 performance cores, PyTorch 2.14 float32 unless noted; one document at a time, as a
  gateway scans one new message. A production x86 server with int8 instructions will differ.

## Results

### REDACT, firewall policy (25 languages)

| Detector | Recall | Precision | Names | Addresses | IDs | Dates | Special | Worst language |
|---|---|---|---|---|---|---|---|---|
| **bardsai + PII-Tracer** | **0.919** | 0.714 | 0.951 | 0.862 | 0.943 | 0.894 | 0.641 | 0.855 (SV) |
| PII-Tracer | 0.885 | 0.654 | 0.940 | 0.664 | 0.907 | 0.885 | 0.578 | 0.811 (SV) |
| bardsai | 0.828 | 0.753 | 0.935 | 0.847 | 0.932 | 0.370 | 0.328 | 0.769 (SV) |
| GLiNER2-PII + special labels | 0.758 | 0.759 | 0.854 | 0.736 | 0.641 | 0.508 | 0.446 | 0.462 (ZH_TW) |
| OpenAI Privacy Filter | 0.720 | 0.676 | 0.803 | 0.509 | 0.659 | 0.764 | 0.063 | 0.516 (HE) |
| patterns (today) | 0.100 | 0.849 | 0.003 | 0.001 | 0.052 | 0.003 | 0.001 | 0.070 (FR) |

Special categories caught by bardsai + PII-Tracer: religion 0.97, sexual orientation 0.84, medical 0.79, allergies
0.74, political party 0.38, sick days 0.37, crime 0.15, trade union 0.14.

### PII-TRACE (conversations, English)

| Detector | Recall | Precision | Clean flag rate | Dates |
|---|---|---|---|---|
| bardsai + PII-Tracer | 0.995 | 0.968 | 6.0% | 0.946 |
| PII-Tracer | 0.971 | 0.950 | 3.6% | 0.914 |
| bardsai | 0.942 | 0.973 | 4.4% | 0.321 |
| OpenAI Privacy Filter | 0.909 | 0.984 | 3.6% | 0.842 |
| GLiNER2-PII | 0.781 | 0.781 | 6.7% | 0.937 |
| patterns (today) | 0.174 | 1.000 | 0% | 0 |

### CPU cost

| Detector | ms per 1,000 chars (8 threads) | p50 per REDACT doc (1,890 chars) | p50 per PII-TRACE message (195 chars) |
|---|---|---|---|
| bardsai, ONNX int8 | 47 | 62 ms | 29 ms on **one** thread |
| bardsai, float32 | 72 | 90 ms | 29 ms |
| GLiNER2-PII | 289 | 330 ms | 107 ms |
| PII-Tracer | 423 | 589 ms | 150 ms |
| OpenAI Privacy Filter (transformers' MoE) | 563 | 1,137 ms | 233 ms |

One core runs bardsai int8 at about 1,400 tokens/s (170 ms per 1,000 characters of new text).

## Findings

1. **Patterns alone catch a tenth of personal data.** The firewall today finds emails, phones and cards; names,
   addresses, dates and health data pass. Patterns add nothing measurable on top of a model (bardsai 0.863 →
   0.865), but they stay: they are exact on secrets and checksummed IDs and cost nothing.
2. **No single model is enough; two complementary ones are.** bardsai is strong on addresses and fast but misses
   dates and special categories; PII-Tracer is the reverse. Together: 0.92 on 25 languages, 0.995 on
   conversations, no language below 0.85. This matches "Mind the Gap" (arXiv 2609.03464): detectors fail on
   different inputs.
3. **The newest names are not the best here.** OpenAI's filter is last among the models on REDACT and close to
   blind to special categories (0.06); GLiNER2 splits text into words and fails on Chinese, Japanese and Korean
   (0.42 to 0.59). Asking GLiNER2 for special categories by name lifted them from 0.04 to 0.45 with no training,
   which is useful for customers' own categories.
4. **Precision is understated, and partly a policy.** Most "false" masks are real data REDACT didn't annotate
   (names, IPs, phones, hashes); the rest are labels a firewall turns off, and some noise (single Chinese
   characters, lone quotes) that a minimum span rule or fine-tuning removes.
5. **Off-the-shelf int8 costs accuracy:** bardsai's shipped int8 file is 35% faster but loses 5 points of recall
   (0.863 → 0.813). Quantize with calibration, or keep sensitive layers in fp16.
6. **CPU cost is the real constraint.** Two models at full precision on every call's new text is about 500 ms
   per 1,000 characters on 8 cores. At 1,000 calls/s with 1,000 new characters each, bardsai int8 alone needs on
   the order of 170 cores; on a GPU an encoder this size batches to hundreds of thousands of tokens per second.

## Recommendation

1. **Build our own student model, trained from these two as teachers.** Label a large multilingual synthetic
   corpus (written by a frontier model, every script, the special categories, customer-support and agent-tool
   text) with the union of bardsai and PII-Tracer plus a frontier model's checks, and fine-tune one small
   multilingual encoder on it (candidates: XLM-R base like bardsai, or a smaller modern encoder such as mmBERT
   small). Target: the pair's 0.92 at bardsai's speed or better, with crime, trade union, politics and sick days
   brought up from 0.14 to 0.38.
2. **Until then, the pair is the reference** to beat, and the fallback if the student falls short.
3. **Keep patterns first** (exact, free), mask only labels the firewall's policy turns on, and add the minimum
   span and stop-character rules the noise above calls for.
4. **Run on ONNX Runtime**, CPU first (low volume, Pro plan, only new messages scanned), with a GPU path when
   volume grows; calibrated int8 measured against this harness before it ships.
5. **Publish the numbers** with this harness, re-run on the student, beside Presidio and the clouds' services
   (not run here: they need accounts).

## Caveats

- Both benchmarks are synthetic. PII-TRACE's release is English only and favors PII-Tracer.
- PII-Tracer was released after REDACT; it can't be ruled out that REDACT-like data was in its training.
- One machine (M1 Pro); the VPS will differ.
- OpenAI's filter ran through transformers' MoE code, not an optimized runtime (its C++/GGML port is reported 7.7×
  faster), so its speed is pessimistic; its accuracy isn't.

## Re-running

```sh
uv sync --python 3.13
HF_HUB_DISABLE_XET=1 uv run python fetch.py          # benchmarks and models, about 7 GB
go build -o .venv/patterns ./patterns                  # from this directory, inside the keel workspace
./queue.sh "bardsai-eu-pii redact" "pii-tracer redact" # one job after another; results/queue.log
uv run python bench.py bardsai-eu-pii redact --union pii-tracer,patterns
```
