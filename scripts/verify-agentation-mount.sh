#!/usr/bin/env bash
# Browser-level mount check for the Agentation toolbar on the /docs UI.
#
# TestDocsAgentationWiring (internal/server/docs_agentation_test.go) pins the
# server-side wiring contract: the import map must resolve every bare
# specifier the toolbar's module imports, must appear before the module, and
# the module must mount the <Agentation/> component (agentation is a library
# — a bare `import "agentation"` loads it and renders nothing, which is
# exactly the silently-broken state this check exists to catch). The Go test
# cannot see what a browser does with the page. This script is the other
# half: it loads the page in a real headless chromium and asserts the
# toolbar actually mounted. Two markers, both required:
#
#   - `id="agentation-root"` — the host the loader creates; the workspace UI
#     policy's own definition of wired (document.getElementById(...)).
#   - the component's portal (`data-agentation-portal` hosting
#     `<agentation-toolbar>`) — Agentation renders itself through a portal
#     appended to <body>, NOT inside #agentation-root, so the host div alone
#     could exist while the render silently failed. The portal element is
#     the proof the component actually ran.
#
# Usage:
#   scripts/verify-agentation-mount.sh [DOCS-URL]
#       Mount-check a running /docs page. DOCS-URL defaults to
#       http://localhost:8080/docs. The page must be reachable without an
#       identity a browser cannot present (loopback callers of a local
#       `seam serve` are identity-denied on /docs — point this at a page
#       rendered for you, or use --build).
#
#   scripts/verify-agentation-mount.sh --build
#       Render the page from the real handler (`go test` dumps it via the
#       SEAM_AGENTATION_DUMP_HTML hook), serve the dump on a loopback port,
#       and mount-check those exact bytes. Needs the Go toolchain; no
#       running SEAM required.
#
# Requirements: a chromium build (SEAM_CHROME env var, system chrome, or the
# newest build in the playwright cache) and network access to esm.sh, where
# the import map resolves react and agentation. Not part of
# scripts/definition-of-done.sh — it is the manual browser-level
# verification the Go suite cannot perform.
#
# Exit 0 prints MOUNT OK; exit 1 prints MOUNT FAILED and where to look.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

BUDGET_MS="${SEAM_MOUNT_BUDGET_MS:-20000}"

find_chrome() {
    local candidates=() candidate newest
    if [ -n "${SEAM_CHROME:-}" ] && [ -x "$SEAM_CHROME" ]; then
        echo "$SEAM_CHROME"
        return 0
    fi
    for candidate in chromium chromium-browser google-chrome google-chrome-stable chrome; do
        if command -v "$candidate" >/dev/null 2>&1; then
            candidates+=("$(command -v "$candidate")")
        fi
    done
    # Playwright cache: newest full chromium build wins over headless_shell.
    while IFS= read -r newest; do candidates+=("$newest"); done < <(
        find "${HOME}/.cache/ms-playwright" -maxdepth 3 \
            \( -path '*chromium*/chrome-linux*/chrome' -o -name 'headless_shell' \) \
            -type f 2>/dev/null | sort -Vr
    )
    # NixOS: a chromium already built into the store carries its own library
    # closure, which matters where system libglib/nss are absent.
    while IFS= read -r newest; do candidates+=("$newest"); done < <(
        ls -d /nix/store/*-chromium-*/bin/chromium 2>/dev/null | sort -Vr
    )
    # A candidate that cannot even report its version (playwright builds
    # often lack system libraries) is worse than a later working one.
    for candidate in "${candidates[@]}"; do
        if "$candidate" --version >/dev/null 2>&1; then
            echo "$candidate"
            return 0
        fi
    done
    return 1
}

CHROME="$(find_chrome)" || {
    echo "MOUNT FAILED: no chromium build found (set SEAM_CHROME, install chromium, or populate ~/.cache/ms-playwright)" >&2
    exit 1
}
echo "Using chromium: $CHROME"

WORK_DIR="$(mktemp -d)"
cleanup() { rm -rf "$WORK_DIR"; }
trap cleanup EXIT

SERVE_PID=""
start_server_for_dump() {
    # Command substitution captures this function's stdout as the URL:
    # every progress line must go to stderr.
    local port
    port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
    echo "Rendering /docs from the real handler into $WORK_DIR/docs.html ..." >&2
    SEAM_AGENTATION_DUMP_HTML="$WORK_DIR/docs.html" \
        go -C "$REPO_ROOT" test ./internal/server -run '^TestDocsAgentationWiring$' -count=1 >/dev/null
    if [ ! -s "$WORK_DIR/docs.html" ]; then
        echo "MOUNT FAILED: handler produced no dump" >&2
        exit 1
    fi
    echo "Serving the dump on 127.0.0.1:$port ..." >&2
    python3 -m http.server "$port" --bind 127.0.0.1 --directory "$WORK_DIR" \
        >/dev/null 2>&1 &
    SERVE_PID=$!
    echo "http://127.0.0.1:$port/docs.html"
}

case "${1:---none}" in
    --build)
        DOCS_URL="$(start_server_for_dump)"
        ;;
    --none)
        echo "usage: $0 [DOCS-URL] | --build" >&2
        exit 2
        ;;
    *)
        DOCS_URL="$1"
        ;;
esac

# headless_shell is already headless; full chrome needs the flag since v132.
CHROME_FLAGS=(--no-sandbox --disable-gpu --disable-dev-shm-usage)
if [ "$(basename "$CHROME")" != "headless_shell" ]; then
    CHROME_FLAGS+=(--headless=new)
fi

# --virtual-time-budget lets the module scripts (import map -> esm.sh ->
# agentation -> toolbar mount) finish before the DOM is serialized.
if ! "$CHROME" "${CHROME_FLAGS[@]}" \
        --dump-dom --virtual-time-budget="$BUDGET_MS" --timeout=$((BUDGET_MS + 15000)) \
        "$DOCS_URL" >"$WORK_DIR/dom.html" 2>"$WORK_DIR/chrome.err"; then
    echo "MOUNT FAILED: chromium could not load $DOCS_URL" >&2
    tail -5 "$WORK_DIR/chrome.err" >&2 || true
    [ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null || true
    exit 1
fi
[ -n "$SERVE_PID" ] && kill "$SERVE_PID" 2>/dev/null || true

HOST_OK=no; PORTAL=no
grep -q 'id="agentation-root"' "$WORK_DIR/dom.html" && HOST_OK=yes
grep -q '<agentation-toolbar>\|data-agentation-portal' "$WORK_DIR/dom.html" && PORTAL=yes

if [ "$HOST_OK$PORTAL" = yesyes ]; then
    echo "MOUNT OK: #agentation-root host present and the Agentation toolbar portal rendered in $DOCS_URL"
    exit 0
fi
if [ "$HOST_OK" = yes ]; then
    echo "MOUNT FAILED: #agentation-root exists but the toolbar portal did not render — the loader created the host, then the import or render failed" >&2
else
    echo "MOUNT FAILED: #agentation-root absent from the rendered DOM of $DOCS_URL" >&2
    echo "The page loaded but the mount loader never ran — check the import map precedes the module and esm.sh was reachable." >&2
fi
cp "$WORK_DIR/dom.html" "${SEAM_MOUNT_KEEP_DOM:-/tmp/seam-agentation-dom.html}"
echo "Rendered DOM kept at ${SEAM_MOUNT_KEEP_DOM:-/tmp/seam-agentation-dom.html} for diagnosis" >&2
exit 1
