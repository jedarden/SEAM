#!/usr/bin/env node

const Ajv = require('ajv');
const addFormats = require('ajv-formats');
const fs = require('fs');

// Load the schema
const schema = JSON.parse(fs.readFileSync('spec/route-fragment-schema.json', 'utf8'));

// Initialize AJV with draft 2020-12 support
const ajv = new Ajv({
  allErrors: true,
  strict: false,
  validateFormats: false,
  addUsedSchema: false
});
addFormats(ajv);

// Remove the $schema key to avoid meta-schema validation issues
const { $schema, ...schemaWithoutMeta } = schema;

// Compile the schema
const validate = ajv.compile(schemaWithoutMeta);

console.log('=== SEAM Extension Validation Tests ===\n');

// Test 1: Valid example
console.log('Test 1: Valid example (should PASS)');
const validExample = JSON.parse(fs.readFileSync('tests/valid-example.json', 'utf8'));
const validResult = validate(validExample);
if (validResult) {
  console.log('✅ PASS: Valid example accepted\n');
} else {
  console.log('❌ FAIL: Valid example rejected');
  console.log('Errors:', ajv.errorsText(validate.errors, { separator: '\n' }));
  process.exit(1);
}

// Test 2: Negative cost (should FAIL)
console.log('Test 2: Negative x-cost-per-call (should FAIL)');
const negativeCost = JSON.parse(fs.readFileSync('tests/invalid-negative-cost.json', 'utf8'));
const negativeCostResult = validate(negativeCost);
if (!negativeCostResult) {
  console.log('✅ PASS: Negative cost correctly rejected\n');
} else {
  console.log('❌ FAIL: Negative cost was accepted (should have been rejected)');
  process.exit(1);
}

// Test 3: Zero window_seconds (should FAIL)
console.log('Test 3: Zero x-quota.window_seconds (should FAIL)');
const zeroWindow = JSON.parse(fs.readFileSync('tests/invalid-zero-window.json', 'utf8'));
const zeroWindowResult = validate(zeroWindow);
if (!zeroWindowResult) {
  console.log('✅ PASS: Zero window_seconds correctly rejected\n');
} else {
  console.log('❌ FAIL: Zero window_seconds was accepted (should have been rejected)');
  process.exit(1);
}

// Test 4: Zero max_iterations (should FAIL)
console.log('Test 4: Zero x-loop-guard.max_iterations (should FAIL)');
const zeroMaxIterations = JSON.parse(fs.readFileSync('tests/invalid-zero-max-iterations.json', 'utf8'));
const zeroMaxIterationsResult = validate(zeroMaxIterations);
if (!zeroMaxIterationsResult) {
  console.log('✅ PASS: Zero max_iterations correctly rejected\n');
} else {
  console.log('❌ FAIL: Zero max_iterations was accepted (should have been rejected)');
  process.exit(1);
}

// Test 5: Negative backoff_ms (should FAIL)
console.log('Test 5: Negative x-loop-guard.backoff_ms (should FAIL)');
const negativeBackoff = JSON.parse(fs.readFileSync('tests/invalid-negative-backoff.json', 'utf8'));
const negativeBackoffResult = validate(negativeBackoff);
if (!negativeBackoffResult) {
  console.log('✅ PASS: Negative backoff_ms correctly rejected\n');
} else {
  console.log('❌ FAIL: Negative backoff_ms was accepted (should have been rejected)');
  process.exit(1);
}

// Test 6: Zero quota limit (should FAIL)
console.log('Test 6: Zero x-quota.limit (should FAIL)');
const zeroLimit = JSON.parse(fs.readFileSync('tests/invalid-zero-quota-limit.json', 'utf8'));
const zeroLimitResult = validate(zeroLimit);
if (!zeroLimitResult) {
  console.log('✅ PASS: Zero quota limit correctly rejected\n');
} else {
  console.log('❌ FAIL: Zero quota limit was accepted (should have been rejected)');
  process.exit(1);
}

// Summary
console.log('=== SUMMARY ===');
console.log('✅ All validation tests passed!');
console.log('');
console.log('All five SEAM extensions have proper validation rules:');
console.log('  1. x-cost-per-call: minimum 0 (rejects negative costs)');
console.log('  2. x-quota.limit: minimum 1 (rejects zero/negative limits)');
console.log('  3. x-quota.window_seconds: minimum 1 (rejects zero windows)');
console.log('  4. x-loop-guard.max_iterations: minimum 1');
console.log('  5. x-loop-guard.backoff_ms: minimum 0');
console.log('');
console.log('Validation tests:');
console.log('  ✅ Schema validates a correct example');
console.log('  ✅ Schema rejects negative costs');
console.log('  ✅ Schema rejects zero quota windows');
console.log('  ✅ Schema rejects zero max_iterations');
console.log('  ✅ Schema rejects negative backoff_ms');
console.log('  ✅ Schema rejects zero quota limits');
