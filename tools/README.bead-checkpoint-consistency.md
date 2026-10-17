# Bead checkpoint consistency check

The host `bead-checkpoint-consistency-check.timer` runs
`scripts/bead-checkpoint-consistency-check.sh --verbose` in SEAM every five
minutes. The script reads `bead sync status --format json` and
`bead doctor --json`. It uses the doctor's `checks` JSON array and reports a
failure if the doctor finds errors, the checkpoint is inconsistent, or the live
store and checkpoint are not aligned. It does not flush, repair, restore, or
close beads. A failed check needs operator inspection; it must not choose which
store state to keep.

The script adds `$HOME/.cargo/bin` to `PATH` when `bead` is otherwise absent,
matching the host unit's environment. Run it manually with:

```bash
scripts/bead-checkpoint-consistency-check.sh --verbose
```

Exit `0` means the read-only checks passed; exit `1` means a health check failed
or its output was invalid; exit `2` means a prerequisite is missing.

## Manual recovery

Use `bead doctor` and `bead sync status` to diagnose the store. If the native
database is missing or corrupt, preserve all of `.beads/` before restoring:

```bash
cp -a .beads ~/scratch/SEAM-beads-backup-<date>
bead sync status
bead restore --source .beads/checkpoint --generation <GEN> --actor <you>
```

Choose `<GEN>` from a verified `current.json` or `previous.json` pointer.
`bead restore` verifies the selected generation before changing live state.
Replacing a non-empty store requires an explicit decision about which state
wins and `--allow-non-empty`; inspect the displaced issue and event counts.
The full recovery procedure is in the home `CLAUDE.md` under “Recovering a
broken or fresh-clone workspace”.
