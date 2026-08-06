# SEAM Route Fragment Schema Validation Summary

## Date: 2026-08-06

## Example File: `docs/notes/route-fragment-schema.json`

## Five SEAM Extensions Present

### 1. x-seam-schema (Fragment-root)
```json
"x-seam-schema": "v1"
```
✅ Present and valid - schema version marker

### 2. x-upstream-map (Fragment-root)
```json
"x-upstream-map": {
  "us-east": { ... },
  "eu-west": { ... },
  "ap-southeast": { ... }
}
```
✅ Present and valid - multi-instance map with all required fields:
- url (required)
- vaultPath (optional)
- injectAs (optional)
- target_host (optional extension)
- rewrite_path (optional extension)
- requiredScope (optional)

### 3. x-loop-guard (Operation-level)
```json
"x-loop-guard": {
  "max_iterations": 10,
  "backoff_ms": 100
}
```
✅ Present and valid on `/forecast` operation
✅ Present and valid on `/current` operation

### 4. x-cost-per-call (Operation-level)
```json
"x-cost-per-call": 0.001
```
✅ Present and valid on `/forecast` operation
✅ Present and valid on `/current` operation

### 5. x-quota (Operation-level)
```json
"x-quota": {
  "limit": 1000,
  "window_seconds": 3600
}
```
✅ Present and valid on `/forecast` operation
✅ Present and valid on `/current` operation

## Schema Compliance Check

### Required Fields (Fragment-root)
- ✅ `x-seam-schema` - "v1"
- ✅ `x-seam-owner` - "weather-service"
- ✅ `paths` - contains `/forecast` and `/current`

### Schema Validation Rules

#### Fragment-level constraints
- ✅ x-upstream-map requires x-instance-param - both present
- ✅ x-upstream-map excludes x-upstream - only map present
- ✅ Multi-instance map with valid instance keys
- ✅ All upstream entries have required url field

#### Operation-level constraints
- ✅ x-quota requires x-cost-per-call - constraint satisfied on both operations
- ✅ x-loop-guard has required max_iterations and backoff_ms
- ✅ x-quota has required limit and window_seconds
- ✅ x-cost-per-call is positive number

## Validation Rules for SEAM Extensions (bf-3n4z)

All five SEAM extensions have proper JSON Schema validation rules in `spec/route-fragment-schema.json`:

### 1. x-cost-per-call (Operation-level)
- **Rule**: minimum 0
- **Purpose**: Rejects negative costs
- **Location**: `spec/route-fragment-schema.json` line 179
- **Definition**:
  ```json
  "costPerCall": {
    "type": "number",
    "minimum": 0,
    "$comment": "Cost per call in arbitrary cost units. Must be non-negative (negative costs rejected)."
  }
  ```

### 2. x-quota.limit (Operation-level)
- **Rule**: minimum 1
- **Purpose**: Rejects zero or negative quota limits
- **Location**: `spec/route-fragment-schema.json` line 186
- **Definition**:
  ```json
  "limit": {
    "type": "integer",
    "minimum": 1,
    "$comment": "Maximum number of cost units allowed within the window. Must be at least 1."
  }
  ```

### 3. x-quota.window_seconds (Operation-level)
- **Rule**: minimum 1
- **Purpose**: Rejects zero or negative time windows
- **Location**: `spec/route-fragment-schema.json` line 187
- **Definition**:
  ```json
  "window_seconds": {
    "type": "integer",
    "minimum": 1,
    "$comment": "Time window in seconds. Must be positive (zero or negative rejected)."
  }
  ```

### 4. x-loop-guard.max_iterations (Operation-level)
- **Rule**: minimum 1
- **Purpose**: Rejects zero or negative iteration counts
- **Location**: `spec/route-fragment-schema.json` line 172
- **Definition**:
  ```json
  "max_iterations": {
    "type": "integer",
    "minimum": 1,
    "$comment": "Maximum number of iterations allowed before the loop guard intervenes."
  }
  ```

### 5. x-loop-guard.backoff_ms (Operation-level)
- **Rule**: minimum 0
- **Purpose**: Rejects negative backoff values (zero is allowed)
- **Location**: `spec/route-fragment-schema.json` line 173
- **Definition**:
  ```json
  "backoff_ms": {
    "type": "integer",
    "minimum": 0,
    "$comment": "Backoff delay in milliseconds between iterations. Zero means no backoff."
  }
  ```

## Validation Test Results

All validation tests pass as of 2026-08-06:

- ✅ Schema validates a correct example
- ✅ Schema rejects negative costs (-0.001)
- ✅ Schema rejects zero quota windows (0)
- ✅ Schema rejects zero max_iterations (0)
- ✅ Schema rejects negative backoff_ms (-100)
- ✅ Schema rejects zero quota limits (0)

**Test files**:
- `tests/valid-example.json` - Valid example with correct minimum values
- `tests/invalid-negative-cost.json` - Negative cost test
- `tests/invalid-zero-window.json` - Zero window test
- `tests/invalid-zero-max-iterations.json` - Zero max_iterations test
- `tests/invalid-negative-backoff.json` - Negative backoff test
- `tests/invalid-zero-quota-limit.json` - Zero quota limit test

**Verification script**: `validate-extension-tests.js` (run with `node validate-extension-tests.js`)

### Additional SEAM Extensions in Example

#### x-api-version
```json
"x-api-version": "v1"
```
✅ Present - API version marker

#### x-instance-param
```json
"x-instance-param": "region"
```
✅ Present - required with x-upstream-map

#### x-required-scope
```json
"x-required-scope": "weather:query:data"
```
✅ Present - fragment-root default scope

## Validation Summary

**Status**: ✅ **VALID**

The example fragment `docs/notes/route-fragment-schema.json` successfully demonstrates all five SEAM extensions:

1. **x-seam-schema** - Schema versioning
2. **x-upstream-map** - Multi-instance routing with new target_host and rewrite_path extensions
3. **x-loop-guard** - Loop protection with max_iterations and backoff_ms
4. **x-cost-per-call** - Cost tracking per operation
5. **x-quota** - Rate limiting per operation

## Ready for Production

The schema file at `spec/route-fragment-schema.json` is complete and ready for:
- **seam lint** validation (when implemented)
- **Runtime quarantine** enforcement
- **Production use** in SEAM gateway

## Extension Coverage

This example demonstrates the complete lifecycle of SEAM extensions:
- Fragment-level routing and configuration
- Multi-instance upstream management
- Operation-level protection and governance
- Cost tracking and rate limiting
- All constraints and cross-field validations satisfied