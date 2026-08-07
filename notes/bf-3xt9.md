# Bead bf-3xt9: x-cost-per-call Validation Rule

## Finding

The x-cost-per-call validation rule **already exists** in the schema at `spec/route-fragment-schema.json` (lines 177-181):

```json
"costPerCall": {
  "type": "number",
  "minimum": 0,
  "$comment": "Cost per call in arbitrary cost units. Must be non-negative (negative costs rejected)."
}
```

## Verification

All acceptance criteria are already met:

1. ✅ **x-cost-per-call has validation rule with minimum 0** - The schema defines `costPerCall` with `minimum: 0`
2. ✅ **Schema rejects negative cost values** - Test file `tests/invalid-negative-cost.json` exists and verifies negative values are rejected
3. ✅ **Schema accepts zero and positive cost values** - Test file `tests/valid-zero-cost.json` exists and verifies zero is accepted

## Test Results

Running `node test_validation_rules.js` confirms:
- Type: `number`
- Minimum: `0`
- Comment explicitly states: "Must be non-negative (negative costs rejected)"

The validation was already implemented in a previous commit.
