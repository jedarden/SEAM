#!/usr/bin/env bash
# Refuse a change to a container's image contents that does not bump its VERSION.
#
# containers/seam and containers/seam-retirement-evaluator publish as
# versioned images; the tag is the VERSION file verbatim, and a change that
# ships without a bump makes the existing tag mean two different builds
# depending on where you look. This checker turns "bump VERSION in the same
# change as the code" from advice into a gate -- the per-route usage-metrics
# chain did it by hand (ec1f032f -> 0.1.13, 84f451b5 -> 0.1.14); every later
# container change gets checked.
#
# Usage:
#   scripts/check-container-versions.sh                 # staged changes (pre-commit)
#   scripts/check-container-versions.sh --range A..B    # a push range (CI)

set -uo pipefail

# Verification also runs from `git archive` extractions, which intentionally
# do not carry a .git directory. Prefer git's root in a checkout, but fall
# back to the directory containing this script when there is no repository
# metadata (same root detection as scripts/definition-of-done.sh).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null)"; then
  :
else
  REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
fi
cd "$REPO_ROOT"

MODE="staged"
RANGE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --range) MODE="range"; RANGE="${2:-}"; shift 2 ;;
    -h|--help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# The whole tree is diffed, not just containers/: both images are built from
# the repository root (seam-ci's kaniko step uses the git context), so image
# content is decided by the Dockerfiles' COPY lines, not by directory
# proximity to the Dockerfile.
if [ "$MODE" = "range" ]; then
  [ -n "$RANGE" ] || { echo "--range needs a revision range" >&2; exit 2; }
  changed=$(git diff --name-only "$RANGE")
  version_at() { git show "${RANGE%%..*}:$1" 2>/dev/null | tr -d '[:space:]'; }
  new_version_at() { git show "${RANGE##*..}:$1" 2>/dev/null | tr -d '[:space:]'; }
else
  changed=$(git diff --cached --name-only 2>/dev/null)
  version_at() { git show "HEAD:$1" 2>/dev/null | tr -d '[:space:]'; }
  new_version_at() { git show ":$1" 2>/dev/null | tr -d '[:space:]'; }
fi

[ -n "$changed" ] || { echo "no files changed"; exit 0; }

# What each image actually ships -- the COPY lines of
# containers/seam/Dockerfile and
# containers/seam-retirement-evaluator/Dockerfile. A file that ships in no
# image maps to empty and can never require a bump.
image_owner() {
  case "$1" in
    cmd/*|internal/*|spec/*|go.mod|go.sum) printf 'containers/seam\n' ;;
    tools/seam-retirement-evaluator/*)     printf 'containers/seam-retirement-evaluator\n' ;;
    *)                                     printf '\n' ;;
  esac
}

# Files that do not end up in the shipped image. Go test files are stripped
# by the compiler; docs never enter a build context. Keep this list tight: a
# loose exemption is a loophole, not a convenience.
is_exempt() {
  case "${1##*/}" in
    VERSION|README.md|*.md) return 0 ;;
  esac
  case "$1" in
    */tests/*|*/testdata/*|*/fixtures/*|*/__pycache__/*) return 0 ;;
    */test_*.py|*_test.py|*_test.go|*.test.mjs) return 0 ;;
  esac
  return 1
}

# Greater-than on dotted numeric versions, so a bump cannot go backwards.
version_gt() {
  [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" = "$1" ]
}

declare -A touched=()
while IFS= read -r file; do
  [ -n "$file" ] || continue
  container=$(image_owner "$file")                     # containers/<name>
  [ -n "$container" ] || continue                      # ships in no image
  [ -f "$container/VERSION" ] || continue              # not a versioned container
  is_exempt "$file" && continue
  touched["$container"]=1
done <<< "$changed"

[ ${#touched[@]} -gt 0 ] || { echo "container changes are docs/tests only; no bump required"; exit 0; }

failures=0
for container in "${!touched[@]}"; do
  name="${container#containers/}"
  old=$(version_at "$container/VERSION")
  new=$(new_version_at "$container/VERSION")

  if [ -z "$new" ]; then
    echo "✗ $name: image contents changed but $container/VERSION is not in this change"
    failures=$((failures + 1))
  elif [ "$old" = "$new" ]; then
    echo "✗ $name: image contents changed but VERSION is still $new"
    echo "    A published tag must mean exactly one build. Bump it in this change."
    failures=$((failures + 1))
  elif [ -n "$old" ] && ! version_gt "$new" "$old"; then
    echo "✗ $name: VERSION went backwards, $old -> $new"
    failures=$((failures + 1))
  else
    echo "✓ $name: $old -> $new"
  fi
done

if [ "$failures" -gt 0 ]; then
  cat >&2 <<'EOF'

Bump the VERSION file in the same change as the code it describes.

  printf '%s\n' "<next semver>" > containers/<name>/VERSION

A published tag must mean exactly one build: shipping different code under a
tag that already exists makes that tag ambiguous, and the drift stays
invisible until an unrelated bump ships it.
EOF
  exit 1
fi

exit 0
