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

## Billing in Stripe test mode

Once by hand before live keys (S5.4), with test keys in `.env` (`STRIPE_SECRET_KEY=sk_test_…`). The tests prove
the same against a fake Stripe (`server/features/resolver/quota_test.go`); this proves Stripe's side.

1. `task promptdam:stripe:sync` creates the products, prices and the `promptdam_requests` meter (again after any
   change to `server/catalog/plans.go`: a changed price needs a new lookup key).
2. Forward Stripe's events to the local API, and put the `whsec_…` it prints in `.env` as `STRIPE_WEBHOOK_SECRET`:

   ```sh
   stripe listen --forward-to localhost:8281/hooks/stripe
   ```

   Then start the API, the worker and the gateway (`task promptdam:dev:api`, `dev:worker`, `dev:gateway`).
3. Buy Pro: `POST /v1/orgs/{org}/billing/checkout` with `{"plan": "pro", "interval": "month"}`, open the URL it
   returns, and pay with card `4242 4242 4242 4242`. `GET /v1/orgs/{org}/billing` says `pro` within seconds.
4. Make calls through the gateway. Within the hour (the worker's `metering-report`), Stripe's test dashboard shows
   the meter's events for the customer, and `GET /v1/orgs/{org}/billing/upcoming` the invoice to come.
5. Set a spend cap of $1 (`PUT /v1/orgs/{org}/billing/spend-caps/requests` with `{"cap_cents": 100}`):
   `GET /v1/orgs/{org}/usage` shows the requests it allows (`ceiling`: 520,000 on Pro), and `DELETE` puts the
   default back. Calls refused at the cap are the tests' (it takes 500,000 calls to reach it here).
6. `go run ./server/cmd/api billing reconcile --stripe` compares what the gateway counted, what was stored and
   reported, and what Stripe holds: it should list nothing.
7. Cancel (`POST /v1/orgs/{org}/billing/cancel`): Pro stays, with `cancel_at_period_end`, until the period ends.
