# Retirement-Finding Handoff Runbook

Status: implemented 2026-09-26 (bead `seam-0c524fe3`).
Audience: the operator who receives a retirement finding and decides whether
to land it.

This is the complete detection-only workflow from the retirement evaluator's
log record to a caller-visible deprecation — and back off it. The evaluator
(`tools/seam-retirement-evaluator/`, see its README for architecture and
isolation) never writes: its entire output is one structured log record and
one Prometheus counter per candidate. Everything after step 1 is a human
edit. The runtime behavior of the block being landed is documented in
[docs/notes/brownout-runtime-semantics.md](notes/brownout-runtime-semantics.md);
this document is only the procedure.

---

## The workflow at a glance

```
evaluator detects quiet route version
        │  (structured log record + seam_retirement_deprecation_candidates_total)
        ▼
human verifies the finding                       ← step 2
        │
human commits the proposed block to              ← step 3
declarative-config main (fragment ConfigMap)
        │  ArgoCD syncs the ConfigMap
        ▼
SEAM hot-reloads the fragment                    ← step 4 (no deployment)
        │  route table swap; Deprecation/Sunset headers appear,
        │  410 Gone inside brownout windows, /changes lists the route
        ▼
(optional) caller appears → revert commit        ← step 5 (same channel back)
```

There is no PR, no branch, no review gate, no token anywhere on this path.
Reversibility is the gate.

## Step 1 — Read the finding

The evaluator runs hourly (`EvaluationInterval`) as a Deployment in
`k8s/rs-manager/seam-retirement-evaluator/`. Each candidate it finds produces
exactly one structured zap record and one counter increment.

```bash
kubectl --server=http://traefik-rs-manager:8001 \
  logs -n seam-retirement-evaluator deploy/seam-retirement-evaluator \
  | grep 'Deprecation candidate detected'
```

The record carries everything the handoff needs:

| Field | Meaning |
|---|---|
| `route` / `api_version` / `spec_version` | the quiet route version |
| `quiet_since` | last observed request (RFC 3339) |
| `eval_window` | `max(3 × observed_max_gap, 7d)` the route exceeded |
| `reason` | the eligibility verdict, e.g. `Zero traffic for 720h0m0s (exceeds window 168h0m0s)` |
| `proposed_sunset` | proposed sunset date (declaration + 90 days) |
| `brownout_windows` | the three proposed windows (indented YAML list items) |
| `fragment_path` | proposal locator — see the warning below |
| `x_seam_deprecated_block` | **the proposed block, fragment-shaped and ready to paste** |
| `body` | the human-readable proposal text |

The counter `seam_retirement_deprecation_candidates_total{route,api_version,spec_version}`
accumulates per route version across runs: a value stuck at 1 means one
detection, a climbing value means the route is still quiet every hour.

> **`fragment_path` is a locator, not a writable target.** It names where the
> proposal applies (`k8s/rs-manager/seam/routes.d/<route>/fragment.yaml` by
> default), but route fragments are not stored as files there: in
> declarative-config each route owner's fragments are JSON entries inside one
> whole ConfigMap manifest (`k8s/rs-manager/seam/configmap-routes-*.yaml`),
> and the evaluator holds no write path to anything. Edit the ConfigMap
> manifest, never the locator path.

## Step 2 — Verify before landing

The evaluator's zero-traffic reading is necessary but not sufficient. Before
touching a fragment:

1. **Confirm the quietness independently** in VictoriaMetrics — query
   `seam_route_version_requests_total{...}` for the route version over the
   reported window. An evaluator cannot distinguish "no callers" from
   "callers not observable"; that judgment is the human part of the handoff.
2. **Check for in-flight callers** — a client mid-migration is exactly the
   case the revert in step 5 exists for. If one is plausible, wait a cycle:
   the counter keeps accumulating.
3. **Re-read the block, not the prose.** The `x_seam_deprecated_block` field
   is the artifact. Its `brownout` key is **singular** — the plural form
   (`brownouts`) parses as an unknown field and the windows would silently
   never fire. The evaluator's output-contract test pins its own emission
   (`tools/seam-retirement-evaluator/output_contract_test.go`); if you edit
   dates by hand, keep the shape.

Dates you may adjust when landing (they are a proposal): `since` should be
the date you actually land the commit, and the brownout windows should keep
their offsets from `since`/`sunset` — ordered, non-overlapping, inside
`[since, sunset]` — because `internal/spec/lint.go` `checkDeprecation`
rejects the block otherwise.

## Step 3 — Land the verdict (the human commit)

