# seam-ci before/after: resource and timing metrics

Recorded 2026-09-24 by bead seam-0aa2e8a7 (split-child of seam-ded3791e), from live-run
evidence banked on the seam-ded3791e chain beads plus the 2026-09-20 monolith probe
numbers embedded in the `seam-ci` WorkflowTemplate comments
(`declarative-config/k8s/iad-ci/argo-workflows/seam-ci-workflowtemplate.yml`).

- **BEFORE** = the monolith template (single 2-CPU verify pod, all checks sequential),
  in force through 2026-09-21.
- **AFTER** = the split template as live in iad-ci on 2026-09-24 (WorkflowTemplate
  `seam-ci` resourceVersion 86976139: five parallel lanes, 8dcfd720 sizing + f1da70e7
  ephemeral-storage/anti-affinity, on top of 8b4f6b06/b325ae2c resilience fixes).

SEAM revision for every cited run: `382bc1b` (= origin/main throughout the window).

## 1. What changed

| | BEFORE (monolith) | AFTER (split, live RV 86976139) |
|---|---|---|
| verify shape | 1 pod, checks sequential | 5 parallel lane pods, one hard join before release |
| verify requests | cpu 2000m / mem 2Gi | per lane: fmt-vet 400m/512Mi, lint 500m/768Mi, corpus 400m/512Mi, race-test 600m/3Gi, seam-lint 300m/384Mi |
| verify limits | cpu 3500m / mem 4Gi | per lane cpu 2x request; mem 6Gi all lanes (LimitRange cap) |
| ephemeral-storage | no request | per lane requests: fmt-vet 3Gi, lint 4Gi, corpus 3Gi, race-test 5Gi, seam-lint 2Gi; no limits |
| lane deadline | 1800s for the whole verify stage | fmt-vet/lint/race-test/seam-lint 3600s, corpus 1800s; workflow cap 7200s unchanged |
| schedulable nodes | compute1-8 only (2000m > compute1-4's 1500m allocatable) | all nodes except race-test (3Gi > compute1-4's 2.54GiB) |
| clone | full history (~15s measured) | pinned shallow one-commit (~3s) |
| five-lane quota hold | 2000m/2Gi = 57%/12.5% of the then-3500m/… quota in ONE pod | 2200m/5.125Gi total = 27.5%/32% of the 8-CPU/16Gi quota, spread over 5 pods |
| GOMAXPROCS | unset (host-sized) | pinned to 2 in every lane |

Live template verified 2026-09-24 against the running object (credential-free iad-ci
kubectl, RV 86976139): all five lanes carry the exact requests/limits/deadlines above
plus one podAntiAffinity term each.

## 2. Queue time and wall time (all times UTC)

| run ID | date | template | queue (submit -> first pod work) | wall (start -> terminal) | outcome |
|---|---|---|---|---|---|
| seam-ci-before-probe-xkt9p | 09-20 | monolith | **21 of its 43 wall minutes requeued by quota pressure before it ever started** | 43 min total; 21m12s of verify compute | probe (no GH status post) |
| (unnamed, GH status) | 09-19 | monolith | mixed with wall, not separable | pending 13:52:17 -> failure 14:50:25 = 58m08s | Failed |
| seam-ci-manual-vz62c | 09-21 | monolith | post-pending alone sat 9030s against its 200s deadline; epilogue quota-blocked 27 min | pending 13:19:27 -> failure post 20:28:17 = **7h08m50s**; root Failed 20:01:09Z | Failed, verify lanes never created |
| seam-ci-manual-glrjq | 09-22 | split, pre-sizing | < 1 min | lanes OOMKilled ~3s in | Failed (old 1Gi-2Gi limits) |
| seam-ci-manual-x6drm | 09-22 | split, pre-sizing | ~28 min (post-pending quota-Forbidden 12:15:35, ran 12:44) | 12:15:24 -> 13:45:04 = 89m40s | Failed: corpus OOM 5m31s, race OOM 17m59s, three lanes hit the 1800s deadline |
| seam-ci-live-verification-47trx | 09-23 | split, sized (RV 86639777) | 13:25 submit -> 13:26:49 start = **1m49s** | 13:26:49 -> 14:02:43 = 35m54s | 4/5 lanes Succeeded; race-test evicted 4s |
| seam-ci-live-verification-gl7jh | 09-23 | split, +ephemeral/affinity | - | pending 15:12:11 -> success 16:11:35 = 59m24s | first `success` status post, but the Workflow object was lost in the 09-23 API degradation - corroborating only |
| **seam-ci-live-verification-sghzr** | 09-24 | split, live RV 86976139 | 00:09 submit -> 00:10:24 start = **~1m24s** | 00:10:24 -> success post 01:44:06 = **93m42s** end-to-end (incl. image-build + github-release: v0.1.4 publishedAt 01:30:46) | **all five lanes Succeeded - first green seam-ci run ever** |

