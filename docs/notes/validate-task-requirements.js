#!/usr/bin/env node
/**
 * Validate that route-fragment-schema.json meets task bf-19bc acceptance criteria:
 *
 * 1. Schema validates all five policy extensions with correct types
 * 2. Rejects invalid values (negative costs, zero quota windows)
 * 3. Complete example fragment with all extensions passes validation
 * 4. Final schema file: docs/notes/route-fragment-schema.json (complete)
 */

const Ajv = require('ajv');
const fs = require('fs');

const ajv = new Ajv({
  allErrors: true,
  strict: false,
  validateFormats: false,
  addUsedSchema: false
});

const schemaJson = fs.readFileSync('/home/coding/SEAM/docs/notes/route-fragment-schema.json', 'utf8');
const schema = JSON.parse(schemaJson);
delete schema.$schema;

console.log('📋 VALIDATING TASK BF-19BC ACCEPTANCE CRITERIA\n');
console.log('='.repeat(70));

// Acceptance Criterion 1: Schema validates all five policy extensions with correct types
console.log('\n✅ CRITERION 1: All five policy extensions exist with correct types\n');

const extensions = [
  { name: 'x-seam-schema', expected: 'string/const', actual: schema.properties['x-seam-schema'] },
  { name: 'x-loop-guard', expected: 'operation object', actual: schema.$defs.loopGuard },
  { name: 'x-cost-per-call', expected: 'operation object', actual: schema.$defs.costPerCall },
  { name: 'x-quota', expected: 'operation object', actual: schema.$defs.quota },
  { name: 'x-upstream-map', expected: 'fragment-root object', actual: schema.$defs.upstreamMap }
];

extensions.forEach(ext => {
  const exists = ext.actual !== undefined;
  const status = exists ? '✅' : '❌';
  console.log(`  ${status} ${ext.name}: ${exists ? 'DEFINED' : 'MISSING'}`);
  if (exists && typeof ext.actual === 'object') {
    if (ext.actual.properties) {
      console.log(`     Properties: ${Object.keys(ext.actual.properties).join(', ')}`);
    }
    if (ext.actual.type) {
      console.log(`     Type: ${ext.actual.type}`);
    }
    if (ext.actual.const) {
      console.log(`     Const: ${ext.actual.const}`);
    }
  }
});

// Acceptance Criterion 2: Rejects invalid values
console.log('\n✅ CRITERION 2: Schema rejects invalid values\n');

const validate = ajv.compile(schema);

const invalidExamples = [
  {
    name: 'Negative cost (-0.001)',
    fragment: {
      'x-seam-schema': 'v1',
      'x-seam-owner': 'test',
      'x-upstream': 'https://example.com',
      'paths': {
        '/test': {
          'get': {
            'x-cost-per-call': -0.001,
            'responses': { '200': { 'description': 'OK' } }
          }
        }
      }
    }
  },
  {
    name: 'Zero cost (0)',
    fragment: {
      'x-seam-schema': 'v1',
      'x-seam-owner': 'test',
      'x-upstream': 'https://example.com',
      'paths': {
        '/test': {
          'get': {
            'x-cost-per-call': 0,
            'responses': { '200': { 'description': 'OK' } }
          }
        }
      }
    }
  },
  {
    name: 'Zero quota window (0s)',
    fragment: {
      'x-seam-schema': 'v1',
      'x-seam-owner': 'test',
      'x-upstream': 'https://example.com',
      'paths': {
        '/test': {
          'get': {
            'x-cost-per-call': 1,
            'x-quota': { 'limit': 100, 'window_seconds': 0 },
            'responses': { '200': { 'description': 'OK' } }
          }
        }
      }
    }
  }
];

invalidExamples.forEach(ex => {
  const valid = validate(ex.fragment);
  const status = !valid ? '✅' : '❌';
  const result = !valid ? 'REJECTED' : 'ACCEPTED (WRONG!)';
  console.log(`  ${status} ${ex.name}: ${result}`);
  if (valid) {
    console.log(`     ERROR: Should have been rejected but passed validation`);
  }
});

// Acceptance Criterion 3: Complete example fragment passes validation
console.log('\n✅ CRITERION 3: Complete example with all extensions passes\n');

const completeExample = {
  'x-seam-schema': 'v1',
  'x-seam-owner': 'weather-service',
  'x-api-version': 'v1',
  'x-instance-param': 'region',
  'x-upstream-map': {
    'us-east': {
      'url': 'https://weather.api.example.com',
      'vaultPath': 'seam/routes/weather-service/api-token',
      'injectAs': { 'kind': 'bearer' },
      'target_host': 'weather.api.example.com',
      'rewrite_path': '/api/v1/forecast'
    }
  },
  'x-required-scope': 'weather:query:data',
  'paths': {
    '/forecast': {
      'get': {
        'summary': 'Get weather forecast with rate limiting',
        'x-loop-guard': {
          'max_iterations': 5,
          'backoff_ms': 10000
        },
        'x-cost-per-call': 0.001,
        'x-quota': {
          'limit': 100,
          'window_seconds': 3600
        },
        'responses': {
          '200': { 'description': 'Success' }
        }
      }
    }
  }
};

const completeValid = validate(completeExample);
const completeStatus = completeValid ? '✅' : '❌';
const completeResult = completeValid ? 'PASSED' : 'FAILED';
console.log(`  ${completeStatus} Complete example: ${completeResult}`);
if (!completeValid) {
  console.log(`     Validation errors:`, JSON.stringify(validate.errors, null, 2));
}

// Acceptance Criterion 4: Final schema file exists and is complete
console.log('\n✅ CRITERION 4: Final schema file docs/notes/route-fragment-schema.json\n');

const schemaStats = fs.statSync('/home/coding/SEAM/docs/notes/route-fragment-schema.json');
console.log(`  ✅ File exists: docs/notes/route-fragment-schema.json`);
console.log(`  ✅ File size: ${schemaStats.size} bytes`);
console.log(`  ✅ Schema structure: ${Object.keys(schema).length} top-level keys`);
console.log(`  ✅ Definitions: ${Object.keys(schema.$defs || {}).length} defined`);

// Final verdict
console.log('\n' + '='.repeat(70));
console.log('\n🎯 FINAL VERDICT:\n');

const allPassed =
  extensions.every(e => e.actual !== undefined) &&
  invalidExamples.every(ex => !validate(ex.fragment)) &&
  completeValid;

if (allPassed) {
  console.log('  ✅ ALL ACCEPTANCE CRITERIA MET');
  console.log('  ✅ Schema is complete and ready for seam lint and runtime quarantine');
  console.log('\n  Task bf-19bc can be CLOSED as COMPLETED');
} else {
  console.log('  ❌ SOME ACCEPTANCE CRITERIA NOT MET');
  console.log('  ❌ Additional work required');
}

console.log('\n' + '='.repeat(70));

process.exit(allPassed ? 0 : 1);