# seam-ci push trigger: why it never fires

Bead: `seam-bce8a10e` (investigation only — no manifest or wiring changes were
made in this bead). Investigated 2026-09-02 (20:30–21:10 EDT; cluster logs are
UTC, declarative-config commit dates are EDT).

## TL;DR — root cause

**The seam-ci Sensor rejects every event because its data filter uses the
wrong container key: `headers.X-Forgejo-Event` (plural) at
`k8s/iad-ci/argo-events/seam-ci-sensor.yml:40` in `jedarden/declarative-config`,
while the webhook-type EventSource delivers headers under `header`
(singular).** Every real push reaches the cluster, is published to the event
bus, and is then discarded by the filter. Live proof: the sensor pod has logged
72 × `not interested in dependency seam-push (didn't pass filter)` since it
started (2026-09-01T15:11:47Z) and has triggered zero workflows. The last two
rejections — 2026-09-02T15:30:28.264Z and 15:30:30.531Z — are the Forgejo and
GitHub deliveries of commit `7698ba5`, 2 seconds apart.

Every other sensor on the same `forgejo-webhooks` EventSource uses the singular
form and demonstrably works: `zai-proxy-dashboard-sensor.yml:40`,
`aivc-sensor.yml:38`, `seam-lint-declarative-config-sensor.yml:42`.
`zai-proxy-dashboard-build-w8jtg` ran 2026-09-03T00:55:26Z on an identical push
event through the identical route. The plural form belongs to the
**github-type** EventSource (`commitgraph-build-sensor.yml` uses
`headers.X-Github-Event` against `github-webhooks` and works) — the seam-ci
sensor was re-wired from the github EventSource to the forgejo one on 09-01
(commit `4729ee61`) and kept the github-style container; the follow-up filter
fix (`9ed3cec0`) changed the header *name* but not the container.

The fix is one line (`headers.` → `header.`), out of scope here.

## Intended end-state trigger path for a Forgejo push

1. Push to `jedarden/SEAM` `main` on Forgejo (`git.ardenone.com`).
2. Forgejo repo webhook **hook id 13** (active, `push` events) POSTs to
   `https://webhooks-ci.ardenone.com/seam`.
3. Cloudflare tunnel → Traefik IngressRoute `github-webhook-ingressroute`
   (`declarative-config:k8s/iad-ci/argo-events/webhook-ingressroute.yml`,
   `/seam` PathPrefix rule added in `33e0eb1b`) → Service
   `forgejo-webhooks-eventsource-svc:12000`.
4. EventSource `forgejo-webhooks` route `seam`
   (`declarative-config:k8s/iad-ci/argo-events/forgejo-eventsource.yml:70-73`)
   publishes the event to NATS JetStream eventbus `default`.
5. Sensor `seam-ci-sensor` dependency `seam-push`
   (`declarative-config:k8s/iad-ci/argo-events/seam-ci-sensor.yml:34-47`)
   filters on `header.X-Forgejo-Event == push` **(currently misspelled
   `headers.`)** and `body.ref == refs/heads/main`.
6. Trigger submits a Workflow `generateName: seam-ci-` into `argo-workflows`
   from WorkflowTemplate `seam-ci`
   (`declarative-config:k8s/iad-ci/argo-workflows/seam-ci-workflowtemplate.yml`),
   with `revision` = `body.after`.

A second, parallel path exists and is live: the GitHub mirror repo
(`github.com/jedarden/SEAM`, hook id 659043016, same URL) delivers its own copy
of every push. After the filter fix those GitHub copies are rejected by design
(they carry `X-GitHub-Event`, not `X-Forgejo-Event`), so exactly one workflow
runs per push. See "Recommendations" for the cleanup decision this implies.

## Candidate causes — ranked, with evidence

### 1. Sensor filter container key mismatch — CONFIRMED (current silence)

For/why:
- `seam-ci-sensor.yml:40` is the only forgejo-wired sensor using `headers.`;
  the three siblings all use `header.`.
