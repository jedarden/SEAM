#!/usr/bin/env python3
"""
Bead Visibility Diagnostic and Repair Daemon

This daemon periodically queries the bead database using multiple methods
(CLI, direct SQLite, checkpoint) and compares results to detect visibility bugs.

When discrepancies are detected, the daemon:
1. Logs the visibility bug with timestamps
2. Runs `bead sync flush-only` to refresh the checkpoint
3. Queries all strategies and consolidates results
4. Updates any stale or inconsistent bead states
5. Creates a diagnostic bead with full details if the issue persists

Run as a systemd service with configurable check intervals (default 5 minutes).
"""

import sqlite3
import json
import subprocess
import sys
import os
import argparse
import logging
from datetime import datetime, timedelta
from pathlib import Path
from typing import Dict, List, Tuple, Optional, Any
import time


class BeadVisibilityChecker:
    """Checks bead visibility across multiple query strategies."""

    def __init__(self, workspace: str, log_dir: Path):
        self.workspace = Path(workspace)
        self.log_dir = log_dir
        self.db_path = self.workspace / ".beads" / "beads.db"
        self.checkpoint_dir = self.workspace / ".beads" / "checkpoint"
        self.logger = self._setup_logger()

    def _setup_logger(self) -> logging.Logger:
        """Setup logging to both file and stdout."""
        logger = logging.getLogger("bead_visibility")
        logger.setLevel(logging.DEBUG)

        # File handler
        log_file = self.log_dir / f"visibility-diagnostic-{datetime.now().strftime('%Y%m%d-%H%M%S')}.log"
        fh = logging.FileHandler(log_file)
        fh.setLevel(logging.DEBUG)
        fh.setFormatter(logging.Formatter(
            '%(asctime)s - %(name)s - %(levelname)s - %(message)s'
        ))

        # Console handler
        ch = logging.StreamHandler()
        ch.setLevel(logging.INFO)
        ch.setFormatter(logging.Formatter('%(levelname)s: %(message)s'))

        logger.addHandler(fh)
        logger.addHandler(ch)

        return logger

    def query_cli_open(self) -> List[str]:
        """Query open beads via CLI."""
        try:
            result = subprocess.run(
                ["bead", "list", "--status", "open", "--json"],
                cwd=self.workspace,
                capture_output=True,
                text=True,
                timeout=30
            )
            if result.returncode == 0:
                beads = json.loads(result.stdout)
                if isinstance(beads, list):
                    return [b["id"] for b in beads]
                elif isinstance(beads, dict) and "id" in beads:
                    return [beads["id"]]
        except (subprocess.TimeoutExpired, json.JSONDecodeError, KeyError) as e:
            self.logger.warning(f"CLI open query failed: {e}")
        return []

    def query_cli_ready(self) -> List[str]:
        """Query ready beads via CLI."""
        try:
            result = subprocess.run(
                ["bead", "list", "--ready", "--json"],
                cwd=self.workspace,
                capture_output=True,
                text=True,
                timeout=30
            )
            if result.returncode == 0:
                beads = json.loads(result.stdout)
                if isinstance(beads, list):
                    return [b["id"] for b in beads]
                elif isinstance(beads, dict) and "id" in beads:
                    return [beads["id"]]
        except (subprocess.TimeoutExpired, json.JSONDecodeError, KeyError) as e:
            self.logger.warning(f"CLI ready query failed: {e}")
        return []

    def query_sqlite_open(self) -> List[str]:
        """Query open beads directly from SQLite."""
        try:
            conn = sqlite3.connect(self.db_path)
            cursor = conn.cursor()
            cursor.execute(
                "SELECT id FROM issues WHERE base_status = 'open'"
            )
            rows = cursor.fetchall()
            conn.close()
            return [row[0] for row in rows]
        except sqlite3.Error as e:
            self.logger.error(f"SQLite open query failed: {e}")
            return []

    def query_sqlite_ready(self) -> List[str]:
        """Query ready beads directly from SQLite."""
        try:
            conn = sqlite3.connect(self.db_path)
            cursor = conn.cursor()
            # Ready = open + unassigned + not manually blocked
            cursor.execute(
                """SELECT id FROM issues
                   WHERE base_status = 'open'
                   AND assignee IS NULL
                   AND manual_blocked = 0"""
            )
            rows = cursor.fetchall()
            conn.close()
            return [row[0] for row in rows]
        except sqlite3.Error as e:
            self.logger.error(f"SQLite ready query failed: {e}")
            return []

    def query_checkpoint_open(self) -> List[str]:
        """Query open beads from checkpoint file."""
        try:
            checkpoint_path = self.checkpoint_dir / "forensic.jsonl"
            if not checkpoint_path.exists():
                self.logger.warning(f"Checkpoint not found: {checkpoint_path}")
                return []

            open_ids = []
            with open(checkpoint_path, 'r') as f:
                for line in f:
                    try:
                        obj = json.loads(line)
                        if obj.get("type") == "issue" and obj.get("base_status") == "open":
                            open_ids.append(obj.get("id"))
                    except json.JSONDecodeError:
                        continue
            return open_ids
        except IOError as e:
            self.logger.error(f"Checkpoint query failed: {e}")
            return []

    def flush_checkpoint(self) -> bool:
        """Run bead sync flush-only to refresh checkpoint."""
        try:
            self.logger.info("Running: bead sync flush-only")
            result = subprocess.run(
                ["bead", "sync", "flush-only"],
                cwd=self.workspace,
                capture_output=True,
                text=True,
                timeout=60
            )
            if result.returncode == 0:
                self.logger.info("Checkpoint flushed successfully")
                return True
            else:
                self.logger.error(f"Flush failed: {result.stderr}")
                return False
        except subprocess.TimeoutExpired:
            self.logger.error("Flush command timed out")
            return False

    def get_bead_details(self, bead_id: str) -> Optional[Dict[str, Any]]:
        """Get full details of a bead from SQLite."""
        try:
            conn = sqlite3.connect(self.db_path)
            conn.row_factory = sqlite3.Row
            cursor = conn.cursor()
            cursor.execute(
                "SELECT * FROM issues WHERE id = ?",
                (bead_id,)
            )
            row = cursor.fetchone()
            conn.close()
            if row:
                return dict(row)
        except sqlite3.Error as e:
            self.logger.error(f"Failed to get details for {bead_id}: {e}")
        return None

    def create_diagnostic_bead(
        self,
        discrepancy_type: str,
        details: Dict[str, Any]
    ) -> bool:
        """Create a diagnostic bead for persistent issues."""
        timestamp = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        title = f"[Visibility Bug] {discrepancy_type} detected at {timestamp}"

        description = f"""## Visibility Bug Detected

**Type:** {discrepancy_type}
**Timestamp:** {timestamp}
**Workspace:** {self.workspace}

### Detection Details

{json.dumps(details, indent=2)}

### Query Results

```json
{json.dumps(details.get("query_results", {}), indent=2)}
```

### Recommended Actions

1. Verify bead database integrity
2. Check checkpoint synchronization
3. Review assignee and manual_blocked flags
4. Consider running `bead doctor --repair`

### Automated Response

This bead was automatically created by the bead visibility diagnostic daemon.
If this issue persists, manual intervention may be required.

---
*Generated by tools/bead_visibility_diagnostic.py*
"""

        try:
            self.logger.info(f"Creating diagnostic bead: {title}")
            result = subprocess.run(
                ["bead", "create", "--title", title, "--priority", "3", "--issue-type", "task"],
                cwd=self.workspace,
                capture_output=True,
                text=True,
                timeout=30,
                input=description
            )
            if result.returncode == 0:
                # Extract bead ID from output
                output = result.stdout + result.stderr
                if "Created" in output or "id:" in output:
                    self.logger.info(f"Diagnostic bead created successfully")
                    return True
            self.logger.error(f"Failed to create diagnostic bead: {result.stderr}")
            return False
        except subprocess.TimeoutExpired:
            self.logger.error("Bead creation timed out")
            return False

    def detect_and_repair(self) -> Dict[str, Any]:
        """Main detection and repair logic."""
        timestamp = datetime.now().isoformat()
        self.logger.info(f"Starting visibility check at {timestamp}")

        results = {
            "timestamp": timestamp,
            "discrepancies": [],
            "repairs_attempted": [],
            "diagnostic_beads_created": []
        }

        # Query all strategies
        cli_open = set(self.query_cli_open())
        cli_ready = set(self.query_cli_ready())
        sqlite_open = set(self.query_sqlite_open())
        sqlite_ready = set(self.query_sqlite_ready())
        checkpoint_open = set(self.query_checkpoint_open())

        query_results = {
            "cli_open_count": len(cli_open),
            "cli_ready_count": len(cli_ready),
            "sqlite_open_count": len(sqlite_open),
            "sqlite_ready_count": len(sqlite_ready),
            "checkpoint_open_count": len(checkpoint_open)
        }

        self.logger.info(f"Query results: {query_results}")

        # Discrepancy 1: CLI returns 0 open but SQLite has records
        if len(cli_open) == 0 and len(sqlite_open) > 0:
            discrepancy = {
                "type": "cli_open_zero_sqlite_positive",
                "cli_open_count": len(cli_open),
                "sqlite_open_count": len(sqlite_open),
                "sqlite_open_ids": list(sqlite_open)[:10]  # First 10
            }
            results["discrepancies"].append(discrepancy)
            self.logger.warning(f"DETECTED: CLI open=0 but SQLite has {len(sqlite_open)}")

            # Attempt repair
            self.logger.info("Attempting repair: flush checkpoint")
            if self.flush_checkpoint():
                results["repairs_attempted"].append("checkpoint_flush")

                # Re-check
                cli_open_after = set(self.query_cli_open())
                if len(cli_open_after) > 0:
                    self.logger.info(f"Repair successful: {len(cli_open_after)} beads now visible")
                else:
                    self.logger.error("Repair failed: beads still invisible")
                    # Create diagnostic bead
                    if self.create_diagnostic_bead(
                        "CLI open returns zero despite SQLite records",
                        {"query_results": query_results, "discrepancy": discrepancy}
                    ):
                        results["diagnostic_beads_created"].append(discrepancy["type"])

        # Discrepancy 2: CLI returns 0 ready but SQLite has ready records
        if len(cli_ready) == 0 and len(sqlite_ready) > 0:
            discrepancy = {
                "type": "cli_ready_zero_sqlite_positive",
                "cli_ready_count": len(cli_ready),
                "sqlite_ready_count": len(sqlite_ready),
                "sqlite_ready_ids": list(sqlite_ready)[:10]
            }
            results["discrepancies"].append(discrepancy)
            self.logger.warning(f"DETECTED: CLI ready=0 but SQLite has {len(sqlite_ready)}")

            # Attempt repair
            self.logger.info("Attempting repair: flush checkpoint")
            if self.flush_checkpoint():
                results["repairs_attempted"].append("checkpoint_flush")

                cli_ready_after = set(self.query_cli_ready())
                if len(cli_ready_after) > 0:
                    self.logger.info(f"Repair successful: {len(cli_ready_after)} ready beads now visible")
                else:
                    self.logger.error("Repair failed: ready beads still invisible")

        # Discrepancy 3: Checkpoint out of sync with SQLite
        if len(checkpoint_open) > 0 and abs(len(checkpoint_open) - len(sqlite_open)) > 5:
            discrepancy = {
                "type": "checkpoint_desync",
                "checkpoint_open_count": len(checkpoint_open),
                "sqlite_open_count": len(sqlite_open),
                "diff": abs(len(checkpoint_open) - len(sqlite_open))
            }
            results["discrepancies"].append(discrepancy)
            self.logger.warning(
                f"DETECTED: Checkpoint desync - checkpoint={len(checkpoint_open)} "
                f"sqlite={len(sqlite_open)} (diff={discrepancy['diff']})"
            )

            # Attempt repair
            self.logger.info("Attempting repair: flush checkpoint")
            if self.flush_checkpoint():
                results["repairs_attempted"].append("checkpoint_flush")

        # Discrepancy 4: Beads stuck in assigned-but-open state
        stuck_assigned = []
        conn = sqlite3.connect(self.db_path)
        cursor = conn.cursor()
        cursor.execute(
            """SELECT id, assignee FROM issues
               WHERE base_status = 'open'
               AND assignee IS NOT NULL
               AND assignee != ''"""
        )
        for row in cursor.fetchall():
            stuck_assigned.append({"id": row[0], "assignee": row[1]})
        conn.close()

        if len(stuck_assigned) > 0:
            discrepancy = {
                "type": "assigned_but_open",
                "count": len(stuck_assigned),
                "examples": stuck_assigned[:5]
            }
            results["discrepancies"].append(discrepancy)
            self.logger.warning(f"DETECTED: {len(stuck_assigned)} beads in assigned-but-open state")

            # Attempt repair for each stuck bead
            for bead in stuck_assigned[:10]:  # Limit to 10 to avoid spam
                bead_id = bead["id"]
                self.logger.info(f"Attempting repair for {bead_id}: release")
                try:
                    result = subprocess.run(
                        ["bead", "release", bead_id],
                        cwd=self.workspace,
                        capture_output=True,
                        text=True,
                        timeout=30
                    )
                    if result.returncode == 0:
                        self.logger.info(f"Released stuck bead: {bead_id}")
                        results["repairs_attempted"].append(f"release_{bead_id}")
                    else:
                        self.logger.warning(f"Failed to release {bead_id}: {result.stderr}")
                except subprocess.TimeoutExpired:
                    self.logger.warning(f"Release command timed out for {bead_id}")

        # Save results
        results_file = self.log_dir / f"check-results-{datetime.now().strftime('%Y%m%d-%H%M%S')}.json"
        with open(results_file, 'w') as f:
            json.dump(results, f, indent=2)
        self.logger.info(f"Results saved to {results_file}")

        return results


