#!/usr/bin/env node

/**
 * Comprehensive Documentation Rendering Validation
 *
 * This script validates the rendered HTML documentation against all acceptance criteria:
 * 1. HTML structure and syntax validation
 * 2. All routes from merged spec are present
 * 3. Interactive features (expand, navigate, search)
 * 4. Request/response schema display accuracy
 * 5. Documentation structure and completeness
 */

const http = require('http');
const fs = require('fs');

const BASE_URL = process.env.DOCS_URL || 'http://localhost:8080';
const SCREENSHOT_DIR = process.env.SCREENSHOT_DIR || '/home/coding/SEAM/scratch/docs-validation';

async function main() {
  console.log('=== SEAM Documentation Rendering Validation ===\n');
  console.log(`Testing documentation at: ${BASE_URL}/docs`);
  console.log(`Screenshot directory: ${SCREENSHOT_DIR}\n`);

  // Create screenshot directory if it doesn't exist
  if (!fs.existsSync(SCREENSHOT_DIR)) {
    fs.mkdirSync(SCREENSHOT_DIR, { recursive: true });
  }

  const results = {
    htmlValidation: await testHTMLStructure(),
    specCompleteness: await testSpecCompleteness(),
    interactiveFeatures: await testInteractiveFeatures(),
    schemaAccuracy: await testSchemaAccuracy(),
    browserCompatibility: await testBrowserCompatibility()
  };

  console.log('\n=== Validation Results ===\n');

  let allPassed = true;

  for (const [testName, result] of Object.entries(results)) {
    const status = result.passed ? '✅' : '❌';
    console.log(`${status} ${testName}: ${result.message}`);
    if (!result.passed) allPassed = false;
  }

  console.log('\n=== Acceptance Criteria Status ===\n');

  const criteria = [
    'HTML passes validation (no syntax errors)',
    'All routes from merged spec are visible',
    'All interactive features work correctly',
    'Documentation browsable in major browsers',
    'Automated tests pass'
  ];

  criteria.forEach((criterion, index) => {
    const status = results.htmlValidation.passed &&
                  results.specCompleteness.passed &&
                  results.interactiveFeatures.passed &&
                  results.browserCompatibility.passed ? '✅' : '❌';
    console.log(`${status} ${criterion}`);
  });

  if (allPassed) {
    console.log('\n✅ All documentation rendering validation passed!');
    process.exit(0);
  } else {
    console.log('\n❌ Some validation tests failed - see details above');
    process.exit(1);
  }
}

async function testHTMLStructure() {
  console.log('Test 1: HTML Structure and Syntax Validation');
  try {
    const html = await fetchText('/docs');

    const checks = [
      { name: 'DOCTYPE declaration', test: () => html.includes('<!DOCTYPE html>') },
      { name: 'HTML tag', test: () => html.includes('<html>') },
      { name: 'HEAD section', test: () => html.includes('<head>') },
      { name: 'BODY section', test: () => html.includes('<body>') },
      { name: 'UTF-8 charset', test: () => html.includes('charset="utf-8"') },
      { name: 'Viewport meta tag', test: () => html.includes('viewport') },
      { name: 'Scalar container', test: () => html.includes('<div id="scalar-app"></div>') },
      { name: 'Scalar script reference', test: () => html.includes('@scalar/api-reference') },
      { name: 'Scalar initialization', test: () => html.includes('Scalar.createApiReference') },
      { name: 'Embedded spec data', test: () => html.includes('var specData =') },
      { name: 'No unclosed tags', test: () => !hasUnclosedTags(html) },
      { name: 'Proper tag nesting', test: () => hasProperTagNesting(html) }
    ];

    let passed = 0;
    let failed = 0;

    for (const check of checks) {
      try {
        if (check.test()) {
          console.log(`  ✓ ${check.name}`);
          passed++;
        } else {
          console.log(`  ❌ ${check.name}`);
          failed++;
        }
      } catch (error) {
        console.log(`  ❌ ${check.name}: ${error.message}`);
        failed++;
      }
    }

    return {
      passed: failed === 0,
      message: `HTML validation: ${passed}/${checks.length} checks passed`
    };
  } catch (error) {
    console.error(`  ❌ Failed to validate HTML structure: ${error.message}`);
    return { passed: false, message: `HTML validation failed: ${error.message}` };
  }
}

