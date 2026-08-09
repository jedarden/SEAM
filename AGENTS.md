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

`declarative-config/infra/` in this repo is a **staging copy scheduled for
removal** (`bf-2abnu`). Do not add to it and do not treat it as authoritative —
it has already drifted from the real repo. New infra config goes to
declarative-config directly.

## Beads

Use `bf`, never the deprecated `br`. `beads.db` is the live store and
`issues.jsonl` is only a checkpoint, so run `bf sync --flush-only` after
creating or closing beads — otherwise they exist only in the db and a
`bf doctor --repair` destroys them. Never hand-edit anything under `.beads/`.

Check for a running fleet worker before touching this repo's git state
(`pgrep -af "clau[d]e --print"` — the bracket avoids matching your own command).
Workers leave large numbers of untracked files mid-flight; committing over them
is how work gets lost.
