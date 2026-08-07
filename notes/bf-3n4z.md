# bf-3n4z: Add validation rules for all SEAM extensions

## Status: COMPLETE

This task was completed in commit fe94129 on 2026-08-06.

## Verification

All validation rules are in place and all tests pass:

### Validation Rules Added
1. **x-cost-per-call**: minimum 0 (rejects negative costs) - line 179
2. **x-quota.limit**: minimum 1 (rejects zero limits) - line 186  
3. **x-quota.window_seconds**: minimum 1 (rejects zero windows) - line 187
4. **x-loop-guard.max_iterations**: minimum 1 - line 172
5. **x-loop-guard.backoff_ms**: minimum 0 - line 173

### Acceptance Criteria Met
- ✅ All five extensions have validation rules
- ✅ Negative costs are rejected
- ✅ Zero quota windows are rejected  
- ✅ Schema validates a correct example
- ✅ Schema rejects invalid examples

### Test Results
All 11 validation tests pass:
- 1 valid example accepted
- 6 invalid examples correctly rejected (negative costs, zero windows, etc.)
- 4 boundary values correctly accepted (zero cost, minimum values)

### Verification Scripts
- `test_validation_rules.js` - Checks validation rules in schema
- `validate-extension-tests.js` - Full validation test suite
- `tests/` directory - Valid and invalid test examples

This bead verifies that the validation rules are properly implemented and all tests pass.
