# syntax=docker/dockerfile:1
#
# One Promptdam server command (api, gateway, worker or migrate) as a static
# binary on a distroless base (CA certificates and time zones included, no
# shell, non-root). Build from the keel workspace root, which has the core
# module next to this one:
#
#   docker build -f products/promptdam/promptdam/deploy/Dockerfile --build-arg CMD=gateway \
#     --build-arg VERSION=$(git -C products/promptdam/promptdam rev-parse --short HEAD) -t promptdam-gateway .

# Base images are pinned by digest: a tag can be moved to other content.
# To bump one, look its digest up and replace it (keep the tag for readers):
#
#   docker buildx imagetools inspect golang:1.27.2-alpine
#   docker buildx imagetools inspect gcr.io/distroless/static-debian12:nonroot

# The Go image builds with its own Go (GOTOOLCHAIN=local), not go.mod's
# toolchain line: its tag names the patch release, so a security release is
# a visible bump here.
FROM golang:1.27.2-alpine@sha256:85dc1069ac644ea3c527b177303a406eb3358192816cd7f9e5848eb658851673 AS build
ARG CMD
ARG VERSION=dev
# The workspace's paths, so the product's go.mod finds the core (../../../core).
COPY core/go.mod core/go.sum /src/core/
COPY products/promptdam/promptdam/go.mod products/promptdam/promptdam/go.sum /src/products/promptdam/promptdam/
WORKDIR /src/products/promptdam/promptdam
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY core/pkg /src/core/pkg
COPY products/promptdam/promptdam/server ./server
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    test -n "$CMD" && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/hasan-muzak/promptdam/server/core.Version=${VERSION}" \
      -o /out/app ./server/cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
