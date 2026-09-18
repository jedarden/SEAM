# SEAM Phase-to-Checkbox Mapping Document

**Version:** 2.0
**Generated:** 2026-09-18
**Task:** seam-d7aac6fe (final consolidation)
**Draft source:** `.beads/evidence-checkbox-draft.md` (seam-9c80a071, closed 2026-09-18)
**Anchor commit:** `be68251` (= `origin/main` tip at generation time; every anchor below re-verified live against `docs/plan/plan.md` at this commit)
**Supersedes:** v1.0 (2026-09-02, seam-f3e0a9dd), which predates the 2026-09-01 verdict incorporation, the 2026-09-18 checkbox extraction supersession, and the Phase 6a ACL resolution
**Purpose:** The canonical evidence-to-checkbox mapping for all 17 `docs/plan/plan.md` Implementation-Phases checkboxes — mapping table, per-phase rationale, and change recommendations in one reference.

---

## Executive Summary

**Result: zero discrepancies.** The expected checkbox state derived from the 2026-09-01 verdict data (seam-7e2bcb06) equals the actual state in `docs/plan/plan.md` for **all 17 phases**. No checkbox in plan.md requires a change on this evidence, and this bead modified none.

- **Join:** phase identity — `phase<ID>-evidence.md` ↔ the unique `Phase <ID>:` checkbox line. 1:1 across all 17 checkboxes and all 13 evidence files, plus 4 explicit absence rows. Corroborated two ways: each evidence file's own H1 names its phase (13/13 match its filename), and 13 of the 17 checkbox texts cite exactly the mapped `.beads/phase*-evidence.md` path — the citation set and the evidence-file set are bijective.
- **State partition (6 / 7 / 4):** 6 verified complete and ticked `[x]` (6a, 8, 9b, 11, 13, 14); 7 incomplete and unticked `[ ]` (3, 4, 5, 6b, 7, 10, 12); 4 with no evidence and unticked `[ ]` (1a, 1b, 2, 9a).
- **Fourth leg:** plan.md's own `**Status:**` header (line 5) states the identical partition, quoted from the same 2026-09-01 verification pass.
- **What changed since v1.0 (2026-09-02):** verdict data is now incorporated directly (v1.0 only cross-referenced it); `.beads/plan-checkbox-state.md` was superseded on 2026-09-18 by a pure extraction (seam-7fdee15d); Phase 6a's two pending ACL criteria were resolved post-verdict by the `tag:seam` tailnet slice (applied and verified 2026-09-18, commit `aeefe43`); and v1.0's stale "Critical Disconnect" key finding — which described six wrongly-ticked checkboxes as still checked, contradicting v1.0's own alignment table — is corrected here (see §10).

---

## 1. Inputs

| Input | File | Version used | Role |
|---|---|---|---|
| Phase inventory | `.beads/phase-inventory.md` | 2026-09-02 (13 files, 4 missing: 1a, 1b, 2, 9a) | Authoritative for which evidence files exist |
| Checkbox states | `docs/plan/plan.md` @ `be68251` (grep `- [x]`/`- [ ] Phase`, lines 868–962) | Re-verified live 2026-09-18 (17 boxes: 6 ticked, 11 unticked) | Authoritative for current state and line anchors |
| Verdict data | `.beads/phase-verdict-summary.md` + `.beads/evidence-verdict-summary.md` | 2026-09-01 (seam-7e2bcb06) | Authoritative for per-phase PASS/FAIL verdicts |
| Draft mapping | `.beads/evidence-checkbox-draft.md` | 2026-09-18 (seam-9c80a071, committed in `be68251`) | Mapping logic, rationale and caveats, incorporated here |

