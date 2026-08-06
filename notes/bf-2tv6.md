# Task bf-2tv6: x-cost-per-call Validation

## Task
Add JSON Schema validation rule for x-cost-per-call extension to reject negative costs.

## Finding
The validation rule is **already present** in the SEAM schema at `spec/route-fragment-schema.json`:

```json
"costPerCall": {
  "type": "number",
  "minimum": 0,
  "$comment": "Cost per call in arbitrary cost units. Must be non-negative (negative costs rejected)."
}
```

## Verification

### 1. Schema Rule Check ✅
- **Location**: `spec/route-fragment-schema.json:177-181`
- **Type**: `number`
- **Minimum**: `0`
- **Comment**: "Cost per call in arbitrary cost units. Must be non-negative (negative costs rejected)."

### 2. Validation Tests ✅
All tests pass with the existing validation:

**Test 1: Valid x-cost-per-call: 0**
- File: `tests/valid-example.json`
- Result: ✅ PASS - Schema accepts zero cost

**Test 2: Invalid x-cost-per-call: -0.001**
- File: `tests/invalid-negative-cost.json`
- Result: ✅ PASS - Schema rejects negative cost

### 3. Acceptance Criteria ✅
- ✅ x-cost-per-call has `minimum: 0` in schema validation rules
- ✅ Schema validates `x-cost-per-call: 0` as valid
- ✅ Schema rejects `x-cost-per-call: -1` (and -0.001) as invalid

## Conclusion
The task requirement is already satisfied. The x-cost-per-call extension has proper validation that:
1. Accepts zero costs (`x-cost-per-call: 0` is valid)
2. Rejects negative costs (`x-cost-per-call: -0.001` is invalid)

No schema changes were needed.
