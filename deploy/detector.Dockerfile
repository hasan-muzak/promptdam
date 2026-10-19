# syntax=docker/dockerfile:1
#
# The detector service (the firewall's model) with ONNX Runtime, on a
# distroless base with a C library (cgo), no shell, non-root. The model is
# baked in: one image is one model version, so every replica reads alike.
# Build from the keel workspace root, with the model directory at
# products/promptdam/promptdam/detector/models/current (model.onnx,
# tokenizer.json, config.json, labels.json; not in git):
#
#   docker build -f products/promptdam/promptdam/deploy/detector.Dockerfile -t promptdam-detector .
#
# Base images are pinned by digest, the libraries by checksum. To bump one:
#
#   docker buildx imagetools inspect golang:1.27.2-bookworm
#   docker buildx imagetools inspect gcr.io/distroless/cc-debian12:nonroot
#   sha256sum of the release archives (onnxruntime, daulet/tokenizers)

FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
ARG ORT_VERSION=1.31.0
ARG ORT_SHA256=cc5c72baf5ae5c8238a6841f0897227be2d02826b9cf98eaf02fdefaeeb03a57
ARG TOKENIZERS_VERSION=1.27.0
ARG TOKENIZERS_SHA256=72556cdca798dd4ea7cdaba308e5f0d68a8cb93b67c96edf485b7a0edd7b07f4
WORKDIR /deps
RUN curl -fsSLo ort.tgz https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/onnxruntime-linux-x64-${ORT_VERSION}.tgz && \
    echo "${ORT_SHA256}  ort.tgz" | sha256sum -c - && tar xzf ort.tgz && \
    cp onnxruntime-linux-x64-${ORT_VERSION}/lib/libonnxruntime.so.${ORT_VERSION} /deps/libonnxruntime.so && \
    curl -fsSLo tok.tgz https://github.com/daulet/tokenizers/releases/download/v${TOKENIZERS_VERSION}/libtokenizers.linux-amd64.tar.gz && \
    echo "${TOKENIZERS_SHA256}  tok.tgz" | sha256sum -c - && tar xzf tok.tgz
# The workspace's paths: the service's module replaces promptdam (../) and
# the core (../../../../core).
COPY core/go.mod core/go.sum /src/core/
COPY products/promptdam/promptdam/go.mod products/promptdam/promptdam/go.sum /src/products/promptdam/promptdam/
COPY products/promptdam/promptdam/detector/go.mod products/promptdam/promptdam/detector/go.sum /src/products/promptdam/promptdam/detector/
WORKDIR /src/products/promptdam/promptdam/detector
RUN --mount=type=cache,target=/go/pkg/mod GOWORK=off go mod download
COPY products/promptdam/promptdam/server/features/detector/wire /src/products/promptdam/promptdam/server/features/detector/wire
COPY products/promptdam/promptdam/detector /src/products/promptdam/promptdam/detector
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOWORK=off CGO_ENABLED=1 CGO_LDFLAGS="-L/deps" go build -trimpath -ldflags "-s -w" -o /out/detector ./cmd/detector

FROM gcr.io/distroless/cc-debian12:nonroot@sha256:9dac0a79194e45a7da0158a9c6da57b217585af0786db3845d1f0ec1a0dd182f
COPY --from=build /out/detector /detector
COPY --from=build /deps/libonnxruntime.so /usr/lib/libonnxruntime.so
COPY products/promptdam/promptdam/detector/models/current /model
ENV DETECTOR_MODEL_DIR=/model DETECTOR_ORT_LIB=/usr/lib/libonnxruntime.so DETECTOR_ADDR=:8484
USER nonroot:nonroot
EXPOSE 8484
ENTRYPOINT ["/detector"]
