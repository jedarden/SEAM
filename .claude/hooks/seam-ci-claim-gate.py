#!/usr/bin/env python3
"""PreToolUse guard: blocks CLAIMING SEAM beads while the seam-ci gate is red.

On 2026-08-27..31 25+ commits landed on this repo while internal/server
carried 99 compile errors -- a red seam-ci stopped being a stop signal
because nothing enforced it. This hook is the enforcement: while
`scripts/ci-gate.sh` reports red, a worker may not take on NEW work here
(`bead claim`, or `bead update --assignee`). Work already assigned keeps
flowing -- dispatched beads arrive pre-assigned by the NEEDLE daemon, which
never passes through this hook, and finishing, verifying or closing an
assigned bead needs no claim. That is the right shape: the way out of a red
gate is completing in-flight work, not pulling more.

The verdict is not decided here. This hook calls `scripts/ci-gate.sh` -- the
one check AGENTS.md documents -- so there is exactly one definition of "red"
in the repo and the deny message quotes the workflow and revision it judged.
An earlier version of this hook re-implemented the cluster query itself and
drifted: it reported the gate green while ci-gate.sh called the same cluster
pending. Wiring beats duplicating; see AGENTS.md, "The seam-ci gate".

FAILS OPEN by design, matching ~/.claude/hooks/org-rule-guard.py: a missing
ci-gate.sh, an unreachable cluster, a timeout, a pending run or any parse
failure all allow the call. A wedged fleet is worse than a missed block --
the gate must never become a second outage layered on the first. Fail-open
covers "could not tell" only; a definitive red always blocks. The pre-commit
`SEAM_ALLOW_RED_GATE=1` bypass is deliberately NOT honoured here: that
escape exists to land the commit that fixes a red gate, and landing a fix
needs no claim -- dispatched work is already assigned.

A verdict is cached for CACHE_TTL seconds so parallel workers do not each
pay the git ls-remote + kubectl round trip on every claim-shaped call.

`--check` prints the current gate state and exits 0, or 1 when red, for
humans and workers to consult without attempting a claim.
"""
import json
import os
import re
import subprocess
import sys
import tempfile
import time

REPO_ROOT = os.path.dirname(
    os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
)
GATE_SCRIPT = os.path.join(REPO_ROOT, "scripts", "ci-gate.sh")
GATE_TIMEOUT = 12  # script's own hook budget is 15s; leave room to report
CACHE_PATH = os.environ.get(
    "SEAM_CI_GATE_CACHE", "/tmp/seam-ci-claim-gate-cache.json"
)
CACHE_TTL = 60.0

GREEN, RED, UNKNOWN = "green", "red", "unknown"


def allow(note=""):
    if note:
        print(note, file=sys.stderr)
    sys.exit(0)


def deny(reason):
    print(json.dumps({"hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": "deny",
        "permissionDecisionReason": reason,
    }}))
    sys.exit(0)


def is_claim_command(cmd):
    """Return a short label if cmd invokes a claim, else None.

    Only a segment that *invokes* bead counts -- split on shell separators
    and require the bead verb up front, so `grep 'bead claim' docs/`,
    `echo ...` or a commit message mentioning it are never gated.
    """
    for seg in re.split(r"\n|&&|\|\||;|\|", cmd):
        tokens = seg.split()
        while tokens and re.match(r"^[A-Za-z_][A-Za-z0-9_]*=", tokens[0]):
            tokens.pop(0)
        if tokens[:1] == ["env"]:
            tokens.pop(0)
        if len(tokens) < 2 or tokens[0] not in ("bead", "bf"):
            continue
        if tokens[1] == "claim":
            return "bead claim"
        if tokens[1] == "update" and any(
            t.startswith("--assignee") for t in tokens[2:]
        ):
            return "bead update --assignee"
    return None


def _cached():
    try:
        cached = json.load(open(CACHE_PATH))
        age = time.time() - cached.get("ts", 0)
        if 0 <= age < CACHE_TTL and cached.get("state") in (GREEN, RED):
            return (cached["state"], cached.get("detail", ""))
    except (OSError, ValueError, KeyError):
        pass
    return None


def _store(state, detail):
    if state == UNKNOWN:
        return  # a pending run or a dead cluster must not stick for 60s
    try:
        fd, tmp = tempfile.mkstemp(
            dir=os.path.dirname(CACHE_PATH) or ".", prefix=".gate-cache-"
        )
        with os.fdopen(fd, "w") as fh:
            json.dump({"ts": time.time(), "state": state, "detail": detail}, fh)
        os.replace(tmp, CACHE_PATH)
    except OSError:
        pass


def gate():
    """(state, detail) from scripts/ci-gate.sh, which owns the verdict.

    Its exit codes: 0 green, 1 red, 2 could-not-tell, 3 no completed run yet.
    Anything this wrapper cannot get a clean answer for is UNKNOWN, and
    UNKNOWN allows -- the gate halts on a known failure, never on a guess.
    """
    hit = _cached()
    if hit:
        return hit
    if not os.access(GATE_SCRIPT, os.X_OK):
        return (UNKNOWN, "scripts/ci-gate.sh missing or not executable")
    try:
        proc = subprocess.run(
            [GATE_SCRIPT], cwd=REPO_ROOT, capture_output=True, text=True,
            timeout=GATE_TIMEOUT,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return (UNKNOWN, "ci-gate.sh: %s" % type(exc).__name__)
    out = (proc.stdout or "").strip().splitlines()
    verdict = out[0] if out else ""
    if proc.returncode == 0:
        state = GREEN
    elif proc.returncode == 1:
        state = RED
    else:
        return (
            UNKNOWN,
            "ci-gate.sh exit %s: %s" % (proc.returncode, verdict or "(no output)"),
        )
    detail = verdict
    _store(state, detail)
    return (state, detail)


def main():
    if "--check" in sys.argv:
        state, detail = gate()
        print("seam-ci claim gate: %s (%s)" % (state.upper(), detail))
        sys.exit(1 if state == RED else 0)
    try:
        payload = json.load(sys.stdin)
    except ValueError:
        allow("seam-ci-claim-gate: unparseable input, allowing (fail open)")
    if payload.get("tool_name") != "Bash":
        allow()
    cmd = (payload.get("tool_input") or {}).get("command") or ""
    what = is_claim_command(cmd)
    if not what:
        allow()
    state, detail = gate()
    if state == RED:
        deny(
            "seam-ci gate is RED -- %s refused. %s. Claiming SEAM beads is "
            "blocked while the latest seam-ci run is Failed (repo AGENTS.md, "
            "\"The seam-ci gate\"). Finish, verify or close work already "
            "assigned to you instead; new claims wait for a green run. "
            "Check the gate: python3 .claude/hooks/"
            "seam-ci-claim-gate.py --check" % (what, detail)
        )
    if state == UNKNOWN:
        allow(
            "seam-ci-claim-gate: gate state unavailable (%s), allowing -- "
            "fail open, do not treat this as a green light in spirit" % detail
        )
    allow()


if __name__ == "__main__":
    main()
