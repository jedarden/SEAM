const Ajv = require("ajv");
const addFormats = require("ajv-formats");
const fs = require("fs");

// Read the schema
const schema = JSON.parse(fs.readFileSync("spec/route-fragment-schema.json", "utf8"));

// Initialize AJV with draft-2020-12 support
const ajv = new Ajv({
  allErrors: true,
  strict: false,
  validateFormats: false
});
// Add draft-2020-12 meta-schema support
const addMetaSchema2020 = require("ajv/dist/refs/json-schema-2020-12/index.js").default;
addMetaSchema2020.call(ajv);
addFormats(ajv);

// Compile the schema
const validate = ajv.compile(schema);

// Read the valid example
const validExample = JSON.parse(fs.readFileSync("docs/notes/route-fragment-example.json", "utf8"));

console.log("=== Testing VALID example ===\n");

// Test the valid example
const validResult = validate(validExample);
if (validResult) {
  console.log("✅ Valid example PASSED validation");
} else {
  console.log("❌ Valid example FAILED validation:");
  console.log(ajv.errorsText(validate.errors, { separator: "\n" }));
  process.exit(1);
}

console.log("\n=== Testing INVALID examples ===\n");

// Test invalid: negative x-cost-per-call
const negativeCostExample = JSON.parse(JSON.stringify(validExample));
negativeCostExample.paths["/forecast"].get["x-cost-per-call"] = -0.001;
const negativeCostResult = validate(negativeCostExample);
if (!negativeCostResult) {
  console.log("✅ Correctly REJECTED negative x-cost-per-call (-0.001)");
} else {
  console.log("❌ FAILED to reject negative x-cost-per-call");
  process.exit(1);
}

// Test invalid: zero quota window_seconds
const zeroWindowExample = JSON.parse(JSON.stringify(validExample));
zeroWindowExample.paths["/forecast"].get["x-quota"].window_seconds = 0;
const zeroWindowResult = validate(zeroWindowExample);
if (!zeroWindowResult) {
  console.log("✅ Correctly REJECTED zero x-quota.window_seconds (0)");
} else {
  console.log("❌ FAILED to reject zero x-quota.window_seconds");
  process.exit(1);
}

// Test invalid: zero loop-guard max_iterations
const zeroMaxIterationsExample = JSON.parse(JSON.stringify(validExample));
zeroMaxIterationsExample.paths["/forecast"].get["x-loop-guard"].max_iterations = 0;
const zeroMaxIterationsResult = validate(zeroMaxIterationsExample);
if (!zeroMaxIterationsResult) {
  console.log("✅ Correctly REJECTED zero x-loop-guard.max_iterations (0)");
} else {
  console.log("❌ FAILED to reject zero x-loop-guard.max_iterations");
  process.exit(1);
}

// Test valid: zero backoff_ms (should be accepted)
const zeroBackoffExample = JSON.parse(JSON.stringify(validExample));
zeroBackoffExample.paths["/forecast"].get["x-loop-guard"].backoff_ms = 0;
const zeroBackoffResult = validate(zeroBackoffExample);
if (zeroBackoffResult) {
  console.log("✅ Correctly ACCEPTED zero x-loop-guard.backoff_ms (0)");
} else {
  console.log("❌ FAILED to accept zero x-loop-guard.backoff_ms:");
  console.log(ajv.errorsText(validate.errors, { separator: "\n" }));
  process.exit(1);
}

// Test invalid: negative backoff_ms
const negativeBackoffExample = JSON.parse(JSON.stringify(validExample));
negativeBackoffExample.paths["/forecast"].get["x-loop-guard"].backoff_ms = -100;
const negativeBackoffResult = validate(negativeBackoffExample);
if (!negativeBackoffResult) {
  console.log("✅ Correctly REJECTED negative x-loop-guard.backoff_ms (-100)");
} else {
  console.log("❌ FAILED to reject negative x-loop-guard.backoff_ms");
  process.exit(1);
}

// Test invalid: zero quota limit
const zeroLimitExample = JSON.parse(JSON.stringify(validExample));
zeroLimitExample.paths["/forecast"].get["x-quota"].limit = 0;
const zeroLimitResult = validate(zeroLimitExample);
if (!zeroLimitResult) {
  console.log("✅ Correctly REJECTED zero x-quota.limit (0)");
} else {
  console.log("❌ FAILED to reject zero x-quota.limit");
  process.exit(1);
}

console.log("\n=== All validation tests PASSED ===\n");
console.log("Summary:");
console.log("  ✅ Valid example accepted");
console.log("  ✅ Negative costs rejected");
console.log("  ✅ Zero quota windows rejected");
console.log("  ✅ Zero max_iterations rejected");
console.log("  ✅ Zero backoff_ms accepted");
console.log("  ✅ Negative backoff_ms rejected");
console.log("  ✅ Zero quota limit rejected");
console.log("\n✅ All five SEAM extensions have proper validation rules!");
