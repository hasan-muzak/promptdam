# syntax=docker/dockerfile:1
#
# One Promptdam server command (api, gateway, worker or migrate) as a static
# binary on a distroless base (CA certificates and time zones included, no
# shell, non-root). Build from the repository root:
#
#   docker build -f products/promptdam/deploy/Dockerfile --build-arg CMD=gateway \
#     --build-arg VERSION=$(git rev-parse --short HEAD) -t promptdam-gateway .

FROM golang:1.27-alpine AS build
ARG CMD
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY pkg ./pkg
COPY products/promptdam/server ./products/promptdam/server
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    test -n "$CMD" && \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/hasan-muzak/keel/products/promptdam/server/core.Version=${VERSION}" \
      -o /out/app ./products/promptdam/server/cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