- Sensor pod log (72 rejections, 0 triggers) plus a full event dump logged at
  warn level showing the real payload shape:
  `"header":{"Accept-Encoding":[...],"X-Forgejo-Event":["push"],...},"body":{"ref":"refs/heads/main",...}}`
  — the container is `header`, values are arrays, and both filter targets are
  present and correct apart from the container name.
- Explains the entire window since the sensor rolled (09-01 15:11Z) through
  observation: 8 SEAM pushes on 09-02, all delivered (see candidate 2's
  delivery data), all rejected.
- Against: none. This is not a hypothesis; it is observed rejection.

### 2. Mirror/webhook delivery failure — HIGH for 2026-08-27..30, refuted today

For:
- The GitHub hook (id 659043016, created 2026-07-30T14:58:54Z, never modified,
  active, correct URL) has **exactly 96 retained deliveries, the earliest on
  2026-08-31** (35 on 08-31, 36 on 09-01, 25 on 09-02, all HTTP 200). GitHub
  retains deliveries for 30 days, so deliveries before 08-31 did not happen:
  during 08-27..30 the trigger had **no incoming signal at all** on the GitHub
  path — the Forgejo→GitHub mirror was not producing push events there, so
  nothing could fire even with correct wiring. The mirror
  (`remote_mirror_cCqpi01EfvW`, sync_on_commit, 8h interval) is delivering
  again now; why it stalled in that window is not determinable from available
  surfaces (Forgejo mirror delivery logs are not exposed via its API).
- Against (today): all 96 deliveries are HTTP 200, and the cluster-side logs
  show the events arriving and being published — delivery is healthy now.

### 3. Eventbus/sensor-side silent wedge — MEDIUM, historical (08-27..31)

For:
- The fleet's NATS JetStream eventbus had known "leadership instability",
  fixed 2026-08-30 02:00–02:03 EDT (`5ea8fb38`, `b78090a2` — RAFT config for
  NATS 2.10.10); eventbus pods were rolled ~08-30 06–07Z.
- `jetstream-watchdog` Deployments exist precisely to recover "a wedged
  JetStream subscription" by deleting the sensor pod — deployed **only** for
  `agentscribe-ci` and `needle-ci` (both ~5d old, created during this exact
  era). `seam-ci-sensor` has no watchdog.
- In `argo-workflows`, no webhook-triggered workflow older than
  2026-08-29T20:18Z (`commitgraph-build-x5222`) survives, while a **manual**
  run (`armor-build-manual-9kjjq`, 08-28 20:19Z) does — TTL keeps ≥5 days of
  history, so webhook-triggered CI was broadly dark before ~08-29.
- On 08-31 the GitHub path delivered 35 × 200 into a then-correctly-wired
  sensor (old config: github EventSource, `headers.X-Github-Event`) and still
  no workflow ran — consistent with a wedged sensor subscription (or residual
  bus instability), not with wiring or delivery problems.
- Against/limits: the old sensor pod was replaced 09-01 15:11Z and the old
  eventsource pod 08-30, so the logs that would attribute individual 08-31
  events are gone. This layer is inference from corroborating signals, not
  observation.

### 4. Wiring removed/regressed in declarative-config — REFUTED

All pieces exist in git and in the live cluster: WorkflowTemplate `seam-ci`
(live; current object recreated 2026-08-26T19:59:46Z), Sensor `seam-ci-sensor`
(pod running), `seam` endpoint in `forgejo-eventsource.yml` and in
`github-eventsource.yml:1066-1075`, `/seam` IngressRoute rule, both
eventsource Services with live endpoints (18d). The 09-01 re-wiring introduced
a *defect*, it did not remove wiring.

### 5. Webhook or secret rotted — REFUTED (with a hardening note)

