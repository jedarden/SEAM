# Evidence → Checkbox Draft Mapping (verdict-incorporated)

**Generated:** 2026-09-18
**Bead:** seam-9c80a071 — "Map evidence to checkboxes with verdicts"
**Status:** DRAFT — mapping logic and rationale only. No checkbox in `docs/plan/plan.md` was modified by this bead.
**Anchor commit:** `2b9a753` (local verification pass) = `aeefe43` (canonical `origin/main` tip). The two trees are identical for every path this mapping cites; they differ only under `corpus/`, `logs/`, `scratch/` and `.gitignore` (history-purge commit `9984a5b`). All plan.md line anchors below are valid at both.

## 1. Inputs

| Input | File | Version used | Role |
|---|---|---|---|
| Phase inventory | `.beads/phase-inventory.md` | 2026-09-02 (13 files, 4 missing: 1a, 1b, 2, 9a) | Authoritative for which evidence files exist |
| Checkbox states | `.beads/plan-checkbox-state.md` | 2026-09-18 10:26 UTC @ `2b9a753` (17 boxes: 6 ticked, 11 unticked; supersedes the 2026-09-02 assessment-style file at the same path) | Authoritative for current checkbox state and line anchors |
| Verdict data | `.beads/phase-verdict-summary.md` + `.beads/evidence-verdict-summary.md` | 2026-09-01, produced by bead seam-7e2bcb06 | Authoritative for per-phase PASS/FAIL verdicts |
| Plan text | `docs/plan/plan.md` | checked live at `2b9a753`; checkbox lines byte-identical in working tree and HEAD | The checkbox target lines |

A fourth corroboration source is used in §6: the `**Status:**` header of `docs/plan/plan.md` (line 5), whose phase partition (6 verified / 7 incomplete / 4 no-evidence) is quoted from the same 2026-09-01 verification pass.

## 2. Mapping logic

The join is **phase identity**, applied by these rules:

1. **Phase-ID join (primary rule).** An evidence file named `phase<ID>-evidence.md` maps to the unique checkbox line in `docs/plan/plan.md` whose label begins `Phase <ID>:` within the `Implementation Phases` section. Letter suffixes are atomic identifiers: `6a ≠ 6b` and `9a ≠ 9b`.
2. **Line anchors.** Anchor line numbers come from the 2026-09-18 extraction; all 17 were re-verified live against `docs/plan/plan.md` (grep `- [x]`/`- [ ] Phase`, lines 868–962, strictly increasing).
3. **Corroboration A — evidence self-title.** Each of the 13 evidence files' H1 names its own phase (e.g. `# Phase 6a Completion Evidence`), and in all 13 cases matches the phase ID in its filename. No file claims a different phase than its name.
4. **Corroboration B — plan-text citation.** 13 of the 17 checkbox texts embed a 2026-09-01 verification annotation that cites exactly one `.beads/phase*-evidence.md` path — and the cited path equals the mapped filename in every case. The citation set and the evidence-file set are **bijective** (no cross-phase citations, no orphans). The 14th annotation (Phase 9a) cites `.beads/phase-verdict-summary.md` instead, because its evidence file does not exist (see §5.2).
5. **Cardinality — 1:1.** The section contains exactly one checkbox per phase and each evidence file covers exactly one phase's completion criteria. No evidence file maps to multiple checkboxes; no checkbox rests on multiple evidence files. Mapping is by phase label, **not** by ship-order position — plan line 800's ship order (`1a → 1b → 9a → 2 → 6a → 3 → 4 → 10 → 5 → 11 → 13 → 6b → 7 → 12 → 8 → 9b → 14`) deliberately differs from document order, so position is not a join key.
6. **Verdict → expected state rule.**
   - `PASS` (including the qualified "substantial pass") → expected `[x]`
   - `FAIL` / `BLOCKED` / `CRITICAL FAILURE` / `CANNOT VERIFY` → expected `[ ]`
   - `NO EVIDENCE` → expected `[ ]` — a checkbox may only be ticked on demonstrated evidence. This is the plan's own house rule: every ticked box carries a dated "COMPLETE — verified …" annotation naming its evidence file, so an unevidenced tick would violate the annotation convention.

