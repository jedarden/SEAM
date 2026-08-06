#!/usr/bin/env node

/**
 * Validation test for SEAM authentication extensions schema.
 * Tests valid and invalid examples against route-fragment-schema-auth.json.
 */

const Ajv = require('ajv');
const addFormats = require('ajv-formats');
const fs = require('fs');
const path = require('path');

// Load schema and examples
const schemaPath = path.join(__dirname, 'route-fragment-schema-auth.json');
const examplesPath = path.join(__dirname, 'route-fragment-schema-auth-examples.json');
const schema = JSON.parse(fs.readFileSync(schemaPath, 'utf8'));
const examples = JSON.parse(fs.readFileSync(examplesPath, 'utf8'));

// Create AJV instance with JSON Schema 2020-12 support
const ajv = new Ajv({
  allErrors: true,
  strict: false,
  strictSchema: false,
  validateFormats: false,
  allowUnionTypes: true
});

// Remove the $schema reference to avoid lookup issues
delete schema.$schema;

addFormats(ajv);

// Compile the schema
const validate = ajv.compile(schema);

console.log('SEAM Authentication Extensions Schema Validation\n');
console.log('='.repeat(60));

let passCount = 0;
let failCount = 0;

// Test valid examples
console.log('\n✅ VALID EXAMPLES (should pass):\n');
for (const [name, exampleData] of Object.entries(examples.examples)) {
  if (name === '$comment') continue;

  const fragment = exampleData.fragment;
  const valid = validate(fragment);

  if (valid) {
    console.log(`✓ ${name}: PASS`);
    passCount++;
  } else {
    console.log(`✗ ${name}: FAIL (unexpected)`);
    console.log('  Errors:', JSON.stringify(validate.errors, null, 2));
    failCount++;
  }
}

// Test invalid examples
console.log('\n❌ INVALID EXAMPLES (should fail):\n');
for (const [name, exampleData] of Object.entries(examples['invalid-examples'])) {
  if (name === '$comment') continue;

  const fragment = exampleData.fragment;
  const valid = validate(fragment);

  if (!valid) {
    console.log(`✓ ${name}: CORRECTLY REJECTED`);
    console.log(`  Expected: ${exampleData['expected-error']}`);
    console.log(`  Got: ${validate.errors[0].message}`);
    passCount++;
  } else {
    console.log(`✗ ${name}: INCORRECTLY ACCEPTED (should fail)`);
    failCount++;
  }
}

// Summary
console.log('\n' + '='.repeat(60));
console.log(`\nSUMMARY: ${passCount} passed, ${failCount} failed\n`);

if (failCount > 0) {
  process.exit(1);
} else {
  console.log('All acceptance criteria validated successfully!\n');
  process.exit(0);
}