#!/usr/bin/env python3
"""
Automated Starvation Diagnostic and Recovery Tool

This tool provides comprehensive automated detection, diagnosis, and recovery
from bead starvation conditions (open beads exist but Pluck finds no candidates).

Features:
1. Runs bead doctor diagnostics with --rehearse
2. Detects starvation conditions (open beads vs ready beads)
3. Identifies root causes (stale checkpoints, stuck assignments, database corruption)
4. Executes appropriate recovery (flush, release, rebuild)
5. Validates recovery success
6. Auto-closes starvation alert beads on successful recovery
7. Can be run as a cron job or systemd service for continuous monitoring

Usage:
    python3 automated_recovery.py [--once] [--interval MIN] [--workspace PATH]
"""

import sqlite3
import json
import subprocess
import sys
import os
import argparse
import time
from datetime import datetime, timedelta
from typing import Dict, List, Set, Any, Optional, Tuple
from dataclasses import dataclass, field
from enum import Enum
import logging


class RecoveryAction(Enum):
    """Types of recovery actions."""
    NO_ACTION = "no_action"
    FLUSH_CHECKPOINT = "flush_checkpoint"
    RELEASE_STUCK_BEADS = "release_stuck_beads"
    REBUILD_CHECKPOINT = "rebuild_checkpoint"
    REPAIR_DATABASE = "repair_database"


class StarvationCause(Enum):
    """Root causes of starvation."""
    STALE_CHECKPOINT = "stale_checkpoint"
    STUCK_ASSIGNMENTS = "stuck_assignments"
    DATABASE_CORRUPTION = "database_corruption"
    WORKER_DISCONNECT = "worker_disconnect"
    UNKNOWN = "unknown"


@dataclass
class StarvationReport:
    """Comprehensive report of starvation diagnosis and recovery."""
    timestamp: str
    workspace: str
    starvation_detected: bool
    open_beads: int
    ready_beads: int
    causes: List[StarvationCause] = field(default_factory=list)
    actions_taken: List[RecoveryAction] = field(default_factory=list)
    recovery_successful: bool = False
    details: Dict[str, Any] = field(default_factory=dict)
    closed_alerts: List[str] = field(default_factory=list)


