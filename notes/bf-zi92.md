# bf-zi92 — Discover twitterapi-proxy-svc deployment configuration

**Date:** 2026-07-31
**Deliverable:** [`docs/research/twitterapi-proxy-deployment.md`](../docs/research/twitterapi-proxy-deployment.md)

## What was done

Read the live deployment on ardenone-cluster through the read-only kubectl proxy
(`http://traefik-ardenone-cluster:8001`), read the deployed source revision
(`origin/main` @ VERSION 0.1.8 in `/home/coding/twitterapi-proxy`), and probed the
running service over the tailnet to verify behaviour rather than infer it.

## Acceptance criteria

- **Service endpoints documented** — Service/Deployment/Pod/IngressRoute/PVC/Secrets
  in namespace `twitterapi-proxy-svc`, plus the full traffic path from the tailnet
  `vpn` entrypoint (:8444) through Traefik to the pod on :3000. §2, §3 of the doc.
- **Request/response format understood** — one local route (`GET /health` → empty 200),
  everything else is a verbatim pass-through to `api.twitterapi.io`; `x-api-key`
  injected server-side, caller `x-api-key`/`authorization` stripped; `x-cache-status:
  hit|miss` is the only header the proxy adds; upstream envelope `{status, msg, data}`.
  Cache-key and TTL semantics documented because they affect capture reproducibility. §4, §5.
- **Access method for capture identified** — `curl --resolve
  twitterapi-proxy.ardenone.com:8444:100.71.31.73 https://…:8444/<path>`, TLS valid,
  no client credentials. kubectl `services/proxy` is forbidden for `devpod-observer`,
  so the tailnet ingress is the route. §6.

## Live verification performed

`/health` → 200 with correct Host, 404 with any other Host (confirms the IngressRoute
match); `GET /twitter/user/info?userName=elonmusk` → 200 `x-cache-status: miss`, repeat
→ `hit`, reordered query → `hit` (confirms alphabetical query normalisation in the
cache key), bogus caller `x-api-key` → unchanged (confirms header stripping). One
billed upstream call was made; everything after it was served from cache.

## Finding: the existing corpus is wrong

`corpus/twitterapi-proxy/corpus.json` (from `bf-kki8`) was written against an assumed
Twitter API v2 shape — `/2/*` paths, `Authorization: Bearer`, `{data,includes,meta}`
envelope, `X-RateLimit-*` headers — none of which this service uses. It cannot be used
for differential comparison as-is. Filed **`bf-1x3w`** (P1) to re-capture against the
live service; the corpus file itself was left untouched, as rewriting it is that bead's
work, not this discovery bead's.

Also worth noting for anyone reading the proxy source: the local worktree `main` is at
0.1.3 and is **behind** what is deployed. `/health`, TLS to upstream, and the front-page
TTL only exist on `origin/main` (0.1.8). Read the deployed revision explicitly.
