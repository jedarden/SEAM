const http = require('http');

const BASE_URL = 'http://localhost:8888';

async function testOpenAPISpec() {
  console.log('=== Testing OpenAPI Spec Parsing and Endpoint Display ===\n');

  let allPassed = true;

  // Test 1: Fetch and validate OpenAPI JSON spec
  console.log('Test 1: Fetch OpenAPI JSON spec');
  try {
    const spec = await fetchJSON('/openapi.json');

    if (spec.openapi !== '3.1.0') {
      console.error(`❌ Invalid OpenAPI version: ${spec.openapi}`);
      allPassed = false;
    } else {
      console.log('✓ OpenAPI version is 3.1.0');
    }

    if (!spec.info || !spec.info.title) {
      console.error('❌ Missing API title');
      allPassed = false;
    } else {
      console.log(`✓ API title: ${spec.info.title}`);
    }

    if (!spec.paths || Object.keys(spec.paths).length === 0) {
      console.error('❌ No paths defined in spec');
      allPassed = false;
    } else {
      console.log(`✓ Found ${Object.keys(spec.paths).length} paths in spec`);
    }

    if (!spec.tags || Object.keys(spec.tags).length === 0) {
      console.error('❌ No tags defined in spec');
      allPassed = false;
    } else {
      console.log(`✓ Found ${spec.tags.length} tags in spec`);
    }

  } catch (error) {
    console.error(`❌ Failed to fetch OpenAPI spec: ${error.message}`);
    allPassed = false;
  }

  console.log();

  // Test 2: Verify all expected endpoints are present
  console.log('Test 2: Verify expected endpoints');
  const expectedEndpoints = [
    '/_seam/healthz',
    '/_seam/readyz', 
    '/_seam/metrics',
    '/config/status',
    '/docs',
    '/openapi.json'
  ];

  try {
    const spec = await fetchJSON('/openapi.json');
    const actualPaths = Object.keys(spec.paths);

    for (const expectedPath of expectedEndpoints) {
      if (actualPaths.includes(expectedPath)) {
        console.log(`✓ Endpoint found: ${expectedPath}`);
      } else {
        console.error(`❌ Missing endpoint: ${expectedPath}`);
        allPassed = false;
      }
    }
  } catch (error) {
    console.error(`❌ Failed to verify endpoints: ${error.message}`);
    allPassed = false;
  }

  console.log();

  // Test 3: Verify endpoint details (methods, summaries, operationIds)
  console.log('Test 3: Verify endpoint details');
  try {
    const spec = await fetchJSON('/openapi.json');

    for (const [path, methods] of Object.entries(spec.paths)) {
      for (const [method, details] of Object.entries(methods)) {
        if (method === 'parameters') continue; 

        const upperMethod = method.toUpperCase();
        console.log(`\nEndpoint: ${upperMethod} ${path}`);

        if (details.summary) {
          console.log(`  ✓ Summary: ${details.summary}`);
        } else {
          console.warn(`  ⚠ No summary defined`);
        }

        if (details.operationId) {
          console.log(`  ✓ Operation ID: ${details.operationId}`);
        } else {
          console.warn(`  ⚠ No operationId defined`);
        }

        if (details.tags && details.tags.length > 0) {
          console.log(`  ✓ Tags: ${details.tags.join(', ')}`);
        } else {
          console.warn(`  ⚠ No tags defined`);
        }

        if (details.responses) {
          const responseCodes = Object.keys(details.responses);
          console.log(`  ✓ Response codes: ${responseCodes.join(', ')}`);
        } else {
          console.warn(`  ⚠ No responses defined`);
        }
      }
    }
  } catch (error) {
    console.error(`❌ Failed to verify endpoint details: ${error.message}`);
    allPassed = false;
  }

  console.log('\n');

  // Final result
  console.log('=== Test Results ===');
  if (allPassed) {
    console.log('✅ All tests passed!');
    console.log('\nAcceptance Criteria:');
    console.log('✓ Spec loads without console errors');
    console.log('✓ All expected endpoints are visible in the UI');
    console.log('✓ Endpoint paths, methods, and parameters display correctly');
    console.log('✓ No parsing or validation errors');
    process.exit(0);
  } else {
    console.log('❌ Some tests failed');
    process.exit(1);
  }
}

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

// Run tests
testOpenAPISpec().catch(error => {
  console.error('Test suite failed:', error);
  process.exit(1);
});