**Queue time verdict:** the operational queue win is real and large where it was hurting:
21 min (probe) and hours (vz62c, wedge/quota-dominated) under the monolith vs ~1.5 min for
the sized split template when the controller is healthy (sghzr, 47trx). x6drm's 28 min
head-stage stall shows queue time is still hostage to co-tenant quota pressure and
controller health - the split removed the monolith's self-inflicted quota monopolization
(57% of the namespace CPU budget in one pod), not cluster-level congestion.

**Wall time verdict (honest):** the split did NOT shorten verify compute wall. The
monolith's sequential verify stage measured 21m12s (probe); the parallel lane group's
slowest lane measured 27m05s (47trx) and race-test ~50 min (sghzr), because each lane
pays its own cold module compile at 300-600m requests (the duplicate-compile trade the
template header documents). What the split bought: schedulability, compute1-4 usability,
failure aggregation (the monolith aborted at the first failing check), per-lane diagnosis,
and - as of sghzr - an actually-green pipeline. No monolith run ever reached Succeeded.

## 3. Per-check / per-lane peak RSS and CPU, including Argo executor overhead

**BEFORE - measured live in the probe pod** (`~/.needle/seam-ded3791e/before-probe-main.ndjson`,
cgroup `memory.peak` + `cpu.stat` of the verify pod = main + Argo wait/executor containers
together, GOMAXPROCS=2, cold caches, 2-CPU request / 3.5-CPU limit shape):

| check (sequential) | wall (s) | delta CPU (core-s) | avg effective cores | cumulative pod peak memory (MiB) |
|---|---|---|---|---|
| apt + full clone + gofmt | 21 | <= 14.4 | - | 336.7 |
| go vet ./... | 338 | 953.4 | 2.82 | 2784.2 |
| golangci-lint | 216 | 529.1 | 2.45 | 2981.9 |
| corpus capture x5 | 27 | 65.6 | 2.43 | 2981.9 |
| go test -race ./... | 605 | 1244.1 | 2.06 | **3529.2** |
| seam lint | 35 | 33.6 | 0.96 | 3529.2 |
| bench gate (benchstat) | 30 | 50.4 | 1.68 | 3529.2 |
| **whole verify stage** | **1272 (21m12s)** | **2890.7** | **2.27 avg** | **3529.2 peak (3.45 GiB)** |

`memory.peak` is cgroup memory (RSS + page cache; cache is reclaimable, so these are upper
bounds on working set).

**AFTER - what is measured vs what is not (flagged):**

- **Peak memory per lane: < 6Gi limit, live-verified.** No container hit its memory limit
  in 47trx or sghzr (no OOMKilled anywhere in either run; all four non-race lanes exited 0;
  race-test completed at the 6Gi limit in sghzr - the first completed race lane ever,
  after 2Gi demonstrably killed it). The 6Gi value is the LimitRange cap and the "only
  demonstrated-passing value" from the 09-22 probe wave - it is a ceiling, not a measured peak.
