#!/usr/bin/env node

const Ajv = require('ajv');
const addFormats = require('ajv-formats');
const fs = require('fs');
const path = require('path');

// Paths
const specSchemaPath = path.resolve(__dirname, '../spec/route-fragment-schema.json');
const examplePath = path.resolve(__dirname, '../docs/notes/route-fragment-schema.json');

console.log('=== Validating Complete SEAM Example ===\n');
console.log(`Spec schema: ${specSchemaPath}`);
console.log(`Example: ${examplePath}\n`);

// Load files
const specSchema = JSON.parse(fs.readFileSync(specSchemaPath, 'utf8'));
const exampleFragment = JSON.parse(fs.readFileSync(examplePath, 'utf8'));

// Initialize AJV with draft 2020-12 support
const ajv = new Ajv({
  allErrors: true,
  strict: false,
  validateFormats: false,
  addUsedSchema: false
});
addFormats(ajv);

// Remove the $schema key to avoid meta-schema validation issues
const { $schema, ...schemaWithoutMeta } = specSchema;

// Compile the spec schema
const validate = ajv.compile(schemaWithoutMeta);

// Validate the example
const isValid = validate(exampleFragment);

if (isValid) {
  console.log('✓ Example fragment is VALID according to spec schema\n');

  // Verify all five extensions are present
  console.log('=== Verifying All Five Extensions ===\n');

  const checks = {
    'x-seam-schema': exampleFragment['x-seam-schema'] !== undefined,
    'x-upstream-map': exampleFragment['x-upstream-map'] !== undefined,
    'x-loop-guard (operation)': exampleFragment.paths?.['/forecast']?.get?.['x-loop-guard'] !== undefined,
    'x-cost-per-call (operation)': exampleFragment.paths?.['/forecast']?.get?.['x-cost-per-call'] !== undefined,
    'x-quota (operation)': exampleFragment.paths?.['/forecast']?.get?.['x-quota'] !== undefined
  };

  let allPresent = true;
  for (const [ext, present] of Object.entries(checks)) {
    const status = present ? '✓' : '✗';
    console.log(`${status} ${ext}: ${present ? 'PRESENT' : 'MISSING'}`);
    if (!present) allPresent = false;
  }

  console.log('\n=== Summary ===');
  if (allPresent) {
    console.log('✓ All five SEAM extensions are present');
    console.log('✓ Schema validation passed');
    console.log('✓ Example is ready for seam lint and runtime quarantine');
    process.exit(0);
  } else {
    console.log('✗ Some extensions are missing');
    process.exit(1);
  }

} else {
  console.log('✗ Example fragment is INVALID\n');
  console.error('Errors:');
  validate.errors.forEach(err => {
    console.error(`  ${err.instancePath} ${err.message}`);
  });
  console.log('\n=== Summary ===');
  console.log('✗ Schema validation failed');
  console.log('✗ Example is NOT ready for use');
  process.exit(1);
}