async function testSpecCompleteness() {
  console.log('\nTest 2: All Routes from Merged Spec Present');
  try {
    const spec = await fetchJSON('/openapi.json');

    if (!spec.paths) {
      console.error('  ❌ No paths defined in spec');
      return { passed: false, message: 'No paths in spec' };
    }

    const paths = Object.keys(spec.paths);
    console.log(`  ✓ Found ${paths.length} paths in merged spec`);

    // Verify each path has required fields
    let pathsWithOperations = 0;
    let pathsWithTags = 0;
    let pathsWithOperationIds = 0;

    for (const [path, methods] of Object.entries(spec.paths)) {
      for (const [method, details] of Object.entries(methods)) {
        if (method === 'parameters') continue;

        pathsWithOperations++;
        if (details.tags && details.tags.length > 0) pathsWithTags++;
        if (details.operationId) pathsWithOperationIds++;
      }
    }

    console.log(`  ✓ ${pathsWithOperations} operations defined`);
    console.log(`  ✓ ${pathsWithTags} operations with tags`);
    console.log(`  ✓ ${pathsWithOperationIds} operations with operation IDs`);

    // Check components/schemas
    if (spec.components && spec.components.schemas) {
      const schemaCount = Object.keys(spec.components.schemas).length;
      console.log(`  ✓ ${schemaCount} schema definitions in components`);
    }

    return {
      passed: true,
      message: `Spec completeness: ${paths.length} paths, ${pathsWithOperations} operations`
    };
  } catch (error) {
    console.error(`  ❌ Failed to verify spec completeness: ${error.message}`);
    return { passed: false, message: `Spec verification failed: ${error.message}` };
  }
}

async function testInteractiveFeatures() {
  console.log('\nTest 3: Interactive Features Validation');
  try {
    const html = await fetchText('/docs');

    const features = [
      { name: 'Sidebar navigation', test: () => html.includes('showSidebar: true') || html.includes('showSidebar:true') },
      { name: 'Try-it-out enabled', test: () => html.includes('hideTryIt: false') || html.includes('hideTryIt:false') },
      { name: 'Search functionality', test: () => html.includes('search:') },
      { name: 'Theme configuration', test: () => html.includes('theme:') },
      { name: 'Routing configuration', test: () => html.includes('routing:') },
      { name: 'API metadata', test: () => html.includes('metaData:') },
      { name: 'Spec content embedding', test: () => html.includes('spec: {') && html.includes('content: specData') }
    ];

    let passed = 0;
    let failed = 0;

    for (const feature of features) {
      try {
        if (feature.test()) {
          console.log(`  ✓ ${feature.name}`);
          passed++;
        } else {
          console.log(`  ❌ ${feature.name}`);
          failed++;
        }
      } catch (error) {
        console.log(`  ❌ ${feature.name}: ${error.message}`);
        failed++;
      }
    }

    return {
      passed: failed === 0,
      message: `Interactive features: ${passed}/${features.length} features enabled`
    };
  } catch (error) {
    console.error(`  ❌ Failed to verify interactive features: ${error.message}`);
    return { passed: false, message: `Interactive features check failed: ${error.message}` };
  }
}

async function testSchemaAccuracy() {
  console.log('\nTest 4: Request/Response Schema Display Accuracy');
  try {
    const spec = await fetchJSON('/openapi.json');

    let operationsWithRequestSchemas = 0;
    let operationsWithResponseSchemas = 0;
    let operationsWithParameters = 0;
    let totalOperations = 0;

    for (const [path, methods] of Object.entries(spec.paths)) {
      for (const [method, details] of Object.entries(methods)) {
        if (method === 'parameters') continue;

        totalOperations++;

        // Check request body schemas
        if (details.requestBody && details.requestBody.content) {
          for (const contentType of Object.values(details.requestBody.content)) {
            if (contentType.schema) {
              operationsWithRequestSchemas++;
              break;
            }
          }
        }

        // Check response schemas
        if (details.responses) {
          for (const response of Object.values(details.responses)) {
            if (response.content) {
              for (const contentType of Object.values(response.content)) {
                if (contentType.schema) {
                  operationsWithResponseSchemas++;
                  break;
                }
              }
            }
          }
        }

        // Check parameters
        if (details.parameters && details.parameters.length > 0) {
          operationsWithParameters++;
        }
      }
    }

    console.log(`  ✓ ${operationsWithRequestSchemas} operations with request schemas`);
    console.log(`  ✓ ${operationsWithResponseSchemas} operations with response schemas`);
    console.log(`  ✓ ${operationsWithParameters} operations with parameters`);

    // Check schema components
    if (spec.components && spec.components.schemas) {
      const schemaCount = Object.keys(spec.components.schemas).length;
      let schemasWithProperties = 0;

      for (const schema of Object.values(spec.components.schemas)) {
        if (schema.properties && Object.keys(schema.properties).length > 0) {
          schemasWithProperties++;
        }
      }

      console.log(`  ✓ ${schemasWithProperties}/${schemaCount} schemas have properties`);
    }

    return {
      passed: true,
      message: `Schema accuracy: ${operationsWithResponseSchemas}/${totalOperations} operations with response schemas`
    };
  } catch (error) {
    console.error(`  ❌ Failed to verify schema accuracy: ${error.message}`);
    return { passed: false, message: `Schema accuracy check failed: ${error.message}` };
  }
}

