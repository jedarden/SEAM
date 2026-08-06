#!/usr/bin/env node

/**
 * Test script for base-fragment-schema.json
 *
 * Requirements:
 * - npm install ajv ajv-formats
 *
 * Run:
 * node docs/notes/test-base-fragment.js
 */

const Ajv = require('ajv');
const addFormats = require('ajv-formats');
const fs = require('fs');
const path = require('path');

// Load the schema
const schemaPath = path.join(__dirname, 'base-fragment-schema.json');
const schema = JSON.parse(fs.readFileSync(schemaPath, 'utf8'));

// Create AJV instance with JSON Schema 2020-12 support
const ajv = new Ajv({
  strict: false,
  strictSchema: false,
  validateFormats: false,
  allowUnionTypes: true,
  schemas: [schema] // Add schema directly to avoid $schema lookup
});

addFormats(ajv);

const validate = ajv.compile(schema);

// Test cases
const tests = {
  valid: [
    {
      name: 'Minimal valid fragment',
      input: {
        paths: {
          '/test': {
            get: {
              summary: 'Test endpoint',
              responses: {
                '200': {
                  description: 'Success'
                }
              }
            }
          }
        }
      }
    },
    {
      name: 'Fragment with components',
      input: {
        paths: {
          '/users': {
            get: {
              summary: 'List users',
              responses: {
                '200': {
                  description: 'Success',
                  content: {
                    'application/json': {
                      schema: {
                        $ref: '#/components/schemas/UserList'
                      }
                    }
                  }
                }
              }
            }
          }
        },
        components: {
          schemas: {
            UserList: {
              type: 'array',
              items: {
                $ref: '#/components/schemas/User'
              }
            },
            User: {
              type: 'object',
              required: ['id', 'name'],
              properties: {
                id: { type: 'string', format: 'uuid' },
                name: { type: 'string' }
              }
            }
          }
        }
      }
    },
    {
      name: 'Path parameters',
      input: {
        paths: {
          '/users/{userId}': {
            parameters: [
              {
                name: 'userId',
                in: 'path',
                required: true,
                schema: {
                  type: 'string'
                }
              }
            ],
            get: {
              summary: 'Get user',
              responses: {
                '200': {
                  description: 'Success'
                }
              }
            }
          }
        }
      }
    },
    {
      name: 'Multiple operations',
      input: {
        paths: {
          '/users': {
            get: {
              summary: 'List users',
              responses: {
                '200': { description: 'Success' }
              }
            },
            post: {
              summary: 'Create user',
              requestBody: {
                content: {
                  'application/json': {
                    schema: {
                      type: 'object',
                      required: ['name'],
                      properties: {
                        name: { type: 'string' }
                      }
                    }
                  }
                }
              },
              responses: {
                '201': { description: 'Created' }
              }
            }
          }
        }
      }
    },
    {
      name: 'With optional openapi field',
      input: {
        openapi: '3.1.0',
        paths: {
          '/test': {
            get: {
              responses: {
                '200': { description: 'Success' }
              }
            }
          }
        }
      }
    },
    {
      name: 'Reusable parameters in components',
      input: {
        paths: {
          '/users/{userId}': {
            parameters: [
              { $ref: '#/components/parameters/UserId' }
            ],
            get: {
              summary: 'Get user',
              responses: {
                '200': { description: 'Success' }
              }
            }
          }
        },
        components: {
          parameters: {
            UserId: {
              name: 'userId',
              in: 'path',
              required: true,
              schema: { type: 'string' }
            }
          }
        }
      }
    },
    {
      name: 'Response with content',
      input: {
        paths: {
          '/data': {
            get: {
              summary: 'Get data',
              responses: {
                '200': {
                  description: 'Success',
                  content: {
                    'application/json': {
                      schema: {
                        type: 'object',
                        properties: {
                          message: { type: 'string' }
                        }
                      }
                    }
                  }
                }
              }
            }
          }
        }
      }
    },
    {
      name: 'Multiple paths',
      input: {
        paths: {
          '/users': {
            get: {
              responses: {
                '200': { description: 'Success' }
              }
            }
          },
          '/posts': {
            get: {
              responses: {
                '200': { description: 'Success' }
              }
            }
          }
        }
      }
    }
  ],
  invalid: [
    {
      name: 'Missing paths',
      input: {},
      expectedError: 'must have required property "paths"'
    },
    {
      name: 'Empty paths object',
      input: {
        paths: {}
      },
      expectedError: 'must NOT have fewer than 1 properties'
    },
    {
      name: 'Path does not start with /',
      input: {
        paths: {
          'invalid': {
            get: {
              responses: {
                '200': { description: 'Success' }
              }
            }
          }
        }
      },
      expectedError: 'must match pattern'
    },
    {
      name: 'Operation missing responses',
      input: {
        paths: {
          '/test': {
            get: {
              summary: 'Test'
            }
          }
        }
      },
      expectedError: 'must have required property "responses"'
    },
    {
      name: 'Empty responses object',
      input: {
        paths: {
          '/test': {
            get: {
              responses: {}
            }
          }
        }
      },
      expectedError: 'must NOT have fewer than 1 properties'
    },
    {
      name: 'Response missing description',
      input: {
        paths: {
          '/test': {
            get: {
              responses: {
                '200': {
                  content: {
                    'application/json': {
                      schema: { type: 'string' }
                    }
                  }
                }
              }
            }
          }
        }
      },
      expectedError: 'must have required property "description"'
    },
    {
      name: 'Invalid openapi version',
      input: {
        openapi: '3.0.0',
        paths: {
          '/test': {
            get: {
              responses: {
                '200': { description: 'Success' }
              }
            }
          }
        }
      },
      expectedError: 'must match pattern'
    },
    {
      name: 'Parameter missing required fields',
      input: {
        paths: {
          '/test': {
            parameters: [
              {
                name: 'param'
              }
            ],
            get: {
              responses: {
                '200': { description: 'Success' }
              }
            }
          }
        }
      },
      expectedError: 'must have required property'
    },
    {
      name: 'Path parameter not marked required',
      input: {
        paths: {
          '/test/{id}': {
            parameters: [
              {
                name: 'id',
                in: 'path',
                schema: { type: 'string' }
              }
            ],
            get: {
              responses: {
                '200': { description: 'Success' }
              }
            }
          }
        }
      },
      expectedError: 'must be equal to true'
    },
    {
      name: 'Invalid HTTP status code',
      input: {
        paths: {
          '/test': {
            get: {
              responses: {
                '999': { description: 'Invalid' }
              }
            }
          }
        }
      },
      expectedError: 'must match pattern'
    }
  ]
};

