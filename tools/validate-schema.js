#!/usr/bin/env node

const Ajv = require('ajv');
const addFormats = require('ajv-formats');
const fs = require('fs');
const path = require('path');

// Load the schema
const schemaPath = path.resolve(process.argv[2] || './docs/notes/route-fragment-schema.json');
const examplesPath = path.resolve(process.argv[3] || './docs/notes/route-fragment-schema-policy-examples.json');

console.log(`Loading schema from: ${schemaPath}`);
console.log(`Loading examples from: ${examplesPath}\n`);

const schema = JSON.parse(fs.readFileSync(schemaPath, 'utf8'));
const examples = JSON.parse(fs.readFileSync(examplesPath, 'utf8'));

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

// Test valid examples
console.log('=== TESTING VALID EXAMPLES ===');
let validCount = 0;
let invalidCount = 0;

for (const [name, exampleData] of Object.entries(examples.examples)) {
  if (name === '$comment') continue;

  const fragment = exampleData.fragment;
  const valid = validate(fragment);

  if (valid) {
    console.log(`✓ ${name}: VALID`);
    validCount++;
  } else {
    console.log(`✗ ${name}: INVALID`);
    console.error('  Errors:', validate.errors.map(e => `    ${e.instancePath} ${e.message}`).join('\n'));
    invalidCount++;
  }
}

console.log(`\nValid examples: ${validCount}/${validCount + invalidCount}\n`);

// Test invalid examples
console.log('=== TESTING INVALID EXAMPLES (should fail) ===');
let expectedFailures = 0;
let unexpectedPasses = 0;

for (const [name, exampleData] of Object.entries(examples['invalid-examples'])) {
  if (name === '$comment') continue;

  const fragment = exampleData.fragment;
  const valid = validate(fragment);

  if (!valid) {
    console.log(`✓ ${name}: CORRECTLY REJECTED`);
    console.log(`  Expected: ${exampleData.expectedError}`);
    expectedFailures++;
  } else {
    console.log(`✗ ${name}: UNEXPECTEDLY PASSED (should have failed)`);
    console.log(`  Expected error: ${exampleData.expectedError}`);
    unexpectedPasses++;
  }
}

console.log(`\nCorrectly rejected: ${expectedFailures}/${expectedFailures + unexpectedPasses}\n`);

// Summary
console.log('=== SUMMARY ===');
const totalValid = validCount + invalidCount;
const totalInvalid = expectedFailures + unexpectedPasses;
const success = validCount === totalValid && unexpectedPasses === 0;

if (success) {
  console.log('✓ All tests passed!');
  process.exit(0);
} else {
  console.log('✗ Some tests failed');
  if (invalidCount > 0) {
    console.log(`  - ${invalidCount} valid examples failed validation`);
  }
  if (unexpectedPasses > 0) {
    console.log(`  - ${unexpectedPasses} invalid examples unexpectedly passed`);
  }
  process.exit(1);
}