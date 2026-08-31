# SEAM Scripts

This directory contains utility scripts for SEAM operations and maintenance.

## bead-starvation-recovery.sh

Automated recovery script for bead starvation issues where beads with stale assignees become permanently unclaimable.

### Problem Statement

From CLAUDE.md "NEEDLE Learnings":

> On 2026-08-24 a fleet-wide sweep found **583** beads stuck this way across 47 of 66 workspaces, with ten workspaces fully starved (`--ready` returning zero while live workers spun). Root cause was NOT reopen — see NEEDLE bead `needle-44e7e5cd` (Mend skips any assignee whose worker *name* is still alive, and `--count 1` workers relaunch under the same name forever).

The failure mode: beads that are `status=open` but have a non-null `assignee` field pointing to a worker that is no longer running. These beads never appear in the `--ready` frontier and become permanently invisible to workers.

### Solution

The script performs four steps:

1. **Run `bead doctor --repair`** — Fixes stale temp files or checkpoint views
2. **Identify running workers** — Extracts current NEEDLE worker identifiers from `ps aux`
3. **Find stale beads** — Queries for `status=open AND assignee IS NOT NULL AND assignee not in running workers`
4. **Clear stale assignees** — Runs `bead update <id> --clear-assignee` for each stuck bead

All actions are logged to `.beads/doctor-recovery.log` with timestamps.

### Usage

```bash
# Dry run (no actual changes)
./scripts/bead-starvation-recovery.sh --dry-run

# Actual recovery
./scripts/bead-starvation-recovery.sh
```

### Output

The script writes both to stdout and to `.beads/doctor-recovery.log`:

```
[2026-08-31T03:39:44Z] === Starvation Recovery Run at 2026-08-31T03:39:44Z ===
[2026-08-31T03:39:44Z] [1/4] Running bead doctor --repair...
[2026-08-31T03:39:44Z]   ✓ bead doctor --repair completed successfully
[2026-08-31T03:39:44Z] [2/4] Identifying currently running NEEDLE workers...
[2026-08-31T03:39:44Z]   ✓ Found 9 running worker(s): glm-armor glm-cgraph glm-coned glm-hopt glm-mta glm-seam glm-spaxel glm-tunnel glm-vista
[2026-08-31T03:39:44Z] [3/4] Identifying assigned-but-open beads...
[2026-08-31T03:39:44Z]   ✓ No stale assigned-but-open beads found
[2026-08-31T03:39:44Z] [4/4] Clearing stale assignees...
[2026-08-31T03:39:44Z] 
[2026-08-31T03:39:44Z] Recovery Summary:
[2026-08-31T03:39:44Z]   Total stale beads identified: 0
[2026-08-31T03:39:44Z]   Total repairs performed: 0
[2026-08-31T03:39:44Z] === End of Recovery Run ===
```

### Integration

This script can be:
- Run manually when starvation is suspected
- Added to a cron job for periodic fleet health checks
- Integrated into NEEDLE worker startup/shutdown hooks
- Called by monitoring systems when `bead list --ready` returns zero but workers are spinning

### Related

- See bead `seam-d267f63b`: "Starvation alert: beads invisible in — Starvation diagnostic and recovery implementation"
- See bead `seam-efe08209`: "Starvation alert: beads invisible in — Automated bead doctor repair with stale-assignee cleanup" (this implementation)
- See CLAUDE.md: "NEEDLE Learnings" section on the 583-bead starvation incident