GitHub hook: created 07-30, `updated_at` identical (never touched), active,
200 on every delivery. Forgejo hook id 13: active, push events, correct URL.
Hardening note: both event payloads arrive with **empty signatures**
(`X-Forgejo-Signature: ""`, `X-Hub-Signature: "sha1="` / `"sha256="` with empty
digests) — no signing secret is configured on either hook, so any client that
can reach `webhooks-ci.ardenone.com/seam` can forge a push event. Not a cause
of the silence; worth fixing separately.

### 6. Sensor submits workflows whose names lack "seam" — REFUTED

`seam-ci-sensor.yml:58` submits `generateName: seam-ci-` into
`argo-workflows`; the template is named `seam-ci`. Zero workflows with `seam`
in the name exist because zero were ever submitted in the observed windows.

### 7. drawrace-style silent route non-registration — REFUTED for the current state

Both EventSource routes are registered and dispatching: the `seam` route on
`forgejo-webhooks` published events (e.g. eventID `0f50ae9e…` at
2026-09-02T15:30:28Z), and the `seam` route on `github-webhooks` dispatched
repeatedly on 09-01 (13:50, 14:09, 14:35Z) before the new `/seam` ingress rule
pulled the traffic onto the forgejo service. Whether a registration failure
contributed *before* 08-30 cannot be checked (pods replaced), but nothing
requires that hypothesis.

## Timeline (UTC unless noted)

| When | Event |
|---|---|
| 07-16 | SEAM repos created on Forgejo and GitHub |
| 07-30 14:51Z | `a2b4f6fd` — seam-ci WorkflowTemplate + Sensor wired to `github-webhooks` (filter `headers.X-Github-Event`, correct for that source); GitHub hook created 14:58:54Z |
| 08-14, 08-16 | Last proven trigger successes: `workflow-seam-ci-rgvzp` (08-14 08:37Z), `workflow-seam-ci-z6cdg` (08-16 13:02Z, phase=Failed — the gate was already red, but it **ran**; artifacts in SEAM repo root) |
| 08-21 | `f8cff48d` — SEAM CI clones/pushes from Forgejo, not the GitHub mirror (Forgejo-primary era) |
| 08-26 19:59Z | Live seam-ci WorkflowTemplate (re)created |
| 08-27..31 | 25+ pushes to Forgejo `main`; zero seam-ci runs; zero GitHub deliveries before 08-31; fleet JetStream instability until the 08-30 RAFT fix |
| 08-31 | Mirror delivers again: 35 × 200 GitHub deliveries — still no run (sensor-side wedge, candidate 3) |
| 09-01 13:52Z | `4729ee61` — sensor re-wired to `forgejo-webhooks`; `seam` endpoint added to `forgejo-eventsource.yml`; github-style filter kept |
| 09-01 14:28Z | `33e0eb1b` — `/seam` IngressRoute rule → forgejo service |
| 09-01 15:02Z | `9ed3cec0` — header name fixed to `X-Forgejo-Event`, plural `headers.` kept |
| 09-01 15:11Z | Current sensor pod starts (config = git HEAD, with the defect) |
| 09-02 | 8 pushes, 25 GitHub deliveries + Forgejo deliveries, **72 filter rejections, 0 workflows** |

## Live cluster observations (read-only, `kubectl --server http://traefik-iad-ci:8001`)

- `seam-ci-sensor-…-wrtdv` Running, started 2026-09-01T15:11:47Z; 72 ×
  "didn't pass filter", 0 trigger lines.
- `forgejo-webhooks-eventsource-…` Running ~34h; `seam` route registered and
  publishing; dispatches observed for `declarative-config`,
  `zai-proxy-dashboard`, `ai-code-battle`, `seam`.
- `github-webhooks-eventsource-…` Running 3d23h; `seam` route dispatched
  through 09-01 14:35Z.
- Services `forgejo-webhooks-eventsource-svc` / `github-webhooks-eventsource-svc`
  both have endpoints (18d old).