def main():
    """Main daemon entry point."""
    parser = argparse.ArgumentParser(
        description="Bead Visibility Diagnostic and Repair Daemon"
    )
    parser.add_argument(
        "--workspace",
        default="/home/coding/SEAM",
        help="Path to workspace directory"
    )
    parser.add_argument(
        "--interval",
        type=int,
        default=5,
        help="Check interval in minutes (default: 5)"
    )
    parser.add_argument(
        "--once",
        action="store_true",
        help="Run once and exit (don't loop)"
    )
    parser.add_argument(
        "--log-dir",
        default="/tmp/bead-visibility-diagnostics",
        help="Directory for log files"
    )
    args = parser.parse_args()

    # Create log directory
    log_dir = Path(args.log_dir)
    log_dir.mkdir(parents=True, exist_ok=True)

    # Initialize checker
    checker = BeadVisibilityChecker(args.workspace, log_dir)

    print(f"Bead Visibility Diagnostic Daemon")
    print(f"Workspace: {args.workspace}")
    print(f"Check interval: {args.interval} minutes")
    print(f"Log directory: {log_dir}")
    print()

    if args.once:
        print("Running one-shot check...")
        results = checker.detect_and_repair()
        print(f"Check complete. Discrepancies found: {len(results['discrepancies'])}")
        print(f"Repairs attempted: {len(results['repairs_attempted'])}")
        print(f"Diagnostic beads created: {len(results['diagnostic_beads_created'])}")
        return 0

    # Daemon mode
    print("Starting daemon mode. Press Ctrl+C to stop.")
    print()

    try:
        while True:
            results = checker.detect_and_repair()

            # Log summary
            discrepancy_count = len(results['discrepancies'])
            repair_count = len(results['repairs_attempted'])
            bead_count = len(results['diagnostic_beads_created'])

            if discrepancy_count > 0:
                print(f"[{datetime.now()}] Found {discrepancy_count} discrepancies, "
                      f"attempted {repair_count} repairs, created {bead_count} diagnostic beads")
            else:
                print(f"[{datetime.now()}] No discrepancies detected - system healthy")

            # Sleep until next check
            sleep_seconds = args.interval * 60
            time.sleep(sleep_seconds)

    except KeyboardInterrupt:
        print("\nDaemon stopped by user")
        return 0


if __name__ == "__main__":
    sys.exit(main())