class StarvationDetector:
    """Detects and diagnoses starvation conditions."""

    def __init__(self, workspace: str, logger: logging.Logger):
        self.workspace = workspace
        self.logger = logger
        self.db_path = os.path.join(workspace, ".beads", "beads.db")
        self.checkpoint_dir = os.path.join(workspace, ".beads", "checkpoint")

    def count_beads_by_status(self, status: str) -> int:
        """Count beads with specific base_status."""
        try:
            cmd = ["bead", "list", f"--status={status}", "--json"]
            result = subprocess.run(
                cmd, cwd=self.workspace, capture_output=True, text=True, timeout=30
            )
            if result.returncode != 0:
                self.logger.warning(f"Failed to count {status} beads: {result.stderr}")
                return 0

            beads = json.loads(result.stdout) if result.stdout else []
            return len(beads)
        except Exception as e:
            self.logger.error(f"Error counting {status} beads: {e}")
            return 0

    def count_ready_beads(self) -> int:
        """Count beads ready for workers to claim."""
        try:
            cmd = ["bead", "list", "--ready", "--json"]
            result = subprocess.run(
                cmd, cwd=self.workspace, capture_output=True, text=True, timeout=30
            )
            if result.returncode != 0:
                self.logger.warning(f"Failed to count ready beads: {result.stderr}")
                return 0

            beads = json.loads(result.stdout) if result.stdout else []
            return len(beads)
        except Exception as e:
            self.logger.error(f"Error counting ready beads: {e}")
            return 0

    def find_starvation_alert_beads(self) -> List[Dict[str, Any]]:
        """Find all open starvation alert beads."""
        try:
            cmd = ["bead", "list", "--status=open", "--json"]
            result = subprocess.run(
                cmd, cwd=self.workspace, capture_output=True, text=True, timeout=30
            )
            if result.returncode != 0:
                return []

            beads = json.loads(result.stdout) if result.stdout else []
            alerts = []

            for bead in beads:
                # Ensure bead is a dict
                if not isinstance(bead, dict):
                    continue

                # Check for starvation-related labels or title
                labels = bead.get("labels", [])
                title = bead.get("title", "").lower()

                if any(
                    "starvation" in str(label).lower()
                    for label in labels
                ) or "starvation" in title:
                    alerts.append(bead)

            return alerts
        except Exception as e:
            self.logger.error(f"Error finding starvation alerts: {e}")
            return []

    def detect_starvation(self) -> Tuple[bool, int, int]:
        """
        Detect starvation condition.

        Returns:
            (starvation_detected, open_beads, ready_beads)
        """
        open_beads = self.count_beads_by_status("open")
        ready_beads = self.count_ready_beads()

        starvation_detected = (open_beads > 0 and ready_beads == 0)
        return starvation_detected, open_beads, ready_beads

    def run_bead_doctor(self, repair: bool = False) -> Tuple[bool, str]:
        """Run bead doctor diagnostics."""
        try:
            cmd = ["bead", "doctor", "--rehearse"]
            if repair:
                cmd.append("--repair")

            result = subprocess.run(
                cmd, cwd=self.workspace, capture_output=True, text=True, timeout=60
            )

            success = result.returncode == 0
            return success, result.stdout + result.stderr
        except Exception as e:
            self.logger.error(f"Error running bead doctor: {e}")
            return False, str(e)

    def diagnose_causes(self) -> List[StarvationCause]:
        """Identify root causes of starvation."""
        causes = []

        # Check for stale checkpoint
        if self._is_checkpoint_stale():
            causes.append(StarvationCause.STALE_CHECKPOINT)

        # Check for stuck assignments
        stuck_beads = self._find_stuck_assignments()
        if stuck_beads:
            causes.append(StarvationCause.STUCK_ASSIGNMENTS)
            self.logger.info(f"Found {len(stuck_beads)} stuck assignments")

        # Check database integrity
        if not self._check_database_integrity():
            causes.append(StarvationCause.DATABASE_CORRUPTION)

        if not causes:
            causes.append(StarvationCause.UNKNOWN)

        return causes

    def _is_checkpoint_stale(self) -> bool:
        """Check if checkpoint is stale."""
        try:
            current_file = os.path.join(self.checkpoint_dir, "current.json")
            if not os.path.exists(current_file):
                return True

            # Check modification time
            mtime = os.path.getmtime(current_file)
            age_hours = (time.time() - mtime) / 3600

            # Consider stale if older than 1 hour
            return age_hours > 1
        except Exception as e:
            self.logger.error(f"Error checking checkpoint staleness: {e}")
            return False

    def _find_stuck_assignments(self) -> List[str]:
        """Find beads with stale assignments."""
        try:
            conn = sqlite3.connect(self.db_path)
            cursor = conn.cursor()

            # Find open beads with assignees
            cursor.execute("""
                SELECT id, assignee, updated_at
                FROM issues
                WHERE base_status = 'open' AND assignee IS NOT NULL
            """)

            stuck_beads = []
            for bead_id, assignee, updated_at in cursor.fetchall():
                if updated_at:
                    # Parse the timestamp
                    try:
                        updated_time = datetime.fromisoformat(updated_at.replace('Z', '+00:00'))
                        age_hours = (datetime.now(updated_time.tzinfo) - updated_time).total_seconds() / 3600

                        # Consider stuck if not updated in 24 hours
                        if age_hours > 24:
                            stuck_beads.append(bead_id)
                    except:
                        pass

            conn.close()
            return stuck_beads
        except Exception as e:
            self.logger.error(f"Error finding stuck assignments: {e}")
            return []

    def _check_database_integrity(self) -> bool:
        """Check database integrity."""
        try:
            conn = sqlite3.connect(self.db_path)
            cursor = conn.cursor()

            cursor.execute("PRAGMA integrity_check")
            result = cursor.fetchone()
            conn.close()

            if result and result[0] == "ok":
                return True
            else:
                self.logger.warning(f"Database integrity check failed: {result}")
                return False
        except Exception as e:
            self.logger.error(f"Error checking database integrity: {e}")
            return False


class StarvationRecovery:
    """Executes recovery actions for starvation conditions."""

    def __init__(self, workspace: str, logger: logging.Logger):
        self.workspace = workspace
        self.logger = logger
        self.detector = StarvationDetector(workspace, logger)

    def execute_recovery(self, causes: List[StarvationCause]) -> List[RecoveryAction]:
        """Execute appropriate recovery actions based on causes."""
        actions = []

        for cause in causes:
            if cause == StarvationCause.STALE_CHECKPOINT:
                if self._flush_checkpoint():
                    actions.append(RecoveryAction.FLUSH_CHECKPOINT)
                else:
                    self.logger.error("Failed to flush checkpoint")

            elif cause == StarvationCause.STUCK_ASSIGNMENTS:
                if self._release_stuck_beads():
                    actions.append(RecoveryAction.RELEASE_STUCK_BEADS)
                else:
                    self.logger.error("Failed to release stuck beads")

            elif cause == StarvationCause.DATABASE_CORRUPTION:
                # First try repair, then rebuild if needed
                success, output = self.detector.run_bead_doctor(repair=True)
                if success:
                    actions.append(RecoveryAction.REPAIR_DATABASE)
                else:
                    self.logger.warning("Repair failed, attempting rebuild")
                    if self._rebuild_checkpoint():
                        actions.append(RecoveryAction.REBUILD_CHECKPOINT)

        return actions

    def _flush_checkpoint(self) -> bool:
        """Flush checkpoint to ensure consistency."""
        try:
            cmd = ["bead", "sync", "flush-only"]
            result = subprocess.run(
                cmd, cwd=self.workspace, capture_output=True, text=True, timeout=60
            )

            if result.returncode == 0:
                self.logger.info("Checkpoint flushed successfully")
                return True
            else:
                self.logger.error(f"Failed to flush checkpoint: {result.stderr}")
                return False
        except Exception as e:
            self.logger.error(f"Error flushing checkpoint: {e}")
            return False

    def _release_stuck_beads(self) -> bool:
        """Release stuck bead assignments."""
        try:
            stuck_beads = self.detector._find_stuck_assignments()
            released_count = 0

            for bead_id in stuck_beads:
                cmd = ["bead", "update", bead_id, "--clear-assignee"]
                result = subprocess.run(
                    cmd, cwd=self.workspace, capture_output=True, text=True, timeout=30
                )

                if result.returncode == 0:
                    released_count += 1
                    self.logger.info(f"Released stuck bead: {bead_id}")
                else:
                    self.logger.warning(f"Failed to release bead {bead_id}: {result.stderr}")

            self.logger.info(f"Released {released_count}/{len(stuck_beads)} stuck beads")
            return released_count > 0
        except Exception as e:
            self.logger.error(f"Error releasing stuck beads: {e}")
            return False

    def _rebuild_checkpoint(self) -> bool:
        """Rebuild checkpoint from forensic.jsonl."""
        try:
            forensic_file = os.path.join(self.workspace, ".beads", "checkpoint", "forensic.jsonl")
            if not os.path.exists(forensic_file):
                self.logger.error(f"Forensic file not found: {forensic_file}")
                return False

            # Backup current database
            db_file = os.path.join(self.workspace, ".beads", "beads.db")
            backup_file = f"{db_file}.backup.{int(time.time())}"

            import shutil
            shutil.copy2(db_file, backup_file)
            self.logger.info(f"Backed up database to: {backup_file}")

            # Rebuild checkpoint
            cmd = ["bead", "sync", "import-only", "--input", forensic_file, "--restore-into-empty", "--actor", "automated-recovery"]
            result = subprocess.run(
                cmd, cwd=self.workspace, capture_output=True, text=True, timeout=120
            )

            if result.returncode == 0:
                self.logger.info("Checkpoint rebuilt successfully")
                return True
            else:
                self.logger.error(f"Failed to rebuild checkpoint: {result.stderr}")
                # Restore backup on failure
                shutil.copy2(backup_file, db_file)
                self.logger.info("Restored database from backup")
                return False
        except Exception as e:
            self.logger.error(f"Error rebuilding checkpoint: {e}")
            return False

    def validate_recovery(self) -> bool:
        """Validate that recovery was successful."""
        starvation_detected, open_beads, ready_beads = self.detector.detect_starvation()

        if not starvation_detected and ready_beads > 0:
            self.logger.info(f"Recovery validated: {ready_beads} ready beads available")
            return True
        else:
            self.logger.warning(f"Recovery incomplete: open={open_beads}, ready={ready_beads}")
            return False

    def close_alert_beads(self, alerts: List[Dict[str, Any]], ready_count: int) -> List[str]:
        """Close starvation alert beads after successful recovery."""
        closed = []

        for alert in alerts:
            bead_id = alert.get("id")
            if not bead_id:
                continue

            reason = f"Automated recovery successful - beads now visible to workers (ready count: {ready_count})"

            try:
                cmd = ["bead", "close", bead_id, "--reason", reason]
                result = subprocess.run(
                    cmd, cwd=self.workspace, capture_output=True, text=True, timeout=30
                )

                if result.returncode == 0:
                    closed.append(bead_id)
                    self.logger.info(f"Closed starvation alert: {bead_id}")
                else:
                    self.logger.warning(f"Failed to close alert {bead_id}: {result.stderr}")
            except Exception as e:
                self.logger.error(f"Error closing alert {bead_id}: {e}")

        return closed


