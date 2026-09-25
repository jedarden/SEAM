# Transient Starvation Backoff Implementation

**Date:** 2026-08-30  
**Bead:** seam-13646ada  
**Alternative for:** seam-7b904efc

## Problem

The original starvation alert system (`seam-7b904efc`) emitted alert beads immediately upon detecting starvation (no candidates but open beads exist). This created false positives when the condition was transient — for example, when a blocking bead was about to close and make new work available.

**Example:** The alert fired at 06:23:39, but the blocking bead closed at 06:33:34, making work available 10 minutes later. The alert was unnecessary noise.

## Solution

Implemented intelligent retry and exponential backoff in the Knot strand (`/home/coding/NEEDLE/src/strand/knot.rs`) to distinguish between transient gaps and persistent starvation.

### Key Changes

1. **Backoff Window Tracking**
   - Added `first_starvation_detected_at: Mutex<Option<DateTime<Utc>>>` field to `KnotStrand`
   - Tracks when starvation was first detected

2. **Backoff Logic**
   - When starvation first reaches the threshold (`cycle == exhaustion_threshold`):
     - Record detection timestamp
     - Enter backoff window (5-15 minutes, jittered)
     - **Do not emit telemetry yet**
   
   - On subsequent evaluations during backoff:
     - Check if still within backoff window
     - If yes: continue waiting, no telemetry
     - If no: emit starvation telemetry
   
   - If condition resolves (non-Invisible diagnosis) during backoff:
     - Log as "transient starvation gap resolved"
     - Clear backoff tracking
     - **No telemetry emitted**

### Configuration

The backoff window is **5-15 minutes** (jittered based on timestamp):
- Minimum: 5 minutes
- Maximum: 14 minutes (5 + 0-9 based on timestamp)
- This prevents thundering herd while providing adequate time for transient conditions to resolve

### Benefits

1. **Eliminates false positives** from timing issues
2. **Reduces alert noise** by giving transient conditions time to self-resolve
3. **Preserves real alerts** for genuine persistent starvation
4. **Maintains existing rate limiting** via `alert_cooldown_minutes`

### Testing

Added comprehensive tests:
- `starvation_backoff_window_delays_telemetry`: Verifies backoff prevents immediate telemetry
- `starvation_resolves_within_backoff_window`: Verifies transient gaps don't emit telemetry
- `starvation_persists_past_backoff_window_emits_telemetry`: Verifies persistent starvation still emits telemetry
- `backoff_tracking_cleared_after_telemetry_emitted`: Verifies cleanup after alert

All existing tests updated to account for new behavior.

## Files Modified

- `/home/coding/NEEDLE/src/strand/knot.rs`: Core implementation
  - Added `first_starvation_detected_at` field
  - Added backoff helper methods
  - Modified evaluation logic for backoff
  - Updated all tests

## Impact

This implementation addresses the root cause of `seam-7b904efc` by introducing a waiting period that allows transient conditions to self-correct before creating human-labeled alert beads. The system now logs "transient starvation gap resolved" instead of creating unnecessary alerts when work becomes available within the backoff window.
