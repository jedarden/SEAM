# SEAM Plan Structure Analysis

**Generated:** 2026-09-02  
**Source:** `docs/plan/plan.md`  
**Purpose:** Reconnaissance of plan.md structure for extraction planning

## File Statistics

- **Size:** 463KB (463,000 bytes)
- **Lines:** 1,116 lines
- **Location:** `/home/coding/SEAM/docs/plan/plan.md`
- **Reading Strategy:** Use targeted grep/sed patterns rather than loading entire file into context

## Document Structure

### Major Sections (Level 2 Headers ##)

1. Status & Revision History (line 3)
2. Normative Language (line 22)
3. Overview (line 30)
4. Motivation (line 34)
5. Glossary (line 38)
6. Non-Goals and What SEAM Is Not (line 65)
7. Acceptance Scenarios (line 87)
8. Success Metrics (line 253)
9. Architecture (line 261)
10. Hosting (line 367)
11. Components (line 378)
12. Data Models (line 407)
13. Future: Per-Agent Tool Scoping (line 501)
14. Version Migration Strategy (line 521)
15. Credential Health Sentinel (line 576)
16. Per-Route Guards: Loop Breaker and Cost Governor (line 596)
17. Response Caching and Single-Flight Coalescing (line 615)
18. Multi-Instance Routes and Fan-Out (line 635)
19. Passive Route Health — Last-Success Tracking (line 647)
20. Fragment Toolchain (line 670)
21. Pre-Phase-7 Trust Boundary (line 684)
22. Testing Strategy (line 696)
23. Performance Budgets (line 753)
24. Implementation Phases (line 794)
25. Risk Register (line 970)
26. Edge Case Catalog (line 993)
27. Anti-Patterns Catalog (line 1022)
28. Security Threat Matrix and Audit Logging (line 1043)
29. Proof Obligations Ledger (line 1068)
30. Open Questions (line 1084)
31. ADR-001: Go Language Ratification (line 1091)

## Phase Organization

### Implementation Phases Section

**Location:** Lines 794-969  
**Total Phases:** 17 phases  
**Checkbox Format:** `- [ ]` for incomplete, `- [x]` for complete

#### Current Status (as of 2026-09-01)

- **Complete (6 phases):** 6a, 8, 9b, 11, 13, 14
- **Incomplete (7 phases):** 3, 4, 5, 6b, 7, 10, 12
- **No Evidence (4 phases):** 1a, 1b, 2, 9a

#### Ship Order (Topological)

```
1a → 1b → 9a → 2 → 6a → 3 → 4 → 10 → 5 → 11 → 13 → 6b → 7 → 12 → 8 → 9b → 14
```

**Phase Numbers are Stable Identifiers:** Phase numbers do not represent execution order but are stable references used throughout the document and in filed beads.

### Phase List with Checkbox Status

| Phase | Checkbox | Status | Description |
|-------|----------|---------|-------------|
| 1a | `[ ]` | No evidence | Gateway scaffold (Go, HTTP server, configuration) |
| 1b | `[ ]` | No evidence | Fragment merge, collision detection, quarantine |
| 9a | `[ ]` | No evidence | Fragment toolchain (`seam lint` validation) |
| 2 | `[ ]` | No evidence | Secret injection, OpenBao integration, scrubbing |
| 6a | `[x]` | **COMPLETE** | Deploy SEAM to rs-manager (single replica) |
| 3 | `[ ]` | Incomplete | Hot reload via ConfigMap file watch |
| 4 | `[ ]` | Incomplete | Onboard z.ai/GLM and twitterapi.io proxy fragments |
| 10 | `[ ]` | Incomplete | Multi-instance routes with x-instance-param |
| 5 | `[ ]` | Incomplete | Onboard kubectl-proxy endpoints (parametrized) |
| 11 | `[x]` | **COMPLETE** | Passive route health (last-2xx tracking, breakers) |
| 13 | `[x]` | **COMPLETE** | Per-route guards (loop breaker, cost governor) |
| 6b | `[ ]` | Incomplete | Agent cutover to SEAM (service-by-service) |
| 7 | `[ ]` | Incomplete | Per-agent scoping with Tailscale WhoIs/Grant |
| 12 | `[ ]` | Incomplete | Credential health sentinel with probe loop |
| 8 | `[x]` | **COMPLETE** | Version migration tooling (deprecation, adapters) |
| 9b | `[x]` | **COMPLETE** | Fragment authoring tools (seam diff, seam import) |
| 14 | `[x]` | **COMPLETE** | Non-tailnet ingress (Cloudflare Access JWT) |

## Checkbox Format Patterns

### Syntax

```markdown
- [x] Phase X: [Name] — [Description with detailed acceptance criteria]
- [ ] Phase Y: [Name] — [Description with blockers or incomplete status]
```

### Format Characteristics

1. **Leading marker:** `- ` (markdown bullet)
2. **Checkbox:** `[x]` or `[ ]` (no space between brackets)
3. **Phase identifier:** `Phase X:` where X is number/letter
4. **Description separator:** `—` (em dash)
5. **Detailed content:** Multi-line descriptions with:
   - Acceptance criteria
   - Dependencies
   - Verification status
   - Evidence file references (`.beads/phaseN-evidence.md`)

### Content Patterns

**Complete phases include:**
- Verification date and evidence file reference
- Pass/fail criteria counts
- Specific completion criteria met

**Incomplete phases include:**
- Blocker descriptions
- Specific failure points
- Evidence of what's missing
- References to verification evidence

## Estimated Checkbox Counts

### Primary Phase Checkboxes
- **Total:** 17 phase checkboxes
- **Checked:** 6 ([x])
- **Unchecked:** 11 ([ ])

### Nested Structure
Each phase description contains embedded acceptance criteria but these are **not checkboxes**—they are structured as:
- Bullet points with completion criteria
- Dependencies and verification requirements
- Evidence file references

### Sub-section Checkboxes
The document also contains checkboxes within:
1. **Acceptance Scenarios (9 scenarios)** - structured as test cases, not checkboxes
2. **Testing Strategy** - completion criteria formatted as lists
3. **Risk Register** - tabular format, no checkboxes

## Reading Strategy Recommendations

### For Full Extraction
1. **Use line-based extraction:** `sed -n 'X,Yp'` for specific sections
2. **Leverage grep patterns:** Target specific phase ranges with `grep -A 50 "Phase N:"`
3. **Chunk by major section:** Process each ## section independently
4. **Stream processing:** Use awk/perl for structured extraction rather than loading full file

### Efficient Phase Extraction
```bash
# Extract specific phase with context
grep -A 30 "^- \[[x ]\] Phase [0-9]" docs/plan/plan.md

# Extract implementation phases section
sed -n '794,969p' docs/plan/plan.md

# Extract only checked phases
grep -A 30 "^- \[x\] Phase" docs/plan/plan.md
```

### Memory-Efficient Processing
- **Never load entire file** - use streaming parsers
- **Process one phase at a time** - each phase is ~30-50 lines
- **Cache phase line numbers** - build index of phase start lines
- **Use targeted ranges** - sed/awk with specific line ranges

## Structural Notes

### Document Conventions

1. **Provenance markers:** Inline `(decided <date>)`, `(corrected <date>)`, `(added <date>)`, `(clarified <date>)`
2. **Status tracking:** Status line at top tracks completion across all phases
3. **Cross-references:** Extensive internal references between sections
4. **Evidence files:** Each phase has corresponding `.beads/phaseN-evidence.md` file

### Phase Dependencies

The plan explicitly states dependency edges that constrain ship order:
- Hard dependencies (e.g., `1a → 1b → everything`)
- Pre-phase prerequisites (OpenBao role/policy provisioning)
- Forcing edges (e.g., `7 → 14` for identity sources)

### Metadata Embedded in Plan

- Revision history table
- ADR-001 language decision
- Risk register with fallback strategies  
- Proof obligations ledger
- Open questions (all resolved as of 2026-08-21)

## Recommendations for Extraction

1. **Phase-first approach:** Extract implementation phases first (highest checkbox density)
2. **Build line index:** Create mapping of phase names to line numbers
3. **Preserve formatting:** Maintain markdown structure and checkbox syntax
4. **Track relationships:** Capture dependency edges and ship order
5. **Include metadata:** Extract verification status and evidence file references

---

**Analysis complete.** Ready for structured extraction based on documented patterns and line ranges.