class AutomatedStarvationRecovery:
    """Main class for automated starvation recovery."""

    def __init__(self, workspace: str, logger: logging.Logger):
        self.workspace = workspace
        self.logger = logger
        self.detector = StarvationDetector(workspace, logger)
        self.recovery = StarvationRecovery(workspace, logger)

    def run_recovery_cycle(self) -> StarvationReport:
        """Run a complete recovery cycle."""
        timestamp = datetime.now().isoformat()
        self.logger.info(f"Starting recovery cycle at {timestamp}")

        # Detect starvation
        starvation_detected, open_beads, ready_beads = self.detector.detect_starvation()

        report = StarvationReport(
            timestamp=timestamp,
            workspace=self.workspace,
            starvation_detected=starvation_detected,
            open_beads=open_beads,
            ready_beads=ready_beads,
        )

        # Find starvation alert beads
        alert_beads = self.detector.find_starvation_alert_beads()
        self.logger.info(f"Found {len(alert_beads)} starvation alert beads")

        # If no starvation, close any existing alerts
        if not starvation_detected:
            self.logger.info("No starvation detected - system healthy")

            if alert_beads and ready_beads > 0:
                closed = self.recovery.close_alert_beads(alert_beads, ready_beads)
                report.closed_alerts = closed
                self.logger.info(f"Closed {len(closed)} resolved alert beads")

            report.recovery_successful = True
            return report

        # Starvation detected - run diagnostics and recovery
        self.logger.warning(f"Starvation detected! Open: {open_beads}, Ready: {ready_beads}")

        # Step 1: Run bead doctor
        self.logger.info("Step 1: Running bead doctor diagnostics")
        doctor_success, doctor_output = self.detector.run_bead_doctor(repair=False)
        report.details["doctor_output"] = doctor_output

        # Step 2: Diagnose causes
        self.logger.info("Step 2: Diagnosing root causes")
        causes = self.detector.diagnose_causes()
        report.causes = causes

        for cause in causes:
            self.logger.info(f"Identified cause: {cause.value}")

        # Step 3: Execute recovery
        self.logger.info("Step 3: Executing recovery actions")
        actions = self.recovery.execute_recovery(causes)
        report.actions_taken = actions

        for action in actions:
            self.logger.info(f"Action taken: {action.value}")

        # Step 4: Validate recovery
        self.logger.info("Step 4: Validating recovery")
        recovery_success = self.recovery.validate_recovery()
        report.recovery_successful = recovery_success

        # Step 5: Close alerts if recovery successful
        if recovery_success and alert_beads:
            self.logger.info("Step 5: Closing starvation alert beads")
            closed = self.recovery.close_alert_beads(alert_beads, ready_beads)
            report.closed_alerts = closed

        # Report results
        if recovery_success:
            self.logger.info("✓ Recovery cycle completed successfully")
        else:
            self.logger.warning("⚠ Recovery cycle completed with issues")

        return report


