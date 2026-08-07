#!/usr/bin/env node

/**
 * Validation script for OpenAPI 3.1 fragment examples
 *
 * Requirements:
 * - npm install ajv ajv-formats
 *
 * Run:
 * node docs/notes/validate-openapi-fragment-examples.js
 */

const Ajv = require('ajv');
const addFormats = require('ajv-formats');
const fs = require('fs');
const path = require('path');

// Load the schema
const schemaPath = path.join(__dirname, 'openapi-fragment-base-schema.json');
const schema = JSON.parse(fs.readFileSync(schemaPath, 'utf8'));

// Create AJV instance with JSON Schema 2020-12 support
const ajv = new Ajv({
  strict: false,
  strictSchema: false,
  validateFormats: false,
  allowUnionTypes: true,
  schemas: [schema]
});

addFormats(ajv);

const validate = ajv.compile(schema);

// Example files to validate
const exampleFiles = [
  'openapi-fragment-example-minimal.json',
  'openapi-fragment-example-paths-only.json',
  'openapi-fragment-example-components-only.json'
];

console.log('Validating OpenAPI 3.1 Fragment Examples');
console.log('='.repeat(60));
console.log(`Schema: ${path.basename(schemaPath)}`);
console.log('='.repeat(60));

let passed = 0;
let failed = 0;

// Validate each example file
exampleFiles.forEach(filename => {
  const examplePath = path.join(__dirname, filename);

  try {
    const exampleData = JSON.parse(fs.readFileSync(examplePath, 'utf8'));
    const isValid = validate(exampleData);

    if (isValid) {
      console.log(`✅ PASS: ${filename}`);
      passed++;
    } else {
      console.log(`❌ FAIL: ${filename}`);
      if (validate.errors) {
        const error = validate.errors[0];
        console.log(`   Error: ${error.message}`);
        console.log(`   Path: ${error.instancePath}`);
        if (error.params) {
          console.log(`   Details: ${JSON.stringify(error.params)}`);
        }
      }
      failed++;
    }
  } catch (readError) {
    console.log(`❌ ERROR: ${filename}`);
    console.log(`   Failed to read or parse file: ${readError.message}`);
    failed++;
  }
});

// Summary
console.log('\n' + '='.repeat(60));
console.log(`Results: ${passed} passed, ${failed} failed out of ${passed + failed} examples`);

if (failed > 0) {
  console.log('\n❌ VALIDATION FAILED');
  process.exit(1);
} else {
  console.log('\n✅ ALL EXAMPLES VALIDATED SUCCESSFULLY');
  process.exit(0);
}