Note on `.beads/plan-checkbox-state.md`: at anchor `be68251` the committed file at that path is still the 2026-09-02 assessment-style version. The 2026-09-18 extraction that supersedes it exists in the working tree only (seam-7fdee15d's deliverable, not yet committed at generation time). This does not affect the mapping: both files agree on all 17 states because both derive from the same, unchanged plan.md checkbox lines, which this document takes directly from `be68251` as the source of record.

## 2. Mapping logic (rationale for the join)

1. **Phase-ID join (primary rule).** An evidence file named `phase<ID>-evidence.md` maps to the unique checkbox line in `docs/plan/plan.md` whose label begins `Phase <ID>:` within the `Implementation Phases` section. Letter suffixes are atomic identifiers: `6a ≠ 6b`, `9a ≠ 9b`.
2. **Line anchors.** All 17 anchors re-verified live at `be68251` (lines 868–962, strictly increasing; these are the only bracket-pair checkboxes anywhere in plan.md).
3. **Corroboration A — evidence self-title.** Each of the 13 evidence files' H1 names its own phase, and in all 13 cases it matches the phase ID in its filename.
4. **Corroboration B — plan-text citation.** The 13 evidence-backed checkbox texts each cite exactly one `.beads/phase*-evidence.md` path (verified: 13 checkbox lines at `be68251` carry such a citation), and the cited path equals the mapped filename in every case. The 14th annotation (Phase 9a) cites `.beads/phase-verdict-summary.md` instead, because its evidence file does not exist.
5. **Cardinality — 1:1.** Exactly one checkbox per phase; each evidence file covers exactly one phase's completion criteria. Mapping is by phase label, **not** by ship-order position — plan line 800's ship order (`1a → 1b → 9a → 2 → 6a → 3 → 4 → 10 → 5 → 11 → 13 → 6b → 7 → 12 → 8 → 9b → 14`) deliberately differs from document order, so position is not a join key.
6. **Verdict → expected state rule.**
   - `PASS` (including the qualified "substantial pass") → expected `[x]`
   - `FAIL` / `BLOCKED` / `CRITICAL FAILURE` / `CANNOT VERIFY` → expected `[ ]`
   - `NO EVIDENCE` → expected `[ ]` — a checkbox may only be ticked on demonstrated evidence. This is the plan's own house rule: every ticked box carries a dated "COMPLETE — verified …" annotation naming its evidence file, so an unevidenced tick would violate the annotation convention.

The join covers 100% of both sides: all 17 checkboxes and all 13 evidence files, plus 4 explicit absence rows.

## 3. Terminology normalization

The verdict files, the checkbox annotations and the tick state use different vocabularies for the same underlying verdict:

| Verdict (2026-09-01 summaries) | Checkbox annotation label | Expected state |
|---|---|---|
| ✅ PASS (incl. "substantial pass") | `COMPLETE` / `VERIFIED COMPLETE` | `[x]` |
| ❌ FAIL | `INCOMPLETE` | `[ ]` |
| ❌ FAIL (blocked) | `BLOCKED` | `[ ]` |
| ❌ FAIL (crash) | `CRITICAL FAILURE` | `[ ]` |
| ❌ CANNOT VERIFY | `CANNOT VERIFY ACCEPTANCE` | `[ ]` |
| ❌ NO EVIDENCE | *(none; 9a: `NO EVIDENCE`)* | `[ ]` |

## 4. Master mapping table

| Phase | Evidence file | plan.md line | Checkbox state | Verdict (2026-09-01) | In-text annotation | Expected | Agree |
|---|---|---|---|---|---|---|---|
| 1a | *(missing)* | 868 | `[ ]` | NO EVIDENCE | none | `[ ]` | yes |
| 1b | *(missing)* | 881 | `[ ]` | NO EVIDENCE | none | `[ ]` | yes |
| 2 | *(missing)* | 882 | `[ ]` | NO EVIDENCE | none | `[ ]` | yes |
| 3 | `.beads/phase3-evidence.md` | 888 | `[ ]` | FAIL — hot reload not enabled | `INCOMPLETE` | `[ ]` | yes |
| 4 | `.beads/phase4-evidence.md` | 889 | `[ ]` | FAIL — fragments not mounted | `INCOMPLETE` | `[ ]` | yes |
| 5 | `.beads/phase5-evidence.md` | 890 | `[ ]` | FAIL — missing cluster/infrastructure | `INCOMPLETE` | `[ ]` | yes |
| 6a | `.beads/phase6a-evidence.md` | 891 | `[x]` | PASS — 8/10 + 2 pending manual ACL | `COMPLETE` | `[x]` | yes |
| 6b | `.beads/phase6b-evidence.md` | 896 | `[ ]` | FAIL (BLOCKED) — YAML loading | `BLOCKED` | `[ ]` | yes |
| 7 | `.beads/phase7-evidence.md` | 897 | `[ ]` | FAIL — identity placeholder, untested | `INCOMPLETE` | `[ ]` | yes |
| 8 | `.beads/phase8-evidence.md` | 912 | `[x]` | PASS — 7/7 | `VERIFIED COMPLETE` | `[x]` | yes |
| 9a | *(missing)* | 931 | `[ ]` | NO EVIDENCE | `NO EVIDENCE` (cites verdict summary) | `[ ]` | yes |
| 9b | `.beads/phase9b-evidence.md` | 949 | `[x]` | PASS — 2/2 | `COMPLETE` | `[x]` | yes |
| 10 | `.beads/phase10-evidence.md` | 950 | `[ ]` | FAIL (CRITICAL) — startup crash | `CRITICAL FAILURE` | `[ ]` | yes |
| 11 | `.beads/phase11-evidence.md` | 959 | `[x]` | PASS — 6/6 | `VERIFIED COMPLETE` | `[x]` | yes |
| 12 | `.beads/phase12-evidence.md` | 960 | `[ ]` | CANNOT VERIFY — 99 compile errors | `CANNOT VERIFY ACCEPTANCE` | `[ ]` | yes |
| 13 | `.beads/phase13-evidence.md` | 961 | `[x]` | PASS — 3/3 | `VERIFIED COMPLETE` | `[x]` | yes |
| 14 | `.beads/phase14-evidence.md` | 962 | `[x]` | PASS — 4/4, 35 tests | `COMPLETE` | `[x]` | yes |

## 5. Checkbox-by-checkbox view

### Checked `[x]` — 6 phases

| Phase | Line | Evidence status | Verification date | Note |
|-------|------|-----------------|-------------------|------|
| Phase 6a | 891 | ✅ 8/10 criteria pass | 2026-09-01 | 2 pending ACL criteria resolved post-verdict 2026-09-18 (§7, caveat 2) |
| Phase 8 | 912 | ✅ all 7 criteria pass | 2026-09-01 | code inspection + binary test |
| Phase 9b | 949 | ✅ both commands implemented | 2026-09-01 | `seam diff`, `seam import --from-url` |
| Phase 11 | 959 | ✅ all 6 criteria pass | 2026-09-01 | three-state rendering, breaker, structured 503, `x-breaker` |
| Phase 13 | 961 | ✅ all 3 criteria pass | 2026-09-01 | loop breaker, cost governor, dry-run |
| Phase 14 | 962 | ✅ all 4 rules pass | 2026-09-01 | JWT validation, scope mapping, header stripping, default-deny; 35 tests |

### Unchecked `[ ]` — 11 phases

| Phase | Line | Evidence status | Primary blocker |
|-------|------|-----------------|-----------------|
| Phase 1a | 868 | ❓ no evidence file | evidence not generated |
| Phase 1b | 881 | ❓ no evidence file | evidence not generated |
| Phase 2 | 882 | ❓ no evidence file | evidence not generated |
| Phase 3 | 888 | ❌ NOT COMPLETE | hot-reload flag exists but not enabled in deployment.yaml |
| Phase 4 | 889 | ❌ INCOMPLETE | fragments not mounted, OpenBao secrets missing |
| Phase 5 | 890 | ❌ CRITICAL FAILURE | `iad-native-ads` missing, schema + YAML-parsing bugs, 6/9 Connectors missing |
| Phase 6b | 896 | ❌ BLOCKED | fragment loader is JSON-only; production YAML cannot load |
| Phase 7 | 897 | ❌ INCOMPLETE | `tsnet` provisioning placeholder, no runtime verification |
| Phase 9a | 931 | ❓ no evidence file | evidence not generated; verdict summary stands in as source of record |
| Phase 10 | 950 | ❌ CRITICAL FAILURE | duplicate `/whoami` registration panics at startup |
| Phase 12 | 960 | ❌ CANNOT VERIFY | 99 compile errors; acceptance undemonstrated |

## 6. Per-phase mapping rationale

### 6.1 The 13 evidence-backed phases

For each row: the filename join (rule 1, §2) selects exactly one checkbox; corroborations A and B confirm it; the expected state follows rule 6 from the 2026-09-01 verdict.

- **Phase 3** → line 888. Verdict FAIL (hot-reload flag present but not enabled in `deployment.yaml`; 3/6 pass, 2/6 fail, 1/6 blocked). Annotation `INCOMPLETE` cites `.beads/phase3-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 4** → line 889. Verdict FAIL (fragment YAML authored but never mounted; no routes served; no OpenBao secrets; 1/7 pass). Annotation `INCOMPLETE` cites `.beads/phase4-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 5** → line 890. Verdict FAIL (`iad-native-ads` absent from upstream map and allowlist; schema-validation and YAML-parsing bugs; 6 of 9 Tailscale Connectors missing; 2/8 pass). Annotation `INCOMPLETE` cites `.beads/phase5-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 6a** → line 891. Verdict PASS, qualified: 8/10 criteria verified; the 2 pending items (tag-restricted ACL grant; two-listener ACL split) needed manual Tailscale-admin verification. Expected `[x]` (a qualified pass still passes rule 6), actual `[x]`. See §7 caveat 2 for the post-verdict resolution.
- **Phase 6b** → line 896. Verdict FAIL/BLOCKED (fragment loader JSON-only, so production YAML fragments cannot load; cutover cannot proceed). Annotation `BLOCKED` cites `.beads/phase6b-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 7** → line 897. Verdict FAIL (5/13 implemented; NEEDLE-side `tsnet` provisioning in placeholder mode; no runtime verification). Annotation `INCOMPLETE` cites `.beads/phase7-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 8** → line 912. Verdict PASS 7/7 (deprecation/sunset headers, `x-adapter`, version selection, version-aware docs, per-version metric, `/changes`, retirement evaluator). Annotation `VERIFIED COMPLETE` cites `.beads/phase8-evidence.md`. Expected `[x]`, actual `[x]`.
- **Phase 9b** → line 949. Verdict PASS 2/2 (`seam diff`, `seam import --from-url`). Annotation `COMPLETE` cites `.beads/phase9b-evidence.md`. Expected `[x]`, actual `[x]`.
- **Phase 10** → line 950. Verdict CRITICAL FAILURE (duplicate `/whoami` registration panics at startup; 4/7 verified at code level only). Annotation `CRITICAL FAILURE` cites `.beads/phase10-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 11** → line 959. Verdict PASS 6/6 (three-state rendering, breaker policy and structured 503, `x-breaker`). Annotation `VERIFIED COMPLETE` cites `.beads/phase11-evidence.md`. Expected `[x]`, actual `[x]`.
- **Phase 12** → line 960. Verdict CANNOT VERIFY (99 compile errors plus runtime-environment requirements; implementation exists but acceptance undemonstrated). Annotation `CANNOT VERIFY ACCEPTANCE` cites `.beads/phase12-evidence.md`. Expected `[ ]` — under rule 6 an undemonstrated phase stays unticked even where code exists. Actual `[ ]`.
- **Phase 13** → line 961. Verdict PASS 3/3 (loop breaker, cost governor, dry-run; verified by code + test inspection). Annotation `VERIFIED COMPLETE` cites `.beads/phase13-evidence.md`. Expected `[x]`, actual `[x]`.
- **Phase 14** → line 962. Verdict PASS 4/4 (JWT validation, scope mapping, header stripping, default-deny; 35 tests). Annotation `COMPLETE` cites `.beads/phase14-evidence.md`. Expected `[x]`, actual `[x]`.

### 6.2 The 4 phases without evidence files

- **Phase 1a (line 868), Phase 1b (line 881), Phase 2 (line 882).** No evidence file exists (inventory rows "Missing — Cannot verify"); no in-text annotation. Verdict NO EVIDENCE → expected `[ ]`; actual `[ ]`. **Mapping caveat:** this agreement is *weak/vacuous* — an unticked box is consistent with NO EVIDENCE, but the state alone cannot distinguish "implemented but never evidenced" from "not attempted". Any future tick of these three lines requires a new evidence file first; mapping by absence is the decision of record.
- **Phase 9a (line 931).** No evidence file exists, but unlike 1a/1b/2 the checkbox carries an annotation: `NO EVIDENCE — verified 2026-09-01` citing `.beads/phase-verdict-summary.md` (§9a) rather than a per-phase file. **Mapping decision:** the verdict summary itself is the verdict source of record for 9a — evidence *of absence* stands in for the absent file. Expected `[ ]`, actual `[ ]`.

## 7. Agreement result and discrepancy scan

- **Zero discrepancies.** Expected state (derived from verdict data) equals actual state for all 17 phases. **No checkbox state changes are required or recommended on this evidence**, and none were made.
- **Three-way agreement for the 13 evidence-backed phases:** verdict summaries ↔ in-text annotation ↔ tick state line up in every case, under the §3 normalization.
- **Fourth leg:** plan.md's `**Status:**` header (line 5) partitions the phases identically — 6 verified complete (6a, 8, 9b, 11, 13, 14), 7 incomplete (3, 4, 5, 6b, 7, 10, 12), 4 lack evidence (1a, 1b, 2, 9a) — matching both verdict files and the checkbox states exactly.

## 8. Change recommendations

### 8.1 Checkbox state changes: none

All 17 checkboxes correctly reflect the verdict data. No tick, untick, or annotation edit is recommended: applying a change with zero expected-vs-actual deltas would be a no-op, and the ticked states already carry dated evidence-citing annotations per the plan's house rule.

### 8.2 Recommended follow-up work (not checkbox edits)

1. **Generate evidence for the 4 absence rows** — see §9 for priority order. Any future tick of 1a/1b/2/9a is gated on a new evidence file first.
2. **Commit the 2026-09-18 checkbox-state extraction** (seam-7fdee15d's deliverable, currently working-tree-only) so the superseded 2026-09-02 assessment file at `.beads/plan-checkbox-state.md` is replaced at HEAD, not just on disk.
3. **Close Phase 6a's two criteria explicitly** at the next 6a re-verification rather than inheriting this document's post-verdict note (§7 caveat 2).
4. **Fix the two self-contradictions in `.beads/evidence-verdict-summary.md`** (§7 caveat 3) so later readers do not propagate the bad count or the false "no missing evidence files" claim.
5. **Remediation order for the failing phases** (carried from v1.0, unchanged by the verdict incorporation): fix the YAML fragment loader (blocks 3, 5, 6b); resolve the 99 compile errors (blocks 7, 10, 12 runtime verification); enable hot reload in `deployment.yaml` (Phase 3); provision OpenBao secrets and mount fragment ConfigMaps (Phase 4); add `iad-native-ads` plus the 6 missing Tailscale Connectors (Phase 5); fix the duplicate `/whoami` registration (Phase 10).

## 9. Phases needing evidence files

| Phase | Evidence file to create | Priority | Reason |
|-------|------------------------|----------|--------|
| Phase 9a | `.beads/phase9a-evidence.md` | **HIGH** | `seam lint` is the CI gate for fragment validation and Phase 3's named precondition |
| Phase 2 | `.beads/phase2-evidence.md` | HIGH | Secret injection is a dependency of phases 4, 7, 12, 13; first end-to-end credential proof |
| Phase 1b | `.beads/phase1b-evidence.md` | MEDIUM | Fragment merge is foundational for phases 7 and 13 dependencies |
| Phase 1a | `.beads/phase1a-evidence.md` | LOW | Foundational work predates evidence tracking |

## 10. Caveats and source-data notes

1. **Verdict currency.** All verdicts date from the 2026-09-01 re-verification pass (seam-7e2bcb06) and are the newest complete verdict set embedded in the artifacts at HEAD. This document maps evidence to checkboxes as the artifacts stand; it does not re-adjudicate any phase. A later phase re-verification would change the *expected* column, never the mapping logic.
2. **Phase 6a's two pending items.** The 6a tick rests on a qualified pass (8/10; the tag-restricted ACL grant and the two-listener ACL split were pending manual verification). Post-verdict, the tailnet ACL slice those items depend on was applied and verified — commit `aeefe43` (2026-09-18, bead seam-a2413e9d) records `seam-rs-manager` carrying only `tag:seam` with `terraform plan` clean against the live ACL. Rule 6 already expected `[x]` from the PASS verdict, so the mapping is unchanged; a future full re-verification of 6a should still close the two criteria explicitly rather than inherit this note.
3. **Source typos, flagged so they are not propagated.** `.beads/evidence-verdict-summary.md` line 24 says "5 phases COMPLETE" while listing six (6a, 8, 9b, 11, 13, 14) — count typo; the correct count is 6 (its own Summary Statistics section says "Complete (6)"). Its "Missing Evidence Files" section says "No missing evidence files", contradicting its own table and `.beads/phase-inventory.md`; the inventory is authoritative for file existence: 1a, 1b, 2 and 9a have no evidence files.
4. **Extraction supersession.** `.beads/plan-checkbox-state.md` is being superseded by the 2026-09-18 extraction-only pass (bead seam-7fdee15d); at anchor `be68251` the committed file is still the 2026-09-02 assessment version, with the extraction present only in the working tree (§1 note). The older `.beads/evidence-to-checkbox-cross-reference.md` (2026-09-02) remains useful as the per-criterion decomposition but predates the supersession and should not be used for state anchors.
5. **Anchor drift.** Line anchors are valid at `be68251`. Any edit to `docs/plan/plan.md` above line 962 invalidates them; re-run the extraction (or re-grep `- [x]`/`- [ ] Phase`) before applying any tick/untick.
6. **Correction to v1.0.** v1.0's "Key Findings" opened with a "Critical Disconnect: 6 phases marked complete (`[x]`) in plan.md are demonstrably incomplete", naming phases 3, 4, 5, 6b, 7 and 10 — none of which is ticked, and all six of which v1.0's own alignment table shows unticked and aligned. That finding described the pre-2026-09-01 state (when those boxes were wrongly ticked and then corrected) and was stale the moment v1.0 was written; this document's §7 states the actual result (zero discrepancies). v1.0's mapping table, checkbox view and remediation plan remain sound and are carried forward here.

## 11. Summary statistics

| Metric | Value | Details |
|--------|-------|---------|
| Total phases | 17 | 1a, 1b, 2–14 (letter suffixes atomic) |
| Evidence files | 13 | 76% coverage |
| Missing evidence | 4 | Phases 1a, 1b, 2, 9a |
| Verified complete `[x]` | 6 | Phases 6a, 8, 9b, 11, 13, 14 |
| Verified incomplete `[ ]` | 7 | Phases 3, 4, 5, 6b, 7, 10, 12 |
| Cannot verify `[ ]` | 4 | Phases 1a, 1b, 2, 9a (no evidence) |
| Mapping discrepancies | **0** | Expected = actual for all 17 |
| Checkbox changes required | **0** | All states already agree with verdict data |

### Blocker metrics (unchanged from v1.0)

| Blocker type | Affected phases |
|--------------|-----------------|
| YAML fragment support (loader is JSON-only) | 3, 5, 6b |
| Compilation errors (99, since 2026-08-30) | 7, 10, 12 |
| Missing runtime environment | 3, 4, 5, 7, 10, 12 |
| Missing Tailscale Connectors (6 of 9 clusters) | 5 |
| Missing OpenBao secrets (twitterapi, zai) | 4 |
| Deployment integration gaps (hot reload, ConfigMap mounts) | 3, 4 |

---

## 12. Verification record for this document

Compiled by seam-d7aac6fe on 2026-09-18 at anchor `be68251`. Checks run before publication, all against `be68251` via `git show`/`git ls-tree` (never the dirty shared working tree):

- All 17 checkbox anchors, states and phase labels re-extracted live: lines 868, 881, 882, 888, 889, 890, 891, 896, 897, 912, 931, 949, 950, 959, 960, 961, 962; `[x]` exactly at 891, 912, 949, 959, 961, 962 — matching §4 row for row.
- All 13 mapped evidence files confirmed present at HEAD (`git ls-tree`); 1a, 1b, 2, 9a confirmed absent.
- 13 checkbox lines confirmed to carry a `.beads/phase*-evidence.md` citation (corroboration B).
- plan.md Status header (line 5) confirmed to state the 6/7/4 partition (fourth leg).
- Working-tree 2026-09-18 extraction cross-checked: agrees with HEAD states on all 17 phases.
- Draft `.beads/evidence-checkbox-draft.md` (committed `be68251`) incorporated in full; no mapping-logic change from draft to this final document.

This document does **not** modify any checkbox in `docs/plan/plan.md`, re-verify any phase, create missing evidence files, or update the plan's `**Status:**` header — those remain owned by their respective beads.

**Document Version:** 2.0
**Chain:** seam-bde2c73b / seam-cfcd1399 / seam-8e80d1f0 → seam-f3e0a9dd (v1.0) → seam-7fdee15d (2026-09-18 extraction) → seam-9c80a071 (verdict-incorporated draft) → **seam-d7aac6fe (this document)**