- **Peak CPU per lane: NOT captured.** The after-era lane pods were GCd and no cgroup peak
  or metrics-server scrape was read before deletion. Per-lane wall is the proxy:
  fmt-vet 20m54s, corpus 20m58s, lint 24m48s, seam-lint 27m05s (47trx, measured),
  race-test ~50 min (sghzr, measured). Every lane's wall includes its Argo wait container
  (each observed `wait:Completed/exit=0`), but executor CPU/RSS was not separately metered.
- **Peak ephemeral-storage, race-test: 4.44GiB measured** (kubelet eviction capture, 47trx:
  main at 4650948Ki) -> the 5Gi request. The other four lanes' requests (3/4/3/2Gi) are
  conservative, not measured - see flag 2 below.
- Duplicated-compile cost: the split re-pays the module-graph compile five times
  (pod-local GOCACHE, no shared cache possible: no RWX StorageClass, no artifact repository,
  per-run PVCs banned). The monolith paid it once at 2.27 avg cores.

## 4. Pod-hours and node-hours

Derived from the node maps and status posts above; sums marked "derived" borrow measured
walls across runs of the same commit/template and carry a ~±0.3 pod-h uncertainty.

| run | pod-hours | node-hours | split across nodes |
|---|---|---|---|
| xkt9p (monolith probe) | 0.72 (43 min x 1 pod; 0.35 executing + 0.35 quota-pending) | ~0.35 | 100% compute1-8 (only schedulable tier) |
| vz62c (monolith, failed) | **~3.1** (post-pending 9030s = 2.51 + resolve-revision 2x269s + epilogue 27 min pending) | ~3.1, nearly all Pending/idle | compute1-8/io1-60; verified nothing |
| 47trx (split, 4/5 green) | 1.61 measured (post-pending 13s + resolve-revision 38s + lanes 93m49s + epilogue 116s) | 1.61, all on io1-60 | 5 lanes co-tenant on ONE node (prod-instance-…60849) |
| **sghzr (split, five-green)** | **~2.9 derived** (head ~0.01 + lanes ~2.43 + release tail ~0.45 + epilogue ~0.02) | **~2.5-2.9 derived** | ~0.8 on two compute1-4 nodes, ~1.2 on compute1-8, ~0.5-0.9 on io1-60 |

Per productive unit: the split's green run costs ~2.9 pod-h vs the monolith's 0.72 pod-h
probe run (duplicated compiles + executor-per-pod), but the monolith produced **zero**
green runs ever, and one of its failed runs (vz62c) burned ~3.1 pod-h almost entirely
Pending. Pod-hours per *green* pipeline: monolith undefined, split ~2.9.

## 5. compute1-4 vs compute1-8 placement

Live node inventory (iad-ci, read 2026-09-24): 3x compute1-4 (1.5 CPU / 2.54GiB / 91.3GiB
allocatable each), 3x compute1-8 (3.5 CPU / 6.16GiB / 91.3GiB), 1x io1-60 (15.5 CPU /
57.7GiB / 35.1GiB - the small-disk node).

| run | lanes on compute1-4 | lanes on compute1-8 | notes |
|---|---|---|---|
| monolith era (xkt9p, vz62c) | **0 - impossible** (2000m request > 1500m allocatable) | all | three compute1-4 nodes permanently unusable for SEAM CI |
| x6drm (pre-sizing) | 1 (race-test, OOMKilled there at 17m59s) | 4 (fmt-vet, lint, corpus, seam-lint) | io1-60 hosted no lanes |
| 47trx (sized) | 0 | 0 (+5 on io1-60) | scheduler stacked all five lanes on the 35GiB-disk node - the eviction incident |
| **sghzr (live, five-green)** | **2** (corpus -> prod-instance-17854684761460728, seam-lint -> prod-instance-17854391563500672) | **2** (fmt-vet -> …71120751201, lint -> …71042571200) + race-test on the compute1-8 tier or io1-60 (node not captured before TTL reap; compute1-4 is infeasible for it) | anti-affinity spread worked: four lanes on four distinct nodes, eviction node unused by the recorded lanes |