The join covers 100% of both sides: all 17 checkboxes (the extraction's completeness checks confirm these are the only bracket-pair checkboxes anywhere in plan.md) and all 13 evidence files, plus 4 explicit absence rows.

## 3. Terminology normalization

The verdict files, the checkbox annotations and the tick state use different vocabularies for the same underlying verdict. Normalization used in §4:

| Verdict (2026-09-01 summaries) | Checkbox annotation label | Expected state |
|---|---|---|
| ✅ PASS (incl. "substantial pass") | `COMPLETE` / `VERIFIED COMPLETE` | `[x]` |
| ❌ FAIL | `INCOMPLETE` | `[ ]` |
| ❌ FAIL (blocked) | `BLOCKED` | `[ ]` |
| ❌ FAIL (crash) | `CRITICAL FAILURE` | `[ ]` |
| ❌ CANNOT VERIFY | `CANNOT VERIFY ACCEPTANCE` | `[ ]` |
| ❌ NO EVIDENCE | *(no annotation; 9a: `NO EVIDENCE`)* | `[ ]` |

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

## 5. Per-phase mapping rationale

### 5.1 The 13 evidence-backed phases

For each row: the filename join (rule 1) selects exactly one checkbox; corroboration A (self-title) and corroboration B (the checkbox text's own citation of that exact file path) confirm it; the expected state follows rule 6 from the 2026-09-01 verdict.

- **Phase 3** → line 888. Verdict FAIL (hot-reload flag present but not enabled in `deployment.yaml`; 3/6 pass, 2/6 fail, 1/6 blocked). Annotation `INCOMPLETE` cites `.beads/phase3-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 4** → line 889. Verdict FAIL (fragment YAML authored but never mounted; no routes served; no OpenBao secrets; 1/7 pass). Annotation `INCOMPLETE` cites `.beads/phase4-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 5** → line 890. Verdict FAIL (`iad-native-ads` absent from upstream map and allowlist; schema-validation and YAML-parsing bugs; 6 of 9 Tailscale Connectors missing; 2/8 pass). Annotation `INCOMPLETE` cites `.beads/phase5-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 6a** → line 891. Verdict PASS, qualified: 8/10 criteria verified; the 2 pending items (tag-restricted ACL grant; two-listener ACL split) needed manual Tailscale-admin verification. Expected `[x]` (a qualified pass still passes rule 6), actual `[x]`. See §7 for the post-verdict resolution of those 2 items.
- **Phase 6b** → line 896. Verdict FAIL/BLOCKED (fragment loader JSON-only, so production YAML fragments cannot load; cutover cannot proceed). Annotation `BLOCKED` cites `.beads/phase6b-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 7** → line 897. Verdict FAIL (5/13 implemented; NEEDLE-side `tsnet` provisioning in placeholder mode; no runtime verification). Annotation `INCOMPLETE` cites `.beads/phase7-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 8** → line 912. Verdict PASS 7/7 (deprecation/sunset headers, `x-adapter`, version selection, version-aware docs, per-version metric, `/changes`, retirement evaluator). Annotation `VERIFIED COMPLETE` cites `.beads/phase8-evidence.md`. Expected `[x]`, actual `[x]`.
- **Phase 9b** → line 949. Verdict PASS 2/2 (`seam diff`, `seam import --from-url`). Annotation `COMPLETE` cites `.beads/phase9b-evidence.md`. Expected `[x]`, actual `[x]`.
- **Phase 10** → line 950. Verdict CRITICAL FAILURE (duplicate `/whoami` registration panics at startup; 4/7 verified at code level only). Annotation `CRITICAL FAILURE` cites `.beads/phase10-evidence.md`. Expected `[ ]`, actual `[ ]`.
- **Phase 11** → line 959. Verdict PASS 6/6 (three-state rendering, breaker policy and structured 503, `x-breaker`). Annotation `VERIFIED COMPLETE` cites `.beads/phase11-evidence.md`. Expected `[x]`, actual `[x]`.
- **Phase 12** → line 960. Verdict CANNOT VERIFY (99 compile errors plus runtime-environment requirements; implementation exists but acceptance undemonstrated). Annotation `CANNOT VERIFY ACCEPTANCE` cites `.beads/phase12-evidence.md`. Expected `[ ]` — under rule 6 an undemonstrated phase stays unticked even where code exists. Actual `[ ]`.
- **Phase 13** → line 961. Verdict PASS 3/3 (loop breaker, cost governor, dry-run; verified by code + test inspection). Annotation `VERIFIED COMPLETE` cites `.beads/phase13-evidence.md`. Expected `[x]`, actual `[x]`.
- **Phase 14** → line 962. Verdict PASS 4/4 (JWT validation, scope mapping, header stripping, default-deny; 35 tests). Annotation `COMPLETE` cites `.beads/phase14-evidence.md`. Expected `[x]`, actual `[x]`.

### 5.2 The 4 phases without evidence files

- **Phase 1a (line 868), Phase 1b (line 881), Phase 2 (line 882).** No evidence file exists (inventory rows "Missing — Cannot verify"); no in-text annotation. Verdict NO EVIDENCE → expected `[ ]`; actual `[ ]`. **Mapping caveat:** this agreement is *weak/vacuous* — an unticked box is consistent with NO EVIDENCE, but the state alone cannot distinguish "implemented but never evidenced" from "not attempted". Any future tick of these three lines requires a new evidence file first; the mapping itself is by absence, and that is the decision of record.
- **Phase 9a (line 931).** No evidence file exists, but unlike 1a/1b/2 the checkbox carries an annotation: `NO EVIDENCE — verified 2026-09-01` citing `.beads/phase-verdict-summary.md` (§9a) rather than a per-phase file. **Mapping decision:** the verdict summary itself is the verdict source of record for 9a — evidence *of absence* stands in for the absent file. Expected `[ ]`, actual `[ ]`.

## 6. Agreement result and discrepancy scan

- **Zero discrepancies.** Expected state (derived from verdict data) equals actual state for all 17 phases. The current checkbox file requires **no state changes** on this evidence.
- **Three-way agreement for the 13 evidence-backed phases:** verdict summaries ↔ in-text annotation ↔ tick state line up in every case, under the §3 normalization.
- **Fourth leg:** `docs/plan/plan.md`'s `**Status:**` header (line 5) partitions the phases identically — 6 verified complete (6a, 8, 9b, 11, 13, 14), 7 incomplete (3, 4, 5, 6b, 7, 10, 12), 4 lack evidence (1a, 1b, 2, 9a) — matching both verdict files and the checkbox states exactly.

## 7. Caveats and source-data notes

1. **Verdict currency.** All verdicts date from the 2026-09-01 re-verification pass (seam-7e2bcb06) and are the newest complete verdict set embedded in the artifacts at HEAD. This draft maps evidence to checkboxes as the artifacts stand; it does not re-adjudicate any phase. A later phase re-verification would change the *expected* column, never the mapping logic.
2. **Phase 6a's two pending items.** The 6a tick rests on a qualified pass (8/10; the tag-restricted ACL grant and the two-listener ACL split were pending manual verification). Post-verdict, the tailnet ACL slice those items depend on was applied and verified — commit `2b9a753`/`aeefe43` (2026-09-18) records `seam-rs-manager` carrying only `tag:seam` with `terraform plan` clean against the live ACL. Rule 6 already expected `[x]` from the PASS verdict, so the mapping is unchanged; a future full re-verification of 6a should still close the two criteria explicitly rather than inherit this note.
3. **Source typos, flagged so they are not propagated.** `.beads/evidence-verdict-summary.md` line 24 says "5 phases COMPLETE" while listing six (6a, 8, 9b, 11, 13, 14) — count typo; the correct count is 6 (its own §Summary Statistics says "Complete (6)"). Its "Missing Evidence Files" section says "No missing evidence files", contradicting its own table and `.beads/phase-inventory.md`; the inventory is authoritative for file existence: 1a, 1b, 2 and 9a have no evidence files.
4. **Extraction supersession.** `.beads/plan-checkbox-state.md` (2026-09-18, extraction-only, bead seam-7fdee15d) supersedes the 2026-09-02 assessment-style file previously at that path. This draft is built on the new extraction; the older `.beads/evidence-to-checkbox-cross-reference.md` (2026-09-02) remains useful as the per-criterion decomposition but predates the supersession and should not be used for state anchors.
5. **Anchor drift.** Line anchors are valid at `2b9a753`/`aeefe43`. Any edit to `docs/plan/plan.md` above line 962 invalidates them; re-run the extraction (or re-grep `- [x]`/`- [ ] Phase`) before applying any tick/untick.

## 8. What this draft does not do

- It does **not** modify any checkbox in `docs/plan/plan.md` — the current states already agree with the verdict data.
- It does **not** re-verify any phase, create missing evidence files (1a, 1b, 2, 9a), or resolve the blockers named in the FAIL verdicts.
- It does **not** update the plan's `**Status:**` header; that line's maintenance is owned elsewhere.