- WorkflowTemplate `seam-ci` present in `argo-workflows`.
- `argo-workflows` holds 39 workflows (oldest 08-28 20:19Z); none named
  `seam*`. Forgejo-triggered CI for other repos ran throughout observation
  (`zai-proxy-dashboard-build-w8jtg`, `declarative-config-post-push-validate-*`,
  `armor-build-*`, `bead-rs-ci-*`, `commitgraph-build-*`).
- Note: the read-only observer SA cannot list EventSource/Sensor CRs in
  `argo-events` (Forbidden) — the CR-level evidence here comes from pod logs,
  which do show the CRs' live behavior.

## Recommendations (for a follow-up fix bead — intentionally not done here)

1. **Fix** `seam-ci-sensor.yml:40`: `headers.X-Forgejo-Event` →
   `header.X-Forgejo-Event` (mirror `zai-proxy-dashboard-sensor.yml:40`), then
   roll the sensor pod.
2. Verify end-to-end with a doc-only push: eventsource publishes `seam` →
   sensor logs acceptance → `seam-ci-*` workflow appears and passes.
3. Decide the fate of GitHub hook 659043016: post-fix its deliveries on `/seam`
   are filtered out (no `X-Forgejo-Event` header) and are harmless duplicates;
   either delete it or keep it as a dormant fallback. Do **not** point it at
   the github EventSource route without also reverting the sensor.
4. Deploy a `jetstream-watchdog` for `seam-ci-sensor` (pattern exists for
   `agentscribe-ci` and `needle-ci`) — candidate 3 showed this sensor class
   fails silently.
5. Configure signing secrets on both hooks and validation on the EventSource
   (signatures are currently empty/unvalidated).

## Outcome (2026-09-03, bead `seam-0e3fbddf`)

Recommendation 1 plus the candidate-5 hardening landed in
`jedarden/declarative-config` `86e44bb9`: the sensor filter reads
`header.X-Forgejo-Event` (singular), and the `seam` EventSource route now
enforces `authSecret` (`forgejo-webhook-secret`, fed by the
`forgejo-webhook-externalsecret.yml` ExternalSecret from `c547ca12` —
OpenBao `rs-manager/iad-ci/forgejo/webhook-secret`). A
`webhook-registration-retry` pod-template annotation bump forces the
eventsource pod to roll on sync (guard against the silent
non-registration failure mode, candidate 7). Forgejo hook id 13 was
re-registered to send `Authorization: Bearer <webhook-secret>` and to
sign deliveries with the same secret; the value moved OpenBao → env →
jq `env` builtin → curl stdin and never passed through argv or a
transcript.

Recommendation 3 resolved by inaction: GitHub hook 659043016 stays as a
dormant fallback — post-fix its deliveries carry no `X-Forgejo-Event`
header and are filtered out by design. Recommendations 4 and the
HMAC-validation half of 5 remain open follow-ups (argo-events v1.9.10
validates only the Bearer token, not `X-Forgejo-Signature`).

**Blocked end-to-end**: cluster-side verification (Synced/Healthy apps,
eventsource pod roll, workflow fire) could not complete — a fleet-wide
GitOps outage began 2026-09-02T23:36Z when ArgoCD's
`declarative-config-repo` GitHub credential died, so `86e44bb9` and
everything after it is pushed but unsynced. Diagnosis, restoration
recipe (operator-bound), and post-restore verification steps:
`declarative-config:docs/argocd-repo-credential-outage-2026-09-02.md`,
tracked as `declarat-e1c7de39`.

## End-to-end re-verification attempt (2026-09-03 ~08:05Z, bead `seam-d20b2887`)

The trigger did **not** fire. The fix is still not live in-cluster, and a
second blocker surfaced on the Forgejo side. All evidence read-only
(`kubectl --server http://traefik-iad-ci:8001`, Forgejo API).

