# twitterapi-proxy-svc — incumbent deployment on ardenone-cluster

Discovery for bead `bf-zi92`. All facts below were read from the **live cluster**
(via the read-only kubectl proxy at `http://traefik-ardenone-cluster:8001`) and from
the deployed source revision, on **2026-07-31**. Live probes against the running
service are marked ✅ *verified live*.

This is the incumbent service SEAM must reproduce for the `twitterapi-proxy` route
fragment, so the differential corpus has to match *this* behaviour, not the Twitter
API v2 shape assumed earlier (see [Corpus discrepancy](#corpus-discrepancy)).

---

## 1. What it actually is

A **wholly generic pass-through caching reverse proxy for `api.twitterapi.io`** —
*not* a Twitter API v2 gateway. It has no knowledge of Twitter semantics: it takes
whatever path the caller sends, injects the API key server-side, forwards it, caches
2xx responses in SQLite so a repeated request is never billed twice, and returns the
upstream response verbatim.

| | |
|---|---|
| Image | `ronaldraygun/twitterapi-proxy:0.1.8` (digest `sha256:39cc4bd1ac1587803a3d39fb28bb4d4121aff2ef4625f602604d7ea5b4a27cf1`) |
| Language / framework | Rust, `axum` + `hyper-util` client, `sqlx`/SQLite cache |
| Source | `/home/coding/twitterapi-proxy` — deployed rev is **`origin/main`** (`VERSION` = 0.1.8). Local `main` is behind at 0.1.3 and lacks `/health`, TLS upstream, and the front-page TTL. Read `git show origin/main:src/main.rs`, not the worktree. |
| Upstream | `https://api.twitterapi.io` (hardcoded, `src/main.rs`) |

## 2. Kubernetes objects (namespace `twitterapi-proxy-svc`)

**Deployment `twitterapi-proxy`** — 1 replica, strategy `Recreate` (single writer to
the SQLite file), status Available, 0 restarts since 2026-07-27T20:30:56Z.

- Container `proxy`, port `3000` (name `http`), `imagePullPolicy: Always`
- Env: `TWITTERAPI_KEY` ← `secretKeyRef{name: twitterapi-proxy-secrets, key: TWITTERAPI_KEY}`; `RUST_LOG=info`; `DATA_DIR=/data`
- Resources: requests 100m / 128Mi, limits 500m / 512Mi
- Probes: liveness + readiness `GET /health` on 3000 (liveness 10s delay / 30s period, readiness 5s / 10s, both 3s timeout, 3 failures)
- Security context: all capabilities dropped, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: false`
- Volume: PVC `twitterapi-proxy-data` (10Gi, Bound) → `/data`

**Service `twitterapi-proxy`** — `ClusterIP 10.43.99.36`, port **8080 → targetPort 3000**,
selector `app=twitterapi-proxy`. In-cluster address:
`http://twitterapi-proxy.twitterapi-proxy-svc.svc.cluster.local:8080`.

**IngressRoute `twitterapi-proxy-vpn`** (`traefik.io/v1alpha1`) — entryPoint **`vpn`**,
rule ``Host(`twitterapi-proxy.ardenone.com`)``, → service `twitterapi-proxy:8080`,
TLS from secret `twitterapi-proxy-vpn-tls`.

**Pod** `twitterapi-proxy-7cbfb5c67b-k69d9` on node `k3s-agent-minisforum`, IP `10.42.6.197`.

**Secrets** in-namespace: `twitterapi-proxy-secrets` (key `TWITTERAPI_KEY`) and
`twitterapi-proxy-vpn-tls` (TLS cert/key).

## 3. Traffic flow

```
caller (tailnet peer)
  → DNS twitterapi-proxy.ardenone.com  (public DNS, but the IP is only routable
                                        over the Tailscale WireGuard mesh)
  → Traefik on ardenone-cluster, entryPoint `vpn` = :8444/tcp, TLS terminated
     (--entrypoints.vpn.http.tls=true; tailnet device traefik-ardenone-cluster
      = 100.71.31.73, exposed via `tailscale.com/expose: true`)
  → IngressRoute twitterapi-proxy-vpn  (Host match)
  → Service twitterapi-proxy :8080
  → Pod :3000  — axum router:  GET /health → 200 (local);  everything else → fallback proxy_handler
  → cache lookup (SQLite /data/cache.db)   ── hit  → replay, x-cache-status: hit, no upstream call, no credit spent
                                            └─ miss → https://api.twitterapi.io<path>?<query>
```

**Reachability is tailnet-only, enforced at the Tailscale device level, not by a
NetworkPolicy.** The `vpn` (8444) and `kubectl-tcp` (8001) entrypoints are the only
ports forwarded by the exposed tailnet device; the public `websecure` (8443)
Cloudflare-tunnel entrypoint has no route for this host. Off-tailnet clients get no
route at all. ✅ *Verified live*: `Host: twitterapi-proxy.ardenone.com` on
`100.71.31.73:8444` → 200; any other Host on the same port → 404.

## 4. Authentication and rate limiting

**Caller → proxy: no authentication whatsoever.** There is no bearer check, no API
key check, no Traefik auth middleware on the IngressRoute. Authorization is purely
network-level (tailnet membership). ✅ *Verified live*: a request with no credentials
at all returns 200.

**Proxy → upstream: `x-api-key` header**, injected server-side from `TWITTERAPI_KEY`.
The proxy **strips `x-api-key` and `authorization` from the inbound request**
(`FORBIDDEN_FORWARD_HEADERS`) before adding its own, so a caller can neither leak nor
override the key. ✅ *Verified live*: sending a bogus `x-api-key` changes nothing.

> ⚠️ This is **`x-api-key`, not `Authorization: Bearer`** — the corpus recorded by
> `bf-kki8` has this wrong. See §7.

**Rate limiting: none in the proxy or at Traefik.** The real constraint is upstream:
twitterapi.io is **credit-metered per call** (e.g. ~18 credits for `/twitter/user/info`,
100 credits for `check_follow_relationship`, `$0.002–0.003` per write op). The cache is
the cost-control mechanism — the whole point is that a repeated request never bills twice.
For SEAM this maps to `x-cost-per-call` / `x-quota` on the route fragment.

## 5. Endpoints and request/response format

**The proxy defines exactly one endpoint of its own:**

| Method | Path | Behaviour |
|---|---|---|
| `GET` | `/health` | Returns bare `200 OK`, empty body, no upstream call. Deliberately routed before the fallback so probes don't burn an API credit. |
| *any* | *any other path* | Fallback `proxy_handler` — verbatim pass-through to `https://api.twitterapi.io<path>?<query>` |

Everything else is therefore **the twitterapi.io API surface**, unmodified:
`/twitter/user/info`, `/twitter/user/last_tweets`, `/twitter/user/followers`,
`/twitter/tweets`, `/twitter/tweet/advanced_search`, `/twitter/tweet/replies`,
`/twitter/list/tweets_timeline`, `/twitter/community/*`, the `*_v2` write ops, etc.
Canonical list: <https://docs.twitterapi.io/llms.txt>.

### Request

- Method, path, query and body are forwarded unchanged.
- All caller headers are forwarded **except** `x-api-key` and `authorization`, which are dropped and replaced.
- Control header `X-Cache-Bypass: 1` — forces a cache miss and a fresh (billed) upstream call. Any other value is ignored.

### Response

- Status and body are the upstream's, byte-for-byte. Hop-by-hop headers (`connection`,
  `keep-alive`, `proxy-authenticate`, `proxy-authorization`, `te`, `trailers`,
  `transfer-encoding`, `upgrade`) are stripped; all other upstream headers pass through.
- The proxy adds exactly one header: **`x-cache-status: hit | miss`**.

✅ *Verified live* — `GET /twitter/user/info?userName=elonmusk`:

```http
HTTP/2 200
content-type: application/json
x-cache-status: miss          # `hit` on the repeat call
x-trace-id: 934dea67-…        # upstream (twitterapi.io)
cf-ray: …, server: cloudflare, alt-svc, nel, report-to   # upstream Cloudflare edge
```
```json
{ "status": "success", "msg": "success",
  "data": { "id": "…", "userName": "…", "followers": 0, "createdAt": "…", "pinnedTweetIds": [], … } }
```

Upstream envelope is `{status, msg, data}` — **not** the Twitter v2 `{data, includes, meta, errors}` envelope.

**Volatile headers a corpus must ignore:** `date`, `cf-ray`, `cf-cache-status`,
`report-to`, `nel`, `alt-svc`, `x-trace-id`, `content-length`, and `x-cache-status`
itself (it flips hit/miss depending on cache state, which is exactly the kind of
non-determinism differential comparison must not trip over).

### Caching semantics (matters for reproducible capture)

- Cache key = `SHA256(method ‖ path ‖ normalized_query ‖ body)`, where `normalized_query`
  sorts params alphabetically — so **query-param order does not affect cache hits**.
  ✅ *Verified live*: a reordered query still returned `x-cache-status: hit`.
- **Only 2xx responses are cached.** Errors always re-hit upstream (and are billed).
- Entries are otherwise **cached forever**, with one exception: a "front page" request —
  one whose `cursor` param is absent or empty — expires after `FRONT_PAGE_TTL_SECS = 300`.
  That TTL exists because cache-forever caused a real 9-day silent-staleness incident
  for the incremental archive puller (see the deployed `docs/plan/plan.md`, ADR-002).
- Cache DB `/data/cache.db` on the 10Gi PVC, with a journal used to rebuild after a
  failed integrity check. B2 backup is supported but **not configured here** — startup
  logs `B2 backup configuration not provided - running without backup`.

## 6. Access method for capture

From this devpod (a tailnet peer), no in-cluster networking and no port-forward needed.
The kubectl-proxy `services/proxy` path is **forbidden** for the
`devpod-observer` service account, so use the tailnet ingress directly:

```bash
# MagicDNS name of the ardenone-cluster Traefik tailnet device = 100.71.31.73
curl --resolve twitterapi-proxy.ardenone.com:8444:100.71.31.73 \
     "https://twitterapi-proxy.ardenone.com:8444/twitter/user/info?userName=elonmusk"

# equivalent, using the tailnet hostname + Host header
curl -H "Host: twitterapi-proxy.ardenone.com" https://traefik-ardenone-cluster:8444/health
```

- TLS **validates without `-k`**; no client credentials are required.
- `GET /health` is free — use it for liveness checks in a capture harness.
- **Every other path costs credits on a cache miss.** For capture: run the request once
  to warm the cache, then all replays are free and byte-identical. Add `X-Cache-Bypass: 1`
  only when you deliberately want a fresh billed call.
- Read-only cluster introspection (used for everything in §2) is available at
  `http://traefik-ardenone-cluster:8001` — pods, deployments, services, ingressroutes,
  secrets, and **pod logs** are readable; `services/proxy` is not.

<a id="corpus-discrepancy"></a>
## 7. Corpus discrepancy — `corpus/twitterapi-proxy/corpus.json` is wrong

The corpus captured under `bf-kki8` (10 entries) was **not** captured from this service;
it was written against an assumed Twitter API v2 shape and does not match reality:

| Recorded in corpus.json | Actual (verified) |
|---|---|
| Paths `/2/users/by/username/…`, `/2/tweets/{id}`, `/2/tweets/search/recent` | `/twitter/user/info`, `/twitter/tweets`, `/twitter/tweet/advanced_search` — no `/2/*` path exists upstream |
| Auth `Authorization: Bearer` (`kind: bearer`) | `x-api-key` header, injected server-side |
| Upstream = Twitter API v2 | Upstream = `api.twitterapi.io` |
| Envelope `{data, includes, meta}` / errors `{title,type,status,detail}` | `{status, msg, data}` |
| Ignore `X-RateLimit-*`, rate-limit 429 window semantics | No `X-RateLimit-*` headers; metering is credit-based, and volatile headers are the Cloudflare/trace set in §5 |
| `x-cache-status` not modelled at all | Present on **every** response; the one header the proxy itself adds |
| `/health` expected to be proxied | `/health` is local to the proxy and returns an **empty** body |

A real capture against the live service is needed before the SEAM route fragment can be
diffed against the incumbent. Filed as follow-up bead **`bf-1x3w`**.
