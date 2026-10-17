#!/usr/bin/env bash
set -euo pipefail

repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
temp_dir="$(mktemp -d "$repo_dir/.bead-check-test.XXXXXX")"
trap 'rm -rf "$temp_dir"' EXIT
mkdir -p "$temp_dir/bin" "$temp_dir/work/.beads"
printf '{}\n' > "$temp_dir/work/.beads/config.json"

cat > "$temp_dir/bin/bead" <<'MOCK'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$MOCK_CALL_LOG"
case "$*" in
  'sync status --format json')
    case "${MOCK_STATUS_MODE:-ok}" in
      ok) printf '%s\n' '{"checkpoint_present":true,"checkpoint_consistent":true,"relationship":"aligned","generation_id":"gen-test"}' ;;
      behind) printf '%s\n' '{"checkpoint_present":true,"checkpoint_consistent":false,"relationship":"behind","generation_id":"gen-test"}' ;;
      invalid) printf '%s\n' '[]' ;;
    esac
    ;;
  'doctor --json')
    case "${MOCK_DOCTOR_MODE:-ok}" in
      ok) printf '%s\n' '{"checks":[{"status":"OK"}],"has_errors":false,"has_warnings":false}' ;;
      errors) printf '%s\n' '{"checks":[{"status":"FAIL"}],"has_errors":true,"has_warnings":false}'; exit 5 ;;
      invalid) printf '%s\n' '{"checks":{},"has_errors":false,"has_warnings":false}' ;;
    esac
    ;;
  *) echo "unexpected bead mutation: $*" >&2; exit 99 ;;
esac
MOCK
chmod +x "$temp_dir/bin/bead"

run_case() {
  local label="$1" expected="$2" status_mode="$3" doctor_mode="$4" actual
  : > "$temp_dir/calls"
  actual=0
  (
    cd "$temp_dir/work"
    PATH="$temp_dir/bin:$PATH" MOCK_CALL_LOG="$temp_dir/calls" \
      MOCK_STATUS_MODE="$status_mode" MOCK_DOCTOR_MODE="$doctor_mode" \
      "$repo_dir/scripts/bead-checkpoint-consistency-check.sh" --verbose
  ) > "$temp_dir/output" 2>&1 || actual=$?
  if [[ "$actual" -ne "$expected" ]]; then
    echo "$label: expected exit $expected, got $actual" >&2
    cat "$temp_dir/output" >&2
    exit 1
  fi
  local expected_calls='sync status --format json
doctor --json'
  if [[ "$status_mode" == invalid ]]; then
    expected_calls='sync status --format json'
  fi
  if ! diff -u <(printf '%s\n' "$expected_calls") "$temp_dir/calls"; then
    echo "$label: unexpected bead commands" >&2
    exit 1
  fi
}

run_case 'healthy JSON arrays' 0 ok ok
run_case 'checkpoint behind' 1 behind ok
run_case 'doctor integrity failure' 1 ok errors
run_case 'invalid status shape' 1 invalid ok
run_case 'invalid doctor checks array' 1 ok invalid
echo 'read-only checkpoint checker tests passed'