- The `495359a` docs-only push (06:58:31Z) was delivered by hook 13 at
  06:58:36Z (delivery `bd391c00-3e53-443f-807e-025e89275770`), published to
  the bus as eventID `13c1ae2c76034785a386426f7251d293`, then rejected by
  the sensor: `not interested in dependency seam-push (didn't pass filter)`.
  A second delivery 12 s later (06:58:48Z, eventID
  `a4029b9243394f3ab1f95516648911e8` — the GitHub mirror copy, hook
  659043016) was rejected identically. Zero `seam-ci-*` workflows exist in
  `argo-workflows`.
- `seam-ci-sensor-…-wrtdv` (started 2026-09-01T15:11:47Z) and
  `forgejo-webhooks-eventsource-…-xtpr4` (13:54:28Z) are the *same pods* the
  investigation above observed — neither rolled, so `86e44bb9` (filter fix,
  `authSecret`, forced eventsource roll) is confirmed unsynced and the
  `declarat-e1c7de39` outage is still in effect.
- **New blocker — hook 13 is not sending auth or a signature.** Forgejo API
  `GET /repos/jedarden/SEAM/hooks/13` returns `authorization_header: ""` and
  no secret, and the 06:58:36Z delivery carried `X-Forgejo-Signature: ""`
  with no `Authorization` header at all. The "re-registered with auth +
  signing" recorded above is **not in effect** on the live hook. Consequence:
  once `86e44bb9` syncs, its `authSecret` will reject hook 13's deliveries
  with 401 and the trigger stays silent — hook 13 must be re-registered with
  the OpenBao secret *before* (or at the same time as) the sync. (GitHub hook
  659043016 *does* have a secret configured; its deliveries carry no
  `X-Forgejo-Event` and are filtered out by design either way.)

## Post-outage re-verification (2026-09-03 ~12:25Z, bead `seam-d20b2887`)

The GitOps outage is over and the entire trigger chain is now live — but the
last hop (workflow submission) fails on a *new, independent* defect: the
iad-ci WorkflowTemplate CRD silently prunes `templateRef`. Evidence, all
read-only:

- `argo-events-ns-iad-ci` synced + healthy (first post-outage sync `0b081fe8`
  11:03:01Z, tracking `bdbab8b08`); the `86e44bb9`/`c547ca12` fix is live:
  eventsource pod rolled 11:05:53Z, sensor pod 11:12:56Z, ExternalSecret
  `forgejo-webhook-secret` SecretSynced.
- Hook 13 was re-registered at 11:49:15Z — `authorization_header` now set
  (Bearer; secret referenced only by its OpenBao path
  `rs-manager/iad-ci/forgejo/webhook-secret`). No HMAC secret, which matches
  the known argo-events v1.9.10 limitation (it validates Bearer but not
  `X-Forgejo-Signature`).
- The 11:49:15.332Z delivery traversed the whole chain: `/seam` accepted it
  (no 401), published eventID `9401ef925b3d4a018c08a27b6ecef538`, and the
  sensor **passed** the `seam-push` filter (`header.X-Forgejo-Event` +
  `body.ref refs/heads/main` — the `86e44bb9` fix works) and attempted the
  `seam-ci` trigger.
