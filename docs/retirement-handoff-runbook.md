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
| `quiet_since` | last observed request, RFC 3339 (e.g. `2026-08-29T11:35:23Z`) |
| `eval_window` | `max(3 × observed_max_gap, 7d)` the route exceeded, as a Go duration (e.g. `336h0m0s`) |
| `reason` | the eligibility verdict, e.g. `Zero traffic for 720h0m0s (exceeds window 336h0m0s)` |
| `proposed_sunset` | proposed sunset date, strict ISO `YYYY-MM-DD` (declaration + 90 days) |
| `brownout_windows` | the three proposed windows (indented YAML list items) |
| `fragment_path` | proposal locator — see the warning below |
| `x_seam_deprecated_block` | **the proposed block, fragment-shaped and ready to paste** |
| `body` | the human-readable proposal text |

### The record schema

The schema is **closed**: the envelope zap's production encoder adds
(`level`, `ts`, `caller`, `msg`) plus exactly the eleven fields above, and
nothing else. Every payload field is a required string; there are no optional
or extension fields. The message is always exactly `Deprecation candidate
detected`, and one distinct `(route, api_version, spec_version)` candidate
produces one record per evaluation run. Formats are part of the contract:
`quiet_since` is an RFC 3339 string and `eval_window` a Go duration string —
deliberately not zap's production defaults for those types (epoch seconds and
float seconds), which would put values in front of a human that no one can
read at a `kubectl logs` terminal. The closed set is pinned by
`TestFindingRecordSchemaIsClosed`; the formats by
`TestRunbookRepresentativeRecordMatchesContract`.

**No secret values.** The record — and the counter below — may carry
route-version identifiers (`route`, `api_version`, `spec_version`) and
content derived from those plus wall-clock time (the reason, the fragment
path, the proposed block, the proposal body), and nothing else: no
credential material (the deployment's OpenBao-held query token never enters
the emit path), no HTTP headers, no query-response content beyond the
identity labels, and no other source-metric label value. Even if a
credential-bearing label ever appeared on the source metric, the parser reads
only `route` and `spec_version`, so the value cannot reach a record. That
prohibition is pinned by `TestFindingIgnoresForeignLabelsAndSecretValues`.

A representative record, as `kubectl logs` shows it (the proposed block is
carried in full; the proposal body is elided):

```json
{"level":"info","ts":1790595323.9972212,"caller":"seam-retirement-evaluator/evaluator.go:252","msg":"Deprecation candidate detected","route":"/users","api_version":"_unversioned","spec_version":"abc123","quiet_since":"2026-08-29T11:35:23Z","eval_window":"336h0m0s","reason":"Zero traffic for 720h0m0s (exceeds window 336h0m0s)","proposed_sunset":"2026-12-27","brownout_windows":"    - start: \"2026-10-28T11:35:23Z\"\n      end: \"2026-11-04T11:35:23Z\"\n    - start: \"2026-11-27T11:35:23Z\"\n      end: \"2026-12-04T11:35:23Z\"\n    - start: \"2026-12-20T00:00:00Z\"\n      end: \"2026-12-27T00:00:00Z\"","fragment_path":"k8s/rs-manager/seam/routes.d/users/fragment.yaml","x_seam_deprecated_block":"x-seam-deprecated:\n  since: \"2026-09-28\"\n  sunset: \"2026-12-27\"\n  brownout:\n    - start: \"2026-10-28T11:35:23Z\"\n      end: \"2026-11-04T11:35:23Z\"\n    - start: \"2026-11-27T11:35:23Z\"\n      end: \"2026-12-04T11:35:23Z\"\n    - start: \"2026-12-20T00:00:00Z\"\n      end: \"2026-12-27T00:00:00Z\"","body":"<elided — the human-readable proposal text>"}
```

The example is itself a test fixture:
`TestRunbookRepresentativeRecordMatchesContract` parses it and checks
every key and format against the emitter's real schema, so the doc cannot
silently drift from what the evaluator emits.

### The metric contract

`seam_retirement_deprecation_candidates_total{route,api_version,spec_version}`
is the counter one candidate increments:

- **Type and name**: a Prometheus counter — it only ever goes up.
- **Labels**: exactly `route`, `api_version`, `spec_version` — the same
  identity triple as the record. `route` and `spec_version` come only from
  those two labels in the normalized `seam_route_version_requests_total`
  query result. `api_version` comes from the evaluator's route-version
  identity extractor; until route metadata extraction is implemented it is
  the fixed sentinel `_unversioned`. The evaluator ignores every other source
  label, including caller, tenant, authorization, cookie, password, and token
  labels, so they can neither become dimensions nor leak into values.
- **Value sanitization**: label values are serialized as quoted Prometheus
  text values. Backslashes become `\\`, double quotes become `\"`, and
  newlines become `\n`; this prevents a route or version value from injecting
  another label or sample line. Sanitization is applied only at exposition;
  the identity values are not hashed, truncated, or supplemented with
  untrusted labels.
