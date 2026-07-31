# bf-1x3w — re-capture twitterapi-proxy corpus against the live service

Replaces `corpus/twitterapi-proxy/corpus.json` as written under `bf-kki8`. That file
was authored against an assumed Twitter API v2 shape and matched nothing the incumbent
does; `bf-zi92` (`docs/research/twitterapi-proxy-deployment.md`) established the real
shape, and this bead captured against the running service.

Captured 2026-07-31T01:43:40Z over the tailnet ingress
(`https://twitterapi-proxy.ardenone.com:8444`, `--resolve` to the Traefik tailnet
device `100.71.31.73`, TLS validates without `-k`, no client credentials). Every fact
below was observed live, not inferred.

## What changed

| | bf-kki8 corpus | this capture |
|---|---|---|
| Paths | `/2/users/by/username/…`, `/2/tweets/{id}`, `/2/tweets/search/recent` | `/twitter/user/info`, `/twitter/tweets`, `/twitter/tweet/advanced_search`, `/twitter/user/followers`, `/twitter/user/last_tweets` |
| Secret injection | `injectAs {kind: bearer}` | `injectAs {kind: header, name: x-api-key}` |
| Volatile headers | `X-RateLimit-*` (do not exist) | `Alt-Svc, Cf-Cache-Status, Cf-Ray, Content-Length, Date, Nel, Report-To, Server, X-Cache-Status, X-Trace-Id` |
| `x-cache-status` | unmodelled | modelled: ignored as volatile, and exercised by a dedicated bypass entry |
| `/health` | expected proxied, generic ignore list | proxy-local, empty body, no secret, and **no `x-cache-status`** |
| Fabricated entries | `rate-limit-error` (429), `unauthorized-error` (401), `create-tweet` (POST /2/tweets) | dropped — the service produces no 429/401, and a real write op both bills and posts to X |
| Entries | 10 | 12 |

The ref string `vault:seam/routes/twitterapi-proxy/api-key` was kept; only `injectAs`
was wrong.

## Two corrections to the bf-zi92 research doc

Probing every endpoint rather than just `/twitter/user/info` contradicted two claims;
§7 of the doc now records both.

1. **`{status, msg, data}` is not the universal envelope.** Three shapes coexist —
   `user/info` wraps in `data`; `user/last_tweets` wraps in `data` *and* adds a `code`
   field; `tweets`, `advanced_search` and `user/followers` return a bare payload with
   no `data` wrapper (followers puts `status`/`msg`/`code` flat at the top level).
2. **Errors use neither envelope** — `{"detail": "…"}`. `404 {"detail":"Not Found"}`
   for an unknown path, `400 {"detail":"userName is required"}` for a missing param.

## Determinism — why 6 of 12 entries set `ignoreBody`

This is the substantive design call. The incumbent and SEAM are independent caching
proxies in front of the same live upstream, so any payload carrying live counters
(`followers`, `likeCount`, `viewCount`) or a moving index (search, timelines) cannot be
expected to match byte-for-byte across the two. Compounding it, a request whose `cursor`
is absent or empty is a "front page" request and expires after `FRONT_PAGE_TTL_SECS=300`,
so even the same proxy re-fetches every 5 minutes.

Rather than blanket-ignoring bodies, the corpus is split so that **6 entries compare
bodies for real**: `health` (empty), the two `{"detail": …}` errors, `post-method-forwarded`,
and — the anchor — `user-followers-cursor-paged`. A non-empty `cursor` makes a request
*not* a front-page request, so it is cached forever and is genuinely frozen. Verified:
first call `miss`, repeat `hit`, and a repeat with the query params **reordered** also
`hit`, confirming the cache key normalizes query order.

`compare.Options.IgnoreBody` never weakens the leak check — the body is still scanned for
the injected key regardless — so the security invariant holds on all 12 entries.

## Other deliberate choices

- **`expect.status` omitted everywhere.** Per `compare.go`, setting it pins the *SEAM*
  status and demotes the incumbent's to context-only. For a verbatim pass-through proxy
  the default rule (`incumbent.status == seam.status`) is the stronger check. The status
  verified live is recorded in each entry's `description` instead.
- **`cache-bypass-forced-miss` is `skip`ped.** `X-Cache-Bypass: 1` forces a fresh billed
  upstream call by design (verified: turns a `hit` into a `miss`), so replaying it in CI
  would bill credits on both targets every run. It stays in the corpus to document the
  control header; unskip deliberately.
- **No write ops.** twitterapi.io's `*_v2` endpoints cost $0.002–0.003 *and* actually post
  to X. Method forwarding is proved instead by `POST` to an unknown path, which returns
  the same `404 {"detail":"Not Found"}` as `GET` — no side effect, nothing billed.
- **Credential-stripping entry** sends decoy `Authorization` and `X-Api-Key` headers
  (literal decoys, not secrets). Verified live: both are stripped and change nothing.

## Verification

`scripts/capture-twitterapi-proxy.sh` re-probes every non-skipped entry and asserts the
recorded status. Run `warm` first to prime the cache, `verify` to check. Full pass at
capture time: all 11 replayable entries matched, with the six 2xx reads served from cache
(`x-cache-status: hit`, unbilled), both errors correctly showing `miss` (non-2xx is never
cached), and `/health` reporting no cache header at all.

The corpus also loads clean through the real `internal/corpus.Load` validator.

## Loose end (not fixed here — pre-existing, affects every corpus)

`corpus.Load` and `AppendEntry` run the HTTP **method** through
`textproto.CanonicalMIMEHeaderKey`, so `"GET"` becomes `"Get"` and `"POST"` becomes
`"Post"` in memory. That is header-key canonicalization applied to the wrong field.
It is consistent across argocd/zai/twitterapi and may well be absorbed downstream, but
it is not what the code intends. Out of scope for this bead; worth its own.