- **Submission rejected by argo-server:** `templates.pipeline.steps[0].post-pending
  template 'github-commit-status' type is unknown`. Root cause: the installed
  `workflowtemplates.argoproj.io` CRD (created 2026-04-04) has no `templateRef`
  property in its Template schema, so the API server silently prunes that
  field on every apply — the live seam-ci template matches git HEAD except
  for exactly the pruned `templateRef` (and the pipeline's `onExit` handler).
  This also explains `argo-workflows-ns-iad-ci` being permanently OutOfSync:
  apply → pruned → live never matches git → auto-sync retries forever.
- Blast radius beyond seam-ci: `clasp-build`, `node-verify` and `rust-verify`
  all have the same `post-pending → github-commit-status` step and the same
  pruned body. The namespace currently holds zero workflow objects from any
  of the four templates while non-`templateRef` templates run normally
  (needle-ci ×19, spaxel-*, mta-my-way-build) — consistent with submit-time
  rejection. `rust-verify` being broken means fleet `cargo test` offloading
  is likely silently falling back to local runs.

The docs-only commit carrying this section is the e2e probe push; its
delivery, filter and submission outcomes are recorded on bead
`seam-d20b2887`. The fix belongs to the CRD (cluster install), not to the
seam-ci manifest — tracked as `seam-9a3d5f53` (P1, in progress).

## Probe outcomes, confirmed live (2026-09-03 ~12:30Z, bead `seam-d20b2887`)

The `a34bcc1` docs-only push (delivered 12:23:09Z) proves every hop of the
trigger chain and pins the failure to exactly one point. All evidence
read-only (`kubectl --server http://traefik-iad-ci:8001`, Forgejo/GitHub
hook APIs):

1. **Delivery + auth — PASS.** Hook 13's 12:23:09.293Z POST was accepted by
   `/seam` and published (eventID `b1574250f35f4fb180eedbcdfb3f9704`) —
   Bearer auth validated. (`authorization_header` is a top-level field of
   Forgejo's `GET /hooks/13` response and is set; it is not under `.config`,
   which only surfaces `content_type` and `url`.)
2. **Sensor filter — PASS.** The sensor logged
   `Triggering actions after receiving dependency seam-push` for that
   eventID: the `86e44bb9` filter fix (`header.X-Forgejo-Event` +
   `body.ref == refs/heads/main`) demonstrably accepts a real push. The
   trigger is wired correctly end to end.
3. **Submission — REJECTED (the CRD defect, deterministic).** 12:23:09.587Z:
   `Failed to submit workflow: rpc error: code = InvalidArgument desc =
   templates.pipeline.steps[0].post-pending template 'github-commit-status'
   type is unknown`. Identical rejection for the 11:49:15Z delivery — two
   for two. argo-server rejects at validation, so **no `seam-ci-*` workflow
   object is created and nothing appears in argo-workflows**.
4. **Mirror copy — no duplicate, stopped one layer earlier than designed.**
   GitHub hook 659043016 delivered its copy of the same push at
   12:23:14.012Z and got **HTTP 401** (`invalid auth header` logged at the
   eventsource 12:23:13.365Z): GitHub cannot send the `Authorization: Bearer`
   header the route now requires, so the mirror delivery is rejected at the
   auth layer and never published — the sensor's `X-Forgejo-Event` filter is
   never even consulted. The earlier "filtered out by design" mechanism note
   described the pre-`authSecret` state (all mirror deliveries before the
   11:05Z eventsource roll returned 200 and were filter-rejected); the
   design outcome is unchanged and now stronger: **exactly one event per
   push reaches the bus, from Forgejo alone.**
5. **Template unchanged — verified by deep diff.** Live-vs-git (`0d3ccc09`
   manifest): the `ci` template carrying the actual gates (gofmt → go vet →
   golangci-lint v2.12.2 → `go test -race` → seam lint → benchmark gate with
   the 20% benchstat threshold) plus `image-build`, `resolve-release` and
   `github-release` are **byte-identical**. The only deltas are the CRD
   defect itself: step 0's `templateRef {github-commit-status: post}` (live
   keeps a mangled `template: github-commit-status` string resolving to an
   in-spec husk `{"name": "github-commit-status"}` with no body), the pruned
   pipeline `onExit`, and the husk template git doesn't define. Nobody
   modified the template to route around the defect.

State of acceptance for `seam-d20b2887`: "fired" is proven through
filter-pass and submission attempt (once per push, zero duplicates), but
criterion 1 — one observable `seam-ci-*` workflow — waits on `seam-9a3d5f53`
replacing the CRD. Post-fix verification is mechanical: confirm
`argo-workflows-ns-iad-ci` is Synced, push one docs-only commit, expect
exactly one accepted `/seam` delivery → one published eventID → one filter
pass → one `seam-ci-*` workflow (mirror copy 401s as in item 4).