1. In `jedarden/declarative-config`, find the route owner's ConfigMap
   manifest under `k8s/rs-manager/seam/` and locate the fragment entry for
   the reported route.
2. Paste the proposed block at the **fragment root** — a sibling of
   `x-seam-schema`, `x-api-version`, `x-upstream` — not inside an operation.
   A fragment-root block covers **every path the fragment declares**; to
   exempt or override one path, place a plain `x-seam-deprecated:` block on
   that path item instead (the path-item form replaces the root default for
   that path only).

   ```yaml
   x-seam-schema: v1
   x-seam-owner: legacy-service
   x-api-version: v1
   x-upstream: https://legacy-service.example.internal
   x-seam-deprecated:
     since: "2026-09-26"
     sunset: "2026-12-25"
     brownout:
       - start: "2026-10-26T11:35:23Z"
         end: "2026-11-02T11:35:23Z"
       - start: "2026-11-25T11:35:23Z"
         end: "2026-12-02T11:35:23Z"
       - start: "2026-12-18T00:00:00Z"
         end: "2026-12-25T00:00:00Z"
   paths:
     /old-route:
       get: ...
   ```

3. **Run the pre-land gate** — the same checks SEAM's own startup performs:

   ```bash
   seam lint <fragment-file> --schema spec/route-fragment-schema.json
   ```

   Exit 0 means the block is schema-valid and deprecation-clean. (The
   fragment-root placement is what the schema sanctions and lint validates;
   the acceptance test
   `internal/server/retirement_handoff_test.go` proves the same shape is
   honored by the runtime.)
4. Commit to `main` and push. ArgoCD syncs the ConfigMap; nothing else to do.

## Step 4 — Observe the hot reload

When the synced ConfigMap updates the mounted fragment file, SEAM's watcher
re-runs load → merge → build → atomic swap. No deployment, no restart, no
dropped connections.

```bash
# reload observed (watch for one "Route table reloaded" line per file change)
kubectl --server=http://traefik-rs-manager:8001 \
  logs -n seam deploy/seam | grep '\[HotReload\]' | tail

# the route now advertises its deprecation
curl -sSi https://<seam-caller>/old-route | grep -iE '^(HTTP|Deprecation|Sunset|Link)'
# Deprecation: since=2026-09-26
# Sunset: 2026-12-25
# Link: </changes>; rel="deprecation", ...

# deprecation inventory
curl -s https://<seam-caller>/changes | jq '.[] | select(.path=="/old-route")'
```

Behavior from here, all pinned by tests in `docs/notes/brownout-runtime-semantics.md`:

- **Outside windows** (including before the first and after the last): the
  route serves normally, every response carries `Deprecation`/`Sunset`/`Link`.
- **Inside a declared brownout window**: structured `410 Gone` with
  `X-SEAM-Brownout: active` — no quota consumed, never cached.
- **Past sunset**: advisory only. The route keeps serving until a human
  removes it; no date on a fragment ever deletes or refuses a route.

## Step 5 — Revert if a caller appears

The verdict travels the same channel back:

1. `git revert` the landing commit in declarative-config (a plain merge-safe
   revert, no force-push), push.
2. ArgoCD syncs; SEAM hot-reloads; the block is gone from the route table on
   the next swap. In-flight requests finish on the old table.
3. Confirm: the `Deprecation` header disappears, `/changes` drops the route,
   `[HotReload] Route table reloaded` appears.

If the revert is urgent, the ArgoCD Application can be force-synced to apply
the reverted manifest sooner — that still *applies the repo*, it does not
bypass it.

## Acceptance evidence

The SEAM-side half of this contract is pinned by
`internal/server/retirement_handoff_test.go`:

- `TestRetirementHandoff_EvaluatorProposalAcceptedByHotReload` — takes the
  block verbatim as the evaluator emits it, lints it, lands it at the
  fragment root, drives the real `Loader.LoadFragments` → `BuildRouteTable`
  → `ThreadSafeTableHolder.Swap` path, and asserts the caller-visible
  headers, the in-window 410, the past-sunset behavior, and that the whole
  acceptance performed no write.
- `TestExtractDeprecation_PropagatedFragmentRootMarker` — pins that the
  fragment-root block (propagated marker) and the path-item block (plain
  key, wins on conflict) both reach `extractDeprecation`.

The evaluator-side half — fragment-shaped emission, one record and counter
per candidate, and no write path — is pinned in
`tools/seam-retirement-evaluator/{output_contract_test.go,write_contract_test.go}`.

```bash
go test -run 'TestRetirementHandoff|TestExtractDeprecation_PropagatedFragmentRootMarker' ./internal/server/
cd tools/seam-retirement-evaluator && go test ./...
```
