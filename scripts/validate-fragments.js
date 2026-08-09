#!/usr/bin/env node

/**
 * Validate SEAM route fragments against the JSON Schema
 *
 * Usage: node scripts/validate-fragments.js
 *
 * This script loads the route fragment schema and validates all example fragments.
 */

const fs = require('fs');
const path = require('path');
const Ajv = require('ajv');
const addFormats = require('ajv-formats');
const yaml = require('js-yaml');

// Load the schema (use spec schema as it's the canonical source)
const schemaPath = path.join(__dirname, '../spec/route-fragment-schema.json');
const schema = JSON.parse(fs.readFileSync(schemaPath, 'utf8'));

// Initialize AJV with JSON Schema 2020-12 support
const ajv = new Ajv({
  strict: false, // Allow unknown properties for forward-compatibility
  allFormats: true,
  formats: {
    'uri-reference': /^([^\s#][^\s]*)#?$/,
    'email': /^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$/
  },
  validateSchema: false, // Skip meta-schema validation
  logger: false
});

addFormats(ajv);

const validate = ajv.compile(schema);

// Fragment files to validate
const fragmentFiles = [
  'docs/notes/fragments/argocd-read-only-proxy.yaml',
  'docs/notes/fragments/simple-secret-injection.yaml',
  'docs/notes/fragments/route-with-credential-probing.yaml',
  'docs/notes/fragments/route-with-cost-and-quota.yaml',
  'docs/notes/fragments/complex-multi-instance-route.yaml'
];

let allValid = true;
const results = [];

for (const file of fragmentFiles) {
  const filePath = path.join(__dirname, '..', file);

  try {
    const fileContent = fs.readFileSync(filePath, 'utf8');
    const fragment = yaml.load(fileContent);

    const valid = validate(fragment);

    if (valid) {
      console.log(`✅ ${file}: VALID`);
      results.push({ file, valid: true });
    } else {
      console.log(`❌ ${file}: INVALID`);
      console.log(`   Errors:`);
      validate.errors.forEach(err => {
        console.log(`   - ${err.instancePath} ${err.message}`);
        if (err.params) {
          console.log(`     Params: ${JSON.stringify(err.params)}`);
        }
      });
      results.push({ file, valid: false, errors: validate.errors });
      allValid = false;
    }
  } catch (err) {
    console.log(`❌ ${file}: ERROR - ${err.message}`);
    results.push({ file, valid: false, error: err.message });
    allValid = false;
  }
}

console.log('\n' + '='.repeat(60));
console.log(`Total: ${results.length} fragments`);
console.log(`Valid: ${results.filter(r => r.valid).length}`);
console.log(`Invalid: ${results.filter(r => !r.valid).length}`);
console.log('='.repeat(60));

process.exit(allValid ? 0 : 1);
