"""Exports a model train.py wrote to model.onnx in the same directory, what the detector service reads (engine.Load:
inputs input_ids and attention_mask, int64 [batch, tokens]; output logits [batch, tokens, tags]).

    uv run python train/export.py models/student-pilot

fp32 here. The static int8 export (QDQ, calibrated on the training texts, the head in fp32) comes with the full
set: it is accepted only within a point of fp32 recall and with identical spans across batch mixes."""

import argparse
from pathlib import Path

import numpy as np
import onnxruntime as ort
import torch
from transformers import AutoModelForTokenClassification, AutoTokenizer


class Logits(torch.nn.Module):
    """The classifier with only its logits as output, so the graph has one output named logits."""

    def __init__(self, m):
        super().__init__()
        self.m = m

    def forward(self, input_ids, attention_mask):
        return self.m(input_ids=input_ids, attention_mask=attention_mask).logits


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("dir")
    ap.add_argument("--opset", type=int, default=17)
    args = ap.parse_args()
    d = Path(args.dir)
    tok = AutoTokenizer.from_pretrained(d)
    # Eager attention: SDPA's mask handling traces with the example's shapes and reads other paddings wrong.
    model = AutoModelForTokenClassification.from_pretrained(d, dtype=torch.float32, attn_implementation="eager").eval()
    m = Logits(model).eval()  # the exporter restores the wrapper's mode, recursively: train unless set

    enc = tok(["Ayşe Yılmaz lives at Bağdat Caddesi 12.", "key"], padding=True, return_tensors="pt")
    x = (enc["input_ids"], enc["attention_mask"])
    out = d / "model.onnx"
    torch.onnx.export(m, x, str(out), input_names=["input_ids", "attention_mask"], output_names=["logits"],
                      dynamic_axes={"input_ids": {0: "batch", 1: "tokens"}, "attention_mask": {0: "batch", 1: "tokens"},
                                    "logits": {0: "batch", 1: "tokens"}},
                      opset_version=args.opset, dynamo=False)

    # The export must read as the checkpoint does: the same logits on a text the export never saw, padded.
    probe = tok(["Dr. Okafor's patient, born 3 March 1981, takes metformin.",
                 'export STRIPE_KEY="$STRIPE_KEY"  # set by CI'], padding=True, return_tensors="pt")
    with torch.inference_mode():
        want = m(probe["input_ids"], probe["attention_mask"]).numpy()
    sess = ort.InferenceSession(str(out), providers=["CPUExecutionProvider"])
    got = sess.run(None, {"input_ids": probe["input_ids"].numpy(), "attention_mask": probe["attention_mask"].numpy()})[0]
    mask = probe["attention_mask"].numpy().astype(bool)
    diff = float(np.abs(got - want)[mask].max())
    same = bool((got.argmax(-1) == want.argmax(-1))[mask].all())
    print(f"saved {out} ({out.stat().st_size / 1e6:.0f} MB); max logit difference {diff:.2e}, same tags: {same}")
    if diff > 1e-3 or not same:
        raise SystemExit("the export doesn't read as the checkpoint does")


if __name__ == "__main__":
    main()
