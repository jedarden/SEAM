# seam-ci github-release exit 22 (seam-ci-v9lxj) — diagnosis

Recorded 2026-09-05, worker `glm-seam`, bead `seam-2bbd3152`.

`seam-ci-v9lxj` (revision `b4d4117`, auto-fired by argo-events on the push) got
further than any previous retained run and then died in the last stage:

| Stage | Phase | Notes |
|---|---|---|
| post-pending | Succeeded | continueOn fix (declarative-config 1c85ed79) holds |
| verify | Succeeded | gofmt → vet → golangci-lint → `go test -race ./...` |
| resolve-release | Succeeded | version=0.1.4 commit=b4d4117 should-release=true |
| image-build | **Succeeded** | 8m41s; digest `sha256:f960b188…961df9e70` |
| github-release | **Failed** | exit 22 after 8m40s, single attempt |

`exit 22` is `curl --fail` on an HTTP response ≥ 400. The pod was deleted by
podGC before anything could be read, so the response body is unrecoverable —
which is the real defect here, see "Template gap" below.

## What was ruled out

Three read-only probe workflows in `argo-workflows` (`seam-gh-token-probe2-vqw27`,
`seam-gh-token-probe6-jkdll`, `seam-gh-release-probe-l79fj`) checked the stage's
dependencies by property. Each mounts `secret/github-webhook-secret` as a file and
prints only properties, never the token:

- The token is alive: `200` on `/user`, scopes `repo, workflow, write:packages`.
- It authenticates as `jedarden` and holds **admin** on `jedarden/SEAM`
  (`collaborator permission: admin`), so push access is not the problem.
- A reproduction `POST /repos/jedarden/SEAM/releases` with a throwaway tag
  (`v0.0.0-ci-probe`, `draft: true`, `prerelease: true`) returned **201**; the
  scratch draft was deleted by the same script (`DELETE` → 204), so nothing remains.
- The GitHub mirror is current (`main` = b4d4117), so `target_commitish: main` was valid.

Conclusion: the release POST works. The v9lxj failure was a transient GitHub API
≥ 400 (a rate-limit blip or a 5xx — both produce `curl -f` exit 22), or less likely
the one other untolerated curl in the stage (the `ghcr.io/token` GET, verified
reachable afterwards).

## Template gap

Every curl in the `github-release` stage is `-fsSL` with no captured status or
body, so a 4xx/5xx survives only as `exit code 22` in the node status. podGC has
by then deleted the pod, and the stage's `retryStrategy` is `OnTransientError`,
which does not treat 22 as transient — so the `limit: 2` retry never fires.

The fix shape, for whoever picks this up: capture status plus response body per
call and print them before failing (as the probe above did), and either add a
retry expression covering curl 22 or tolerate the two cosmetic calls (`ghcr.io/token`
and the anonymous manifest probe) with `|| echo ''`. The `%%{http_code}` in the
manifest probe is also wrong — curl's `-w` prints `%%` as a literal `%`, so the
visibility check can never read `200` and release notes claim "HTTP %{http_code}".

Beacon: this stage is the last one standing between the gate and a green run.