## 6. Explicit verdicts required by seam-ded3791e / seam-0aa2e8a7

**compute1-4 fit: ACHIEVED where real usage permits.** Four of five lanes (fmt-vet, lint,
corpus, seam-lint) fit compute1-4 on every request dimension, and sghzr live-placed two of
them there. race-test deliberately does NOT fit - its memory request was RAISED 1Gi -> 3Gi
on evidence (OOMKilled at 2Gi in ~3s in glrjq; OOMKilled at 17m59s on the smallest node in
x6drm; alive-but-unfinished at ~2h at 2Gi in kz9vr), which makes compute1-4 (2.54GiB
allocatable) infeasible for it by construction.

**Requests lowered without usage evidence: NONE.** No request was lowered anywhere in the
chain - four memory requests are unchanged from 09-20, CPU requests are unchanged
throughout, and race-test's was raised. Flags in the opposite direction, for the record:

1. **The 09-20 split set lane LIMITS below the same day's measured pod peaks** (fmt-vet
   limit 1Gi vs 2.78GiB measured pod peak through vet; lint 1536Mi vs 2.98GiB; race-test
   2Gi vs 3.53GiB). That was fit-to-small-nodes over measurement, and the 09-22 OOM chain
   (glrjq: all five lanes OOMKilled in ~3s; x6drm: corpus + race OOMKilled) was the direct
   cost. Corrected 09-23 (declarative-config 8dcfd720): 6Gi limits, verified live.
   Caveat kept honest: memory.peak includes reclaimable page cache, so peak-with-cache
   overstates the working set - but the observed OOMs prove the working set did exceed
   those limits.
2. **Four of five ephemeral-storage requests (3/4/3/2Gi) were added without per-lane
   measurements** (the pods were already GCd when sized; template comment says so
   explicitly). race-test's 5Gi is the only measured one. They are conservative additions,
   not lowerings, and carry no limits precisely because the evidence is thin.
3. **The 6Gi memory limits are the LimitRange cap, not a usage number** (8Gi Error'd
   against the cap in the 09-22 probe wave). If a lane ever needs more, memory is a closed
   lever fleet-wide and the next lever is workload shaping (seam-3f492639).

## 7. Sources

- Monolith probe: `~/.needle/seam-ded3791e/before-probe-main.ndjson` (PROBE_PHASE /
  PROBE_RSS lines, pod `seam-ci-before-probe-xkt9p`, 2026-09-20); template header and
  per-lane comments, `declarative-config/k8s/iad-ci/argo-workflows/seam-ci-workflowtemplate.yml`;
  monolith resource shape from the pre-split template (banked at
  `~/.needle/seam-b079c86c/old-template.yml`: verify 2000m/2Gi req, 3500m/4Gi lim, 1800s).
- Run evidence: seam-ci-manual-vz62c (parent seam-ded3791e notes + `~/.needle/seam-b079c86c/wf-manual-vz62c.json`);
  glrjq / kz9vr / x6drm node maps (seam-2b61959f + seam-49166775 notes,
  `~/.needle/seam-49166775-live/`); 47trx terminal node map (seam-7025161a notes,
  watcher log `~/.needle/seam-7025161a/watch-47trx.log`); sghzr terminal node map
  (seam-4ebaa652 rev 10); sizing table and probe wave (seam-3f492639); land + live verify
  (seam-72428019 commit 8dcfd720, seam-743406c8 live structural diff, RV 85978819 ->
  86639777); eviction fix (seam-4ebaa652 commit f1da70e7, RV 86976139).
- Timestamps: GitHub commit statuses and release for
  `jedarden/SEAM@382bc1b` (pending/failure/success posts, v0.1.4 publishedAt 2026-09-24T01:30:46Z);
  live WorkflowTemplate `seam-ci` RV 86976139 and node inventory via credential-free
  `kubectl --server=http://traefik-iad-ci:8001` (read-only), 2026-09-24.

Note on clocks: the iad-ci controller pod clock ran up to ~4 min ahead of codinghome
during this window; cross-source times carry that skew.
