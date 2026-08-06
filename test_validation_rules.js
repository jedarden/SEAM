const fs = require("fs");

// Read the schema
const schema = JSON.parse(fs.readFileSync("spec/route-fragment-schema.json", "utf8"));

console.log("=== Checking Validation Rules in Schema ===\n");

// Check x-cost-per-call validation
const costPerCallDef = schema.$defs.costPerCall;
console.log("1. x-cost-per-call:");
console.log(`   Type: ${costPerCallDef.type}`);
console.log(`   Minimum: ${costPerCallDef.minimum}`);
console.log(`   Comment: ${costPerCallDef.$comment}`);
if (costPerCallDef.minimum === 0) {
  console.log("   ✅ CORRECT: minimum is 0 (rejects negative costs)");
} else {
  console.log(`   ❌ ERROR: minimum is ${costPerCallDef.minimum}, should be 0`);
  process.exit(1);
}

// Check x-quota validation
const quotaDef = schema.$defs.quota;
console.log("\n2. x-quota:");
console.log(`   limit minimum: ${quotaDef.properties.limit.minimum}`);
console.log(`   window_seconds minimum: ${quotaDef.properties.window_seconds.minimum}`);
if (quotaDef.properties.limit.minimum === 1 && quotaDef.properties.window_seconds.minimum === 1) {
  console.log("   ✅ CORRECT: both limit and window_seconds have minimum 1");
} else {
  console.log(`   ❌ ERROR: limit min=${quotaDef.properties.limit.minimum}, window_seconds min=${quotaDef.properties.window_seconds.minimum}`);
  process.exit(1);
}

// Check x-loop-guard validation
const loopGuardDef = schema.$defs.loopGuard;
console.log("\n3. x-loop-guard:");
console.log(`   max_iterations minimum: ${loopGuardDef.properties.max_iterations.minimum}`);
console.log(`   backoff_ms minimum: ${loopGuardDef.properties.backoff_ms.minimum}`);
if (loopGuardDef.properties.max_iterations.minimum === 1 && loopGuardDef.properties.backoff_ms.minimum === 0) {
  console.log("   ✅ CORRECT: max_iterations min=1, backoff_ms min=0");
} else {
  console.log(`   ❌ ERROR: max_iterations min=${loopGuardDef.properties.max_iterations.minimum}, backoff_ms min=${loopGuardDef.properties.backoff_ms.minimum}`);
  console.log("   Expected: max_iterations min=1, backoff_ms min=0");
  process.exit(1);
}

console.log("\n=== Summary ===");
console.log("✅ All five extensions have proper validation rules:");
console.log("  1. x-cost-per-call: minimum 0 (rejects negative costs)");
console.log("  2. x-quota.limit: minimum 1 (rejects zero/negative limits)");
console.log("  3. x-quota.window_seconds: minimum 1 (rejects zero windows)");
console.log("  4. x-loop-guard.max_iterations: minimum 1");
console.log("  5. x-loop-guard.backoff_ms: minimum 0");
console.log("\n✅ All validation rules are correct!");