// Run tests
console.log('Testing base-fragment-schema.json\n');
console.log('='.repeat(60));

let passed = 0;
let failed = 0;

function runTest(test, category) {
  const isValid = validate(test.input);

  if (category === 'valid') {
    if (isValid) {
      console.log(`✅ PASS: ${test.name}`);
      passed++;
    } else {
      console.log(`❌ FAIL: ${test.name}`);
      console.log(`   Expected: valid`);
      console.log(`   Got: ${validate.errors ? JSON.stringify(validate.errors[0], null, 2) : 'unknown error'}`);
      failed++;
    }
  } else {
    if (!isValid) {
      console.log(`✅ PASS: ${test.name}`);
      if (validate.errors) {
        const error = validate.errors[0];
        console.log(`   Rejected: ${error.message}`);
      }
      passed++;
    } else {
      console.log(`❌ FAIL: ${test.name}`);
      console.log(`   Expected: invalid (${test.expectedError})`);
      console.log(`   Got: accepted`);
      failed++;
    }
  }
}

// Run valid tests
console.log('\nVALID TESTS (should accept):');
console.log('-'.repeat(60));
tests.valid.forEach(test => runTest(test, 'valid'));

// Run invalid tests
console.log('\nINVALID TESTS (should reject):');
console.log('-'.repeat(60));
tests.invalid.forEach(test => runTest(test, 'invalid'));

// Summary
console.log('\n' + '='.repeat(60));
console.log(`Results: ${passed} passed, ${failed} failed out of ${passed + failed} tests`);

if (failed > 0) {
  console.log('\n❌ TESTS FAILED');
  process.exit(1);
} else {
  console.log('\n✅ ALL TESTS PASSED');
  process.exit(0);
}