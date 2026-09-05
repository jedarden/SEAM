# SEAM — agent rules

Repo-level rules. These sit closer to the work than `~/CLAUDE.md`, so they win
where they differ. Nothing here relaxes the hard prohibitions there.

## Secrets travel by reference, never by value

**This is the rule this repo exists to embody.** SEAM's whole purpose is that a
calling agent passes a *route reference* and never sees the credential — the
mediator injects it server-side from OpenBao. Agents working *on* SEAM are held
to the same standard the product enforces.

So: **never write a credential value.** Not into a file, commit, bead, doc, log,
or chat. Write the retrieval path instead:

- an OpenBao path — `secret/<cluster>/<app>/<key>`
- or the command that fetches it — `gh auth token`, `git credential fill`

If you need to demonstrate that a credential works, **record the result of the
check, not the credential**: "verified: has `repo` scope, ADMIN on
declarative-config" is the deliverable. The token is not.

If a bead asks you to *provision* a credential, its deliverable is **the path
where the value now lives**, plus whatever policy grants access to it. A bead
that does not name a storage target is under-specified — say so and ask, rather
than inventing a home for the value.

### Why this is stated so bluntly

On 2026-08-09 a worker on `bf-2hwgv` ("Provision GitHub token with
declarative-config PR capability") did the verification correctly and then
pasted the live `gho_` GitHub OAuth token into
`docs/notes/github-token-declarative-config-pr.md` — twice, under "Current
Authentication Status" and "Option A". Nothing but Forgejo's pre-receive hook
stood between that and a public GitHub mirror. The token had to be rotated.

The failure was not carelessness about secrets in the abstract; the note was
otherwise careful and accurate. It was that "document the token" and "document
how to obtain the token" were treated as the same instruction. They are not.

A `PreToolUse` hook (`~/.claude/hooks/org-rule-guard.py`) now denies writes
containing a high-signal credential value in any file type. It **fails open** by
design — a missed violation is recoverable, a wedged fleet is not — so the rule
binds you whether or not the hook catches it. Genuine test fixtures may carry a
`gitleaks:allow` comment on the line; obvious placeholders (all-one-character
bodies, `example`, `REPLACE`) pass unblocked.

## Where things live

The SEAM **binary** lives here. Its **configuration** — route fragments, OpenBao
policies, Kubernetes manifests — belongs in `jedarden/declarative-config` under
`k8s/rs-manager/{seam,seam-retirement-evaluator}/`. That split is load-bearing:
the retirement evaluator opens PRs against declarative-config to remove retired
route fragments, so config has to be reviewable there.

`declarative-config/infra/` in this repo is a retirement pointer only. Do not
restore manifests there. New infrastructure configuration goes directly to the
authoritative `declarative-config` repository paths named by that pointer.

## Beads

This workspace migrated from bead-forge to **bead-rs on 2026-08-14**. Use the
**`bead`** CLI — `bf`/`br` do not work here and will fail with a schema
mismatch (`no such column: status`) rather than a clean error, because
`.beads/beads.db` is bead-rs-native, not bf-shaped. **If `bead <cmd>` fails
with a workspace/schema error, do not reach for bf's corruption-recovery
recipe (`rm .beads/beads.db && bf sync --import`) — running `bf` against this
store creates a valid-looking but empty bf-schema database and silently
destroys the live bead-rs state.** The correct recovery is:
`bead init` (preserves the committed workspace identity) then
`bead sync import-only --input .beads/checkpoint/forensic.jsonl
--restore-into-empty --actor <you>`. `beads.db` is the live store;
`.beads/checkpoint/` (git-tracked) is the durable checkpoint — flush with
`bead sync flush-only` after creating or closing beads. Never hand-edit
anything under `.beads/`.

Check for a running fleet worker before touching this repo's git state
(`pgrep -af "clau[d]e --print"` — the bracket avoids matching your own command).
Workers leave large numbers of untracked files mid-flight; committing over them
is how work gets lost.

## The seam-ci gate

Every push to `main` runs **seam-ci** on iad-ci, which runs the *whole*
`scripts/definition-of-done.sh --all` (gofmt, vet, golangci-lint,
`go test -race`, seam lint, benchmark gate) against a fresh clone. The fleet
twice landed long stretches of commits on top of a red gate — 25+ commits
between 2026-08-27 and 08-31 while `verify` was failing — because a red gate
was treated as someone else's problem. It is not:

**When the gate is red, work on this repo halts.** Do not claim SEAM beads,
and do not commit, until the tree at `main` passes verify again. A red gate
means the tree does not satisfy its own Definition of Done; anything built on
it inherits that.

Mechanically, five layers enforce this:

1. **`scripts/ci-gate.sh`** — is the gate red? Green (`exit 0`), red (`1`),
   cluster error (`2`), or no completed run yet (`3`). It checks the latest
   seam-ci run for `origin/main`'s current revision through the
   credential-free kubectl proxy. Run it before claiming anything.
2. **`.githooks/pre-commit`** — refuses commits while the gate is red, after
   the fast-lane DoD passes. Escape hatch:
   `SEAM_ALLOW_RED_GATE=1 git commit ...` (recorded in
   `.beads/bypasses.jsonl`, like a `--no-verify`). **This bypass is how the
   commit that *fixes* a red gate gets in** — that commit cannot wait for a
   green run it is itself about to cause. Any other use of the bypass is a
   violation to record and justify.
3. **`scripts/ci-gate-bead.sh`** — empties the bead claim frontier. It
   maintains one gate bead (title `GATE: seam-ci is red - do not claim SEAM
   beads`) wired as a blocker of every ready bead; since `bead list --ready`
   excludes blocked beads, a red gate leaves nothing to claim:
   - `open` — while red (idempotent; re-run after creating beads under a red
     gate so they pick up the blocker edge too)
   - `close` — only when `ci-gate.sh` reports green; refuses otherwise
   - `status` — gate phase, gate bead state, what is still claimable

If you find the gate red and the gate bead missing, run `open`. If you find
it green and the gate bead still open, run `close`. Both take seconds; both
are the difference between a gate and a dashboard.

4. **`scripts/ci-gate-watch.sh`** — does that for you, every 5 minutes.
   Installed by `tools/install_ci_gate_watch.sh` as the `ci-gate-watch.timer`
   systemd user timer, it reconciles the gate bead with live CI state: red →
   `open`, green → `close`, pending or cluster-error → hold (a run in flight
   never flaps the frontier, and an unreachable cluster never blocks or
   releases work). With the timer installed you only need the manual
   `open`/`close` above on a machine that does not run it. Transitions log to
   `~/.local/state/seam-ci-gate/watch.log`.

5. **`.claude/hooks/seam-ci-claim-gate.py`** — the layer that needs no
   cooperation at all. A Claude Code `PreToolUse` hook, wired by this repo's
   `.claude/settings.json`, it runs on every `Bash` call and refuses any that
   *invokes a claim* — `bead claim`, or `bead update --assignee ...` — while
   `scripts/ci-gate.sh` reports red. Layers 1-4 rely on a worker choosing to
   consult them; this one fires on the path itself, so the refusal arrives
   with the gate's own verdict attached:

   > seam-ci gate is RED — bead claim refused. GATE red revision=1e2b4fcc4
   > workflow=seam-ci-kgr42 phase=Failed. …

   Deliberate shapes, so the gate is not a second outage:
   - **It decides nothing itself.** It shells out to `scripts/ci-gate.sh`
     (layer 1) and quotes the verdict, so there is one definition of "red"
     here. An earlier draft re-implemented the cluster query and promptly
     disagreed with layer 1 about the same cluster — duplication is how a
     gate drifts into decoration.
   - **Fail open.** A missing `ci-gate.sh` (fresh clone), an unreachable
     cluster, a pending run, or an unparsable response all *allow* the call.
     Only a definitive red blocks. Never build on "the gate let me".
   - **Only claims are gated.** Finishing, verifying, closing or committing
     an already-assigned bead passes, as does any command that merely
     mentions claims. Dispatched work arrives pre-assigned by the NEEDLE
     daemon and never touches this hook — which is the way out of a red
     gate: complete what you hold, claim nothing new.
   - **The pre-commit bypass does not apply here.** `SEAM_ALLOW_RED_GATE=1`
     exists to land the commit that *fixes* a red gate; landing a fix needs
     no claim.
   - Verdicts are cached for 60 s (`SEAM_CI_GATE_CACHE` overrides the path)
     so parallel workers do not each pay the round trip.

   Because the hook is repo-scoped and committed, it self-activates for any
   Claude Code session opened on this checkout — nothing to install, and no
   NEEDLE-side change is needed. Consult it without attempting a claim:
   `python3 .claude/hooks/seam-ci-claim-gate.py --check`.


