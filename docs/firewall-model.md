# The firewall's model

Patterns find what has a shape: keys, cards, IBANs, national ID numbers with their checksums. They miss what a
person writes: "I'm Ayşe Yılmaz, I have type 2 diabetes, I live at Bağdat Caddesi 12". The bake-off
([research/pii](../research/pii/README.md)) measured it: patterns catch 10% of the personal data in 25 languages. The
detector model reads the text for the rest, and for secrets in formats no pattern lists.

## How a call is read

1. **Patterns first**, over every string of the request, as before.
2. **The model**, over the strings that are text: messages and tool results, never tool definitions, metadata,
   identifiers or thinking (signed by the provider: it only gets the tokens found elsewhere). Long strings are read
   in pieces of at most 2 KiB, cut where only what comes before decides, so a conversation's history keeps its
   pieces from call to call.
3. **The scan cache:** each piece is remembered by a keyed hash of the project, the model's version and its text,
   in the gateway's memory and in Valkey for every replica, for a day. Only spans are kept, never text. A
   conversation's history is read once.
4. **Merge:** a pattern wins a place both found (it is exact). A secret only the model found is dropped when it is
   code reading a secret (`os.Getenv(...)`, `cfg.Token`) or a placeholder (`your-api-key-here`), and blocks only when
   the model is at least 0.9 sure; below, it alerts.
5. **Consistency:** a value the model found once is masked wherever else the request holds it (a first name said
   again, a name in a tool result or in thinking), in one read (Aho-Corasick), at word boundaries in scripts with
   spaces. Nothing is kept between calls: the next request holds the history again.
6. **Actions** per label: tokenize, redact, block, alert or off.

## When a call waits

- A call **waits** for the model when a label it reads masks or blocks: what leaves must be decided before. At most
  32 KiB of text not read before per call, the newest first; the rest is reported as `partial` and not masked.
- When every label only **alerts**, the call goes at once; the model reads it afterwards on a bounded queue, and its
  findings join the call's row and events (dropped and counted when the queue is full).
- When the model fails or is too slow (the wait grows with the size asked: the p99 per KiB of its latest answers,
  200 ms to 3 s), the patterns' findings apply alone (`model.fail_mode: open`, the default), or the call is refused
  with `scan:model` (`closed`). A service failing 5 calls in a row is left alone for 5 s.

## Settings

`GET/PUT …/firewall`:

- `model.secrets` (every plan): find keys, tokens and passwords no pattern knows (`credential`, with the secrets'
  action).
- `model.pii` (Pro): personal data and special categories.
- `sensitive`: the special categories' action (health, religion, sexual orientation, political opinion, trade
  union, criminal record, ethnicity, biometric and genetic data), alert by default.
- `model.labels`: an action per label over its class's. `personal_date`, `location` and `ipv4` are off unless
  chosen: answers usually need dates and places.
- `model.allow`: terms never masked (the company's name, products, a support agent's name), in any case.
- `model.fail_mode`: `open` or `closed`.

`GET /v1/firewall/presets` lists the labels in the dashboard's groups and five use cases (customer support,
healthcare, HR and finance, coding agents, strict), each the actions it sets.

## Tokens

Tokens are named by a keyed hash of the conversation's first message, the detector and the value
(`⟦person_k3x9q2mb⟧`, the key derived from the vault): the same in every call of a conversation and on every
replica, so the history the client sends back is tokenized to the bytes the provider already cached (and signed, in
thinking blocks); different in another conversation; nothing a provider can compute. Restoring covers tool calls'
arguments too, escaped marks included, parallel calls apart, and text held at a block's end.

## The service

`detector/` is its own Go module (cgo: ONNX Runtime and HF tokenizers), so the gateway's static build is unchanged.

- `POST /v1/spans {priority, deadline_ms, texts}` answers each text's spans (UTF-8 byte offsets, a firewall label, a
  score) and the model's version. Every label at least 0.3 sure is returned; projects choose what to act on, so a
  setting changed never reads a text again.
- Texts are read in windows of 512 tokens overlapping by 128; windows from every request are batched (waiting calls
  first, 16 a batch or 3 ms), each padded to a fixed bucket so a text reads the same whatever shares its batch.
  Too much queued is refused at once (503) so the gateway falls back without waiting.
- A model directory: `model.onnx`, `tokenizer.json`, `config.json`, `labels.json` (the model's labels mapped to the
  firewall's). v0 serves bardsai's model; the model trained in round PD replaces it when it passes its exam.
- Run it here: `task detector:lib`, then `task detector:run` (DETECTOR_MODEL_DIR); the gateway reads it with
  `PROMPTDAM_DETECTOR_URL=http://127.0.0.1:8484` and `PROMPTDAM_DETECTOR_TOKEN`. Tests: `task detector:test`.
- Image: `deploy/detector.Dockerfile`, built, signed and attested by keel's `promptdam-release` workflow.

## Measured (M1 Pro, v0, fp32)

- Two short texts through the gateway's client and the service: 126 ms on first read, 6 µs once cached.
- The national ID detectors: prose with no ID-shaped text costs ~0.1 ms per 6 KiB; number-dense text reads at
  ~3 MB/s with all of them on (only new text is read, per call).
- `Inspect` with personal data on (patterns, no model): 162 µs for 6.4 KiB.

## Limits

- The model's quality is the bake-off's until our own model replaces v0: dates and special categories are weak, and
  bardsai tags street addresses as locations.
- Images and files aren't read.
- Text past the 32 KiB a call may wait for isn't masked on that call (`partial`); the next call reads it from the
  cache if it is still in the history.