- **Cardinality**: bounded by construction — one series per distinct route
  version that has ever emitted a candidate, so the series population is
  bounded by the same set of route versions the 14-day traffic query
  observes (`seam_route_version_requests_total`'s population, never larger).
  The label set is closed: no per-caller, per-status, or time-bucketed label
  may be added — an unbounded label would make the metric a liability at
  scrape time. Series accumulate across runs (a value stuck at 1 means one
  detection; a climbing value means the route is still quiet every hour) and
  are never reset by a scrape.
- **Rendering**: text exposition format 0.0.4, series sorted by label
  triple, label values quoted and escaped — repeated scrapes of an unchanged
  registry are byte-identical.

Two companion series report the run itself rather than any candidate:
`seam_retirement_evaluation_runs_total{result="success"|"error"}` and the
gauge `seam_retirement_routes_evaluated` (route versions the most recent run
considered).

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

The operator command below automates the safe preparation boundary: it reads
the structured finding, inserts the proposed block at the fragment root in a
named standalone fragment or ConfigMap `data` entry, runs the real `seam lint`
engine before writing, and can wait for the gateway's hot-reload counter. It
does not commit, push, call kubectl, mutate a live object, or use a credential.
The default is a no-write plan; `--apply` is the explicit local working-tree
write. `fragment_path` from the finding is deliberately not used as a target.

```bash
seam retirement-handoff \
  --finding evaluator-finding.jsonl \
  --target /path/to/declarative-config/k8s/rs-manager/seam/configmap-routes-legacy.yaml \
  --data-key legacy.yaml \
  --schema /path/to/SEAM/spec/route-fragment-schema.json

# After reviewing the diff, apply before committing and pushing:
seam retirement-handoff \
  --finding evaluator-finding.jsonl \
  --target /path/to/declarative-config/k8s/rs-manager/seam/configmap-routes-legacy.yaml \
  --data-key legacy.yaml \
  --schema /path/to/SEAM/spec/route-fragment-schema.json \
  --apply

# After the commit is pushed and ArgoCD has reconciled:
seam retirement-handoff --observe-only \
  --observe-url http://SEAM_OPERATOR_HOST:8081/health/upstreams
```

For a standalone fragment, omit `--data-key` and point `--target` at the
reviewed `owner/fragment.yaml`. The command refuses to overwrite an existing
marker and leaves the target untouched when lint fails. It prints the
ordinary `git revert <landing-commit>` action after an apply; that revert is
the reversible handoff, not a live Kubernetes mutation.

The equivalent manual steps remain useful when the operator wants to inspect
the ConfigMap edit directly:

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
   the acceptance tests in
   `internal/server/retirement_handoff_test.go` prove the same shape is
   honored by the runtime.) A block pasted inside an operation fails this
   gate — `deprecation.wrong-placement` — because the runtime never reads
   one; the plural `brownouts` key fails it too, wherever the block sits.
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
- `TestRetirementHandoff_MisplacedProposalRejectedByPrelandGate` — the
  placement half of step 3: the proposal pasted inside an operation is
  rejected by the pre-land gate (`deprecation.wrong-placement`) even though
  the schema cannot see it, and the runtime shows why — the block is never
  read, so the route stays undeprecated. The plural-`brownouts` trap is
  rejected on a path-item override too (`deprecation.unknown-field`), while
  a well-formed path-item override lints clean and reaches the route table.
- `TestRetirementHandoff_RevertClearsAdvertisedDeprecation` — step 5: the
  revert travels the same hot-reload path back (reload → rebuild → swap),
  and the same clock instant that produced an in-window 410 serves 200 with
  nothing advertised once the block is gone.

The evaluator-side half — fragment-shaped emission, one record and counter
per candidate, and no write path — is pinned in
`tools/seam-retirement-evaluator/{output_contract_test.go,write_contract_test.go,candidate_input_contract_test.go}`.
The last of those pins the input normalization the one-record-one-counter
sentence rests on: a series without both the `route` and `spec_version`
labels is dropped, duplicate series for one route version collapse onto the
first, and an unreadable count is traffic (exactly-zero is the necessary
condition), so a duplicate or malformed series can never manufacture a
candidate or double-emit one.

```bash
go test -run 'TestRetirementHandoff|TestExtractDeprecation_PropagatedFragmentRootMarker' ./internal/server/
go test -run 'TestRetirementHandoff' ./cmd/seam/
cd tools/seam-retirement-evaluator && go test ./...
```

The command tests additionally prove the default plan is read-only, a named
ConfigMap data entry receives the marker at its fragment root, a lint failure
cannot write, the reload counter is observed, and the printed rollback is an
ordinary Git revert. The evaluator write-contract tests remain the separate
proof that proposal generation has no file, process, credential, or git-host
write primitive.
