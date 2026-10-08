# Deploying Promptdam

One image per server command: `api`, `gateway`, `worker` and `migrate`, each a static binary on a distroless base
(no shell, non-root). `Dockerfile` builds any of them; its base images are pinned by digest, with how to bump them
at the top.

## Releasing

Images are built by keel's `promptdam-release` workflow (`keel/.github/workflows/promptdam-release.yml`), at the
commits keel points to, never on a laptop. To release, push Promptdam, point keel at it, and tag keel:

```sh
git tag promptdam-v0.1.0 && git push origin promptdam-v0.1.0
```

The workflow pushes `ghcr.io/hasan-muzak/promptdam-<command>:0.1.0` for the four commands, then for each one:

- signs its digest with **cosign keyless**: the signature's certificate names the workflow and the tag that built
  it (GitHub's OIDC identity), so there is no signing key to keep or leak;
- attaches an **SPDX SBOM** (syft) as a signed attestation;
- verifies both, as the VPS will, so a release that doesn't verify fails in the workflow.

The packages are private: the VPS logs in to GHCR with a token that has `read:packages` only.

## Verifying before the VPS pulls

A tag can be moved and a registry can be written to; a signature from the release workflow can't be made without
it. Resolve the tag to a digest once, verify that digest, and pull and run that digest, never the tag:

```sh
image=ghcr.io/hasan-muzak/promptdam-gateway
identity='^https://github\.com/hasan-muzak/keel/\.github/workflows/promptdam-release\.yml@refs/tags/promptdam-v'
issuer=https://token.actions.githubusercontent.com

digest=$(docker buildx imagetools inspect "$image:0.1.0" --format '{{json .Manifest}}' | jq -r .digest)
cosign verify "$image@$digest" --certificate-identity-regexp "$identity" --certificate-oidc-issuer "$issuer" > /dev/null &&
  docker pull "$image@$digest"
```

The VPS's compose file (it comes with the first VPS, S4.6) names each service's image by that digest
(`ghcr.io/hasan-muzak/promptdam-gateway@sha256:…`), so what runs is what was verified.
`cosign verify` fails, and nothing is pulled, if the image wasn't signed by the release workflow from a
`promptdam-v*` tag of keel: built elsewhere, from a branch, or changed after signing.

The SBOM, verified the same way:

```sh
cosign verify-attestation --type spdxjson "$image@$digest" \
  --certificate-identity-regexp "$identity" --certificate-oidc-issuer "$issuer" \
  | jq -r '.payload' | base64 -d | jq '.predicate'
```

It lists every Go module and OS package in the image, to check against a new advisory without pulling it.

## Building locally

From the keel root, which has the core next to Promptdam:

```sh
docker build -f products/promptdam/promptdam/deploy/Dockerfile --build-arg CMD=gateway \
  --build-arg VERSION=$(git -C products/promptdam/promptdam rev-parse --short HEAD) -t promptdam-gateway .
```

Local images aren't signed and don't verify: they are for trying the image, not for the VPS.
