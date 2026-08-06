#!/usr/bin/env node
/**
 * Test SEAM policy and metadata extensions schema validation
 *
 * Tests that the route-fragment-schema.json properly validates:
 * - x-seam-schema (string, version marker e.g. 'v1')
 * - x-loop-guard (object with max_iterations, backoff_ms fields)
 * - x-cost-per-call (number, cost units)
 * - x-quota (object with limit, window_seconds fields)
 * - x-upstream-map (object with target_host, rewrite_path fields)
 */

const Ajv = require('ajv');
const fs = require('fs');

const ajv = new Ajv({
  allErrors: true,
  strict: false,
  validateFormats: false,
  addUsedSchema: false
});

// Remove the schema declaration to avoid validation issues
const schemaJson = fs.readFileSync('/home/coding/SEAM/docs/notes/route-fragment-schema.json', 'utf8');
const schema = JSON.parse(schemaJson);
delete schema.$schema;

const examples = JSON.parse(fs.readFileSync('/home/coding/SEAM/docs/notes/route-fragment-schema-policy-examples.json', 'utf8'));

const validate = ajv.compile(schema);

console.log('Testing SEAM Policy Extensions Schema Validation\n');
console.log('='.repeat(60));

let passed = 0;
let failed = 0;

// Test valid examples
console.log('\n📋 VALID EXAMPLES (should pass)\n');
for (const [name, example] of Object.entries(examples.examples)) {
  const valid = validate(example.fragment);
  if (valid) {
    console.log(`✅ ${name}`);
    console.log(`   ${example.description}`);
    passed++;
  } else {
    console.log(`❌ ${name}`);
    console.log(`   ${example.description}`);
    console.log(`   Errors:`, JSON.stringify(validate.errors, null, 2));
    failed++;
  }
  console.log('');
}

// Test invalid examples
console.log('\n🚫 INVALID EXAMPLES (should fail)\n');
for (const [name, example] of Object.entries(examples['invalid-examples'])) {
  const valid = validate(example.fragment);
  if (!valid) {
    console.log(`✅ ${name}`);
    console.log(`   ${example.$comment}`);
    console.log(`   Expected: ${example.expected_error}`);
    passed++;
  } else {
    console.log(`❌ ${name}`);
    console.log(`   ${example.$comment}`);
    console.log(`   Expected to fail but passed validation`);
    failed++;
  }
  console.log('');
}

console.log('='.repeat(60));
console.log(`\n📊 RESULTS: ${passed} passed, ${failed} failed\n`);

// Test specific field requirements from task
console.log('🔍 CHECKING TASK FIELD REQUIREMENTS\n');
console.log('='.repeat(60));

// Check x-seam-schema
console.log('\nx-seam-schema extension:');
console.log(`  Current schema: ${JSON.stringify(schema.properties['x-seam-schema'])}`);
console.log(`  Task requirement: string, version marker e.g. 'v1'`);

// Check x-loop-guard
console.log('\nx-loop-guard extension:');
const loopGuardDef = schema.$defs.loopGuard;
if (loopGuardDef) {
  console.log(`  Current schema fields: ${Object.keys(loopGuardDef.properties).join(', ')}`);
  console.log(`  Task requirement: max_iterations, backoff_ms`);
}

// Check x-cost-per-call
console.log('\nx-cost-per-call extension:');
const costPerCallDef = schema.$defs.costPerCall;
if (costPerCallDef) {
  console.log(`  Current schema type: ${costPerCallDef.type}`);
  console.log(`  Current schema fields: ${Object.keys(costPerCallDef.properties || {}).join(', ')}`);
  console.log(`  Task requirement: number, cost units`);
}

// Check x-quota
console.log('\nx-quota extension:');
const quotaDef = schema.$defs.quota;
if (quotaDef) {
  console.log(`  Current schema fields: ${Object.keys(quotaDef.properties).join(', ')}`);
  console.log(`  Task requirement: limit, window_seconds`);
}

// Check x-upstream-map
console.log('\nx-upstream-map extension:');
const upstreamMapDef = schema.$defs.upstreamMap;
if (upstreamMapDef) {
  console.log(`  Current schema type: ${upstreamMapDef.type}`);
  console.log(`  Task requirement: object with target_host, rewrite_path`);
}

console.log('\n' + '='.repeat(60));

process.exit(failed > 0 ? 1 : 0);