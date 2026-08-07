# Task bf-19bc: SEAM Policy and Metadata Extensions

## Summary

Verified that the SEAM route fragment schema at `spec/route-fragment-schema.json` includes all five required policy and metadata extensions with proper validation.

## Extensions Implemented

1. **x-seam-schema** (string)
   - Version marker (e.g., 'v1')
   - Defined at line 25 of schema
   - Required field at fragment root

2. **x-upstream-map** (object)
   - Contains `target_host` and `rewrite_path` fields per instance
   - Defined in `upstreamMapEntry` at lines 201-222
   - Supports multi-instance routing with instance parameter selection

3. **x-loop-guard** (object)
   - Fields: `max_iterations` (integer, minimum 1), `backoff_ms` (integer, minimum 0)
   - Defined in `loopGuard` at lines 168-176
   - Applied at operation level

4. **x-cost-per-call** (number)
   - Cost units, minimum 0 (negative costs rejected)
   - Defined in `costPerCall` at lines 177-181
   - Required when x-quota is present

5. **x-quota** (object)
   - Fields: `limit` (integer, minimum 1), `window_seconds` (integer, minimum 1)
   - Defined in `quota` at lines 182-190
   - Zero/negative limits and windows rejected

## Validation Results

All acceptance criteria met:

✓ Schema validates all five policy extensions with correct types
✓ Rejects invalid values:
  - Negative costs: rejected (minimum 0)
  - Zero quota limits: rejected (minimum 1)
  - Zero quota windows: rejected (minimum 1)
  - Zero max iterations: rejected (minimum 1)
✓ Complete example fragment with all extensions passes validation
✓ Schema ready for seam lint and runtime quarantine

## Example Fragment

Complete example demonstrating all five extensions:
- Location: `docs/notes/route-fragment-schema.json`
- Includes x-seam-schema: v1
- Includes x-upstream-map with target_host and rewrite_path for 3 instances
- Includes x-loop-guard on operations (max_iterations, backoff_ms)
- Includes x-cost-per-call (0.001, 0.0005)
- Includes x-quota (limit, window_seconds)

## Schema Location

Authoritative schema: `spec/route-fragment-schema.json`
Complete example: `docs/notes/route-fragment-schema.json`
