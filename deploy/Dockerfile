# syntax=docker/dockerfile:1
#
# One Promptdam server command (api, gateway, worker or migrate) as a static
# binary on a distroless base (CA certificates and time zones included, no
# shell, non-root). Build from the keel workspace root, which has the core
# module next to this one:
#
#   docker build -f products/promptdam/promptdam/deploy/Dockerfile --build-arg CMD=gateway \
#     --build-arg VERSION=$(git -C products/promptdam/promptdam rev-parse --short HEAD) -t promptdam-gateway .

FROM golang:1.27-alpine AS build
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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