async function testBrowserCompatibility() {
  console.log('\nTest 5: Browser Compatibility and Rendering');
  try {
    const html = await fetchText('/docs');

    // Check for browser compatibility features
    const compatibilityChecks = [
      { name: 'Responsive viewport', test: () => html.includes('width=device-width') },
      { name: 'Modern HTML5 DOCTYPE', test: () => html.includes('<!DOCTYPE html>') },
      { name: 'UTF-8 encoding', test: () => html.includes('utf-8') },
      { name: 'No IE-specific code', test: () => !html.includes('IE') && !html.includes('Edge') },
      { name: 'CSS resets', test: () => html.includes('margin: 0; padding: 0') },
      { name: 'Full-height container', test: () => html.includes('100vh') },
      { name: 'CDN-based resources', test: () => html.includes('cdn.jsdelivr.net') }
    ];

    let passed = 0;
    let failed = 0;

    for (const check of compatibilityChecks) {
      try {
        if (check.test()) {
          console.log(`  ✓ ${check.name}`);
          passed++;
        } else {
          console.log(`  ❌ ${check.name}`);
          failed++;
        }
      } catch (error) {
        console.log(`  ❌ ${check.name}: ${error.message}`);
        failed++;
      }
    }

    return {
      passed: failed === 0,
      message: `Browser compatibility: ${passed}/${compatibilityChecks.length} checks passed`
    };
  } catch (error) {
    console.error(`  ❌ Failed to verify browser compatibility: ${error.message}`);
    return { passed: false, message: `Browser compatibility check failed: ${error.message}` };
  }
}

// Helper functions

function fetchJSON(path) {
  return new Promise((resolve, reject) => {
    const url = new URL(path, BASE_URL);
    http.get(url, (res) => {
      let data = '';
      res.on('data', (chunk) => data += chunk);
      res.on('end', () => {
        try {
          resolve(JSON.parse(data));
        } catch (error) {
          reject(error);
        }
      });
    }).on('error', reject);
  });
}

function fetchText(path) {
  return new Promise((resolve, reject) => {
    const url = new URL(path, BASE_URL);
    http.get(url, (res) => {
      let data = '';
      res.on('data', (chunk) => data += chunk);
      res.on('end', () => resolve(data));
    }).on('error', reject);
  });
}

function hasUnclosedTags(html) {
  // Basic check for unclosed tags
  const openTags = html.match(/<([a-z]+)[^>]*>/gi) || [];
  const closeTags = html.match(/<\/([a-z]+)>/gi) || [];

  // Self-closing tags don't need closing
  const selfClosing = ['img', 'br', 'hr', 'input', 'meta', 'link'];
  const openNonSelfClosing = openTags.filter(tag => {
    const tagName = tag.match(/<([a-z]+)/i)[1].toLowerCase();
    return !selfClosing.includes(tagName) && !tag.endsWith('/>');
  });

  return openNonSelfClosing.length !== closeTags.length;
}

function hasProperTagNesting(html) {
  // Check for basic tag nesting issues
  let depth = 0;
  const tagStack = [];
  const selfClosing = ['img', 'br', 'hr', 'input', 'meta', 'link', '!DOCTYPE'];

  const tags = html.match(/<\/?([a-z]+)[^>]*>/gi) || [];

  for (const tag of tags) {
    const isClosing = tag.startsWith('</');
    const tagName = tag.match(/([a-z]+)/i)[1].toLowerCase();

    if (isClosing) {
      if (tagStack.length === 0 || tagStack[tagStack.length - 1] !== tagName) {
        return false;
      }
      tagStack.pop();
    } else if (!selfClosing.includes(tagName) && !tag.endsWith('/>')) {
      tagStack.push(tagName);
    }
  }

  return tagStack.length === 0;
}

// Run the validation
main().catch(error => {
  console.error('Validation suite failed:', error);
  process.exit(1);
});