#!/usr/bin/env bash
# Fetches the detector service's native libraries into lib/: ONNX Runtime's
# shared library and HF tokenizers' static one, for this machine.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p lib
ORT=1.31.0
TOK=1.27.0
case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) ort=onnxruntime-osx-arm64-$ORT; tok=darwin-arm64; so=libonnxruntime.$ORT.dylib ;;
  Linux-x86_64) ort=onnxruntime-linux-x64-$ORT; tok=linux-amd64; so=libonnxruntime.so.$ORT ;;
  Linux-aarch64) ort=onnxruntime-linux-aarch64-$ORT; tok=linux-arm64; so=libonnxruntime.so.$ORT ;;
  *) echo "lib.sh: no prebuilt libraries for $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL "https://github.com/microsoft/onnxruntime/releases/download/v$ORT/$ort.tgz" | tar xz -C "$tmp"
cp "$tmp/$ort/lib/$so" lib/
curl -fsSL "https://github.com/daulet/tokenizers/releases/download/v$TOK/libtokenizers.$tok.tar.gz" | tar xz -C lib
echo "lib/: $(ls lib | tr '\n' ' ')"
echo "export DETECTOR_ORT_LIB=$PWD/lib/$so"
