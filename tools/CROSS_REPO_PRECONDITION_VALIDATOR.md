# Cross-Repo Precondition Validator

## Overview

The `validate_cross_repo_preconditions.sh` script automatically detects and validates cross-repository dependencies documented in bead descriptions. When a bead depends on work in another workspace (e.g., "declarative-config bead X must be merged first"), this validator checks if the precondition is met and marks beads accordingly.

## Problem Solved

Without automated precondition validation, beads with cross-repo dependencies appear ready (no dependencies, no assignee) but cannot be worked due to external blockers. This causes starvation alerts where the system reports open beads but the ready frontier is empty.

Example from bead `seam-0f0172a0`:
- Bead was open with no dependencies
- But it required: `declarative-config bead "github-commit-status: post iad-ci results to GitHub commit statuses" is merged`
- This external dependency couldn't be expressed as a formal `blocks` edge
- Result: Bead appeared ready but was unclaimable, triggering starvation alerts

## Usage

```bash
# Dry run (recommended first)
./tools/validate_cross_repo_preconditions.sh --dry-run

# Full execution (marks beads with unmet preconditions)
./tools/validate_cross_repo_preconditions.sh

# Verbose mode for debugging
./tools/validate_cross_repo_preconditions.sh --verbose
```

## Precondition Format

Document cross-repo preconditions in bead descriptions using this format:

```markdown
## Precondition (cross-repo, cannot be a blocks edge)

<workspace-name> bead "<bead-pattern>" is merged|closed
```

### Examples

**By bead ID:**
```markdown
## Precondition (cross-repo, cannot be a blocks edge)

declarative-config bead "declarative-abc123def" is merged
```

**By title pattern:**
```markdown
## Precondition (cross-repo, cannot be a blocks edge)

declarative-config bead "github-commit-status: post iad-ci results to GitHub commit statuses" is merged
```

**Multiple preconditions:**
```markdown
## Precondition (cross-repo, cannot be a blocks edge)

- declarative-config bead "argo-workflow-template: deploy service" is merged
- NEEDLE bead "needle-flush-checkpoint" is closed
- armor bead "armor-auth-integration" is merged
```

## How It Works

1. **Scans all open beads** in the current workspace
2. **Extracts precondition patterns** from descriptions matching the format above
3. **Queries remote workspaces** via the `bead` CLI to check if:
   - The referenced bead exists
   - The bead has the required status (merged/closed)
4. **Takes action**:
   - If preconditions are **unmet**: Marks bead as `manual_blocked=true` with details
   - If preconditions are **met**: Clears `manual_blocked` if it was previously set
5. **Reports** statistics and any errors encountered

## Supported Workspaces

The validator searches for workspaces under `$WORKSPACE_ROOT` (default: `/home/coding`). A workspace must:
- Exist as a directory under the workspace root
- Contain a `.beads/beads.db` file (bead-rs store)
- Be accessible with the `bead` CLI

Currently supported workspaces include:
- `SEAM`
- `NEEDLE`
- `declarative-config`
- `ARMOR`
- `CLASP`
- And any other workspace with a bead-rs store

## Integration Options

### 1. Manual Execution

Run before claiming work or when starvation alerts occur:

```bash
# In a workspace with starvation alerts
cd /home/coding/SEAM
./tools/validate_cross_repo_preconditions.sh
```

### 2. Cron Integration (Recommended)

Add to crontab for automatic validation:

```bash
# Run every 5 minutes
*/5 * * * * cd /home/coding/SEAM && ./tools/validate_cross_repo_preconditions.sh >> /var/log/seam-precondition-validator.log 2>&1
```

### 3. NEEDLE Worker Integration

Add to NEEDLE worker startup or before claiming beads:

```bash
# In NEEDLE worker script
cd /home/coding/SEAM
./tools/validate_cross_repo_preconditions.sh --dry-run
# Then proceed with normal worker logic
```

## Exit Codes

- `0`: Success (all preconditions validated or no open beads)
- `1`: Usage error or invalid arguments
- `2`: No workspace found or permission error
- `3`: Bead CLI error

## Statistics Reporting

The validator reports:
- **Total beads processed**: Number of open beads scanned
- **Beads with preconditions**: Beads having documented cross-repo dependencies
- **Preconditions met**: Preconditions that are satisfied
- **Preconditions unmet**: Preconditions that are blocking work
- **Errors**: Parsing or workspace access errors

Example output:
```
[2026-08-30T21:59:31-04:00] === Validation complete ===
[2026-08-30T21:59:31-04:00] Total beads processed: 7
[2026-08-30T21:59:31-04:00] Beads with preconditions: 1
[2026-08-30T21:59:31-04:00] Preconditions met: 0
[2026-08-30T21:59:31-04:00] Preconditions unmet: 1
[2026-08-30T21:59:31-04:00] Errors: 0
```

## Troubleshooting

### "Workspace not found" Error

The validator cannot find the referenced workspace:
```
Warning: Workspace 'declarative-config' not found at /home/coding/declarative-config
```

**Solution**: Check that the workspace path is correct and accessible.

### "Bead not found" Error

The pattern doesn't match any beads in the target workspace:
```
No beads found matching pattern: github-commit-status
```

**Solution**: Update the bead description with the correct bead ID or a more precise title pattern.

### Bead Exists But Not Closed

The precondition bead exists but hasn't reached the required state:
```
Precondition NOT met: declarative-config/github-commit-status exists but is not merged
```

**Solution**: Wait for the precondition bead to close, or update the precondition if it's no longer needed.

## Implementation Notes

### Bead Store Backend

The script uses `bead-rs` (the canonical CLI as of 2026-08-14). It queries bead databases directly using SQLite and the `bead` CLI.

### Pattern Matching

The regex pattern for preconditions:
```bash
^[[:space:]]*([a-z0-9-]+)[[:space:]]+bead[[:space:]]+\"([^\"]+)\"[[:space:]]+is[[:space:]]+(merged|closed)
```

This matches:
- Workspace names: lowercase alphanumeric with hyphens
- Bead patterns: any text within quotes
- Condition keywords: "merged" or "closed"

### CSV Parsing

The script uses jq to convert bead JSON to CSV for reliable field extraction:
```bash
jq -r '.[] | [.id, .title, .description, (.manual_blocked // false)] | @csv'
```

This handles complex descriptions with newlines, quotes, and special characters.

## Related Documentation

- [Beads (bead-rs CLI)](/home/coding/CLAUDE.md#beads-bead-rs-cli)
- [NEEDLE Fleet Dispatch](/home/coding/CLAUDE.md#needle-fleet-dispatch--no-worktrees)
- [SEAM Operational Runbook](/home/coding/SEAM/docs/operational-runbook.md)

## Future Enhancements

Potential improvements:
1. **Real-time validation**: Hook into bead creation/update to validate preconditions immediately
2. **Notification system**: Alert when preconditions are met (bead becomes claimable)
3. **Dependency graph visualization**: Show cross-repo dependencies as a graph
4. **Precondition templates**: Standard precondition formats for common patterns
5. **Automatic unblocking**: Integrate with the bead claim process to auto-release when preconditions are met