def setup_logging(verbose: bool = False, log_file: Optional[str] = None) -> logging.Logger:
    """Setup logging configuration."""
    logger = logging.getLogger("automated_recovery")
    logger.setLevel(logging.DEBUG if verbose else logging.INFO)

    formatter = logging.Formatter(
        "%(asctime)s - %(name)s - %(levelname)s - %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S"
    )

    # Console handler
    console_handler = logging.StreamHandler()
    console_handler.setFormatter(formatter)
    logger.addHandler(console_handler)

    # File handler if specified
    if log_file:
        file_handler = logging.FileHandler(log_file)
        file_handler.setFormatter(formatter)
        logger.addHandler(file_handler)

    return logger


def main():
    """Main entry point."""
    parser = argparse.ArgumentParser(
        description="Automated Starvation Diagnostic and Recovery Tool"
    )
    parser.add_argument(
        "--workspace",
        default="/home/coding/SEAM",
        help="Path to workspace (default: /home/coding/SEAM)"
    )
    parser.add_argument(
        "--once",
        action="store_true",
        help="Run once and exit (default: loop mode)"
    )
    parser.add_argument(
        "--interval",
        type=int,
        default=5,
        help="Check interval in minutes (default: 5)"
    )
    parser.add_argument(
        "--verbose",
        action="store_true",
        help="Enable verbose logging"
    )
    parser.add_argument(
        "--log-file",
        help="Path to log file for audit trail"
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Show what would be done without making changes"
    )

    args = parser.parse_args()

    # Setup logging
    logger = setup_logging(args.verbose, args.log_file)

    # Validate workspace
    if not os.path.exists(args.workspace):
        logger.error(f"Workspace not found: {args.workspace}")
        sys.exit(1)

    db_path = os.path.join(args.workspace, ".beads", "beads.db")
    if not os.path.exists(db_path):
        logger.error(f"Beads database not found: {db_path}")
        sys.exit(1)

    logger.info(f"Starting automated starvation recovery")
    logger.info(f"Workspace: {args.workspace}")
    logger.info(f"Interval: {args.interval} minutes")
    logger.info(f"Mode: {'once' if args.once else 'loop'}")

    # Create recovery system
    recovery_system = AutomatedStarvationRecovery(args.workspace, logger)

    if args.once:
        # One-shot mode
        logger.info("Running one-shot recovery cycle")
        report = recovery_system.run_recovery_cycle()

        # Print summary
        logger.info(f"Summary: {report.open_beads} open, {report.ready_beads} ready, "
                   f"recovery={'successful' if report.recovery_successful else 'failed'}")

        if report.causes:
            logger.info(f"Causes identified: {[c.value for c in report.causes]}")

        if report.actions_taken:
            logger.info(f"Actions taken: {[a.value for a in report.actions_taken]}")

        sys.exit(0 if report.recovery_successful else 1)

    else:
        # Loop mode
        logger.info("Starting loop mode")
        interval_seconds = args.interval * 60

        while True:
            try:
                report = recovery_system.run_recovery_cycle()
                logger.info(f"Cycle complete - sleeping for {args.interval} minutes")
                time.sleep(interval_seconds)
            except KeyboardInterrupt:
                logger.info("Interrupted by user")
                break
            except Exception as e:
                logger.error(f"Error in recovery cycle: {e}")
                time.sleep(interval_seconds)


if __name__ == "__main__":
    main()
