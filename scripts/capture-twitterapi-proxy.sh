#!/usr/bin/env bash
#
# Re-capture / verify corpus/twitterapi-proxy/corpus.json against the live
# incumbent (bead bf-1x3w; supersedes the bf-kki8 corpus).
#
# The incumbent is reachable ONLY from a Tailscale peer: DNS for
# twitterapi-proxy.ardenone.com is public but the address is routable only over
# the tailnet mesh, and Traefik's `vpn` entryPoint (:8444) is the sole exposed
# path. See docs/research/twitterapi-proxy-deployment.md §3 and §6.
#
# Two modes:
#   verify  (default)  re-probe every entry and assert the recorded status and
#                      cache/envelope facts still hold. Free after warming:
#                      cached 2xx replays cost no upstream credits.
#   warm               prime the proxy's SQLite cache so a subsequent verify (or
#                      a seam-replay run) is byte-stable and unbilled.
#
# COST: a cache miss on any /twitter/* path bills twitterapi.io credits
# (~18 for /twitter/user/info). Non-2xx responses are never cached, so the two
# error entries always hit upstream — twitterapi.io does not bill those.
# The corpus's cache-bypass-forced-miss entry is NOT exercised here: it forces a
# billed call by design and is marked skip in the corpus.

set -euo pipefail

MODE="${1:-verify}"

HOST="twitterapi-proxy.ardenone.com"
PORT="8444"
# Tailnet address of the ardenone-cluster Traefik device. --resolve is required:
# the cert is valid for $HOST, so connecting to the tailnet name directly fails
# TLS verification. TLS validates without -k.
TAILNET_IP="${TWITTERAPI_PROXY_IP:-100.71.31.73}"
BASE="https://${HOST}:${PORT}"
RESOLVE=(--resolve "${HOST}:${PORT}:${TAILNET_IP}")

fail=0

# probe <label> <expected-status> <curl args...>
# Prints the observed status and x-cache-status, and flags a status mismatch.
probe() {
  local label="$1" want="$2"; shift 2
  local hdr body got cache
  hdr="$(mktemp)"; body="$(mktemp)"
  if ! curl -sS -D "$hdr" -o "$body" --max-time 30 "${RESOLVE[@]}" "$@"; then
    printf '  %-30s CURL FAILED\n' "$label"; fail=1; rm -f "$hdr" "$body"; return
  fi
  got="$(awk 'tolower($0) ~ /^http\// {s=$2} END {print s}' "$hdr" | tr -d '\r')"
  cache="$(awk 'tolower($0) ~ /^x-cache-status:/ {print $2}' "$hdr" | tr -d '\r')"
  if [[ "$got" == "$want" ]]; then
    printf '  %-30s %s  x-cache-status=%-5s %s\n' "$label" "$got" "${cache:-none}" "$(head -c 60 "$body" | tr -d '\n')"
  else
    printf '  %-30s %s (WANT %s)  MISMATCH\n' "$label" "$got" "$want"; fail=1
  fi
  rm -f "$hdr" "$body"
}

echo "incumbent: ${BASE}  (via ${TAILNET_IP}, tailnet-only)"
echo "mode:      ${MODE}"
echo

# The proxy's own endpoint: local, empty body, no upstream call, no credit.
# Also the liveness gate — if this fails, nothing below is meaningful.
echo "== proxy-local =="
probe health 200 "${BASE}/health"
echo

echo "== pass-through reads (2xx; cached, so free after warming) =="
probe user-info                  200 "${BASE}/twitter/user/info?userName=elonmusk"
probe user-last-tweets           200 "${BASE}/twitter/user/last_tweets?userName=elonmusk"
probe tweets-by-id               200 "${BASE}/twitter/tweets?tweet_ids=2082712206169256279"
probe search-advanced            200 "${BASE}/twitter/tweet/advanced_search?query=from%3Aelonmusk&queryType=Latest"
probe user-followers-front-page  200 "${BASE}/twitter/user/followers?userName=elonmusk"
probe user-followers-cursor-paged 200 "${BASE}/twitter/user/followers?userName=elonmusk&cursor=1872192298332259623"
echo

echo "== errors (never cached; upstream does not bill non-2xx) =="
probe error-unknown-path         404 "${BASE}/twitter/nonexistent/endpoint"
probe error-missing-required-param 400 "${BASE}/twitter/user/info"
probe post-method-forwarded      404 -X POST "${BASE}/twitter/nonexistent/endpoint"
echo

# The proxy strips inbound x-api-key and authorization (FORBIDDEN_FORWARD_HEADERS)
# and injects its own key, so these decoys must change nothing.
echo "== credential stripping (decoy values, not secrets) =="
probe credential-stripping 200 \
  -H 'Authorization: Bearer NOT-A-REAL-TOKEN-decoy' \
  -H 'X-Api-Key: NOT-A-REAL-KEY-decoy' \
  "${BASE}/twitter/tweets?tweet_ids=2082712206169256279"
echo

if [[ "$MODE" == "warm" ]]; then
  echo "Cache warmed. Cursor-paged entries are now cached forever; cursorless"
  echo "'front page' entries expire after FRONT_PAGE_TTL_SECS=300, so run a"
  echo "replay within 5 minutes if you need those to be stable."
  echo
fi

if (( fail )); then
  echo "FAIL: at least one entry did not match the corpus." >&2
  exit 1
fi
echo "OK: all probed entries match corpus/twitterapi-proxy/corpus.json."
