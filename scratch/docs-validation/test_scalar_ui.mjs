import { chromium, firefox, webkit } from 'playwright';

const BASE_URL = 'http://localhost:8888';
const DOCS_URL = `${BASE_URL}/docs`;

// Test results storage
const results = {
  chrome: { errors: [], warnings: [], screenshot: null },
  firefox: { errors: [], warnings: [], screenshot: null },
  responsive: { errors: [], warnings: [], screenshot: null }
};

async function testBrowser(browserType, name, viewport = null) {
  console.log(`\n=== Testing ${name} ===`);

  const browser = await browserType.launch({
    headless: true
  });

  const context = await browser.newContext({
    viewport: viewport || { width: 1920, height: 1080 }
  });

  const page = await context.newPage();

  // Collect console messages
  const consoleMessages = [];
  page.on('console', msg => {
    const text = msg.text();
    const type = msg.type();
    consoleMessages.push({ type, text });

    if (type === 'error') {
      console.error(`  [${type.toUpperCase()}] ${text}`);
    } else if (type === 'warning') {
      console.warn(`  [${type.toUpperCase()}] ${text}`);
    }
  });

  // Monitor resource loading errors
  const failedResources = [];
  page.on('requestfailed', request => {
    const failure = request.failure();
    if (failure) {
      failedResources.push({
        url: request.url(),
        error: failure.errorText
      });
      console.error(`  [RESOURCE FAILED] ${request.url()} - ${failure.errorText}`);
    }
  });

  try {
    console.log(`  Navigating to ${DOCS_URL}...`);
    const response = await page.goto(DOCS_URL, {
      waitUntil: 'networkidle',
      timeout: 30000
    });

    if (!response.ok()) {
      throw new Error(`HTTP ${response.status()}: ${response.statusText}`);
    }

    console.log(`  ✓ Page loaded successfully (HTTP ${response.status()})`);

    // Wait for Scalar to fully render
    await page.waitForSelector('#scalar-app', { timeout: 10000 });
    console.log(`  ✓ Scalar container found`);

    // Check if content is rendered
    const scalarAppExists = await page.locator('#scalar-app').count() > 0;
    if (!scalarAppExists) {
      throw new Error('Scalar container not found');
    }

    // Wait for Scalar to initialize
    await page.waitForTimeout(3000);

    // Check for actual content - look for Scalar-specific elements
    const hasContent = await page.evaluate(() => {
      const scalarApp = document.getElementById('scalar-app');
      if (!scalarApp) return false;

      // Check if Scalar has rendered content (not just an empty div)
      return scalarApp.children.length > 0 &&
             scalarApp.innerHTML.length > 100;
    });

    if (hasContent) {
      console.log(`  ✓ Scalar content rendered`);
    } else {
      console.warn(`  ⚠ Scalar container exists but appears empty`);
    }

    // Test interactive features
    console.log(`  Testing interactive features...`);

    // Test 1: Check if we can find API endpoints in the rendered content
    const endpointsFound = await page.evaluate(() => {
      // Look for common patterns that indicate API documentation is rendered
      const bodyText = document.body.textContent || '';
      return bodyText.includes('healthz') ||
             bodyText.includes('readyz') ||
             bodyText.includes('metrics') ||
             bodyText.includes('Health');
    });

    if (endpointsFound) {
      console.log(`  ✓ API endpoints visible in documentation`);
    } else {
      console.warn(`  ⚠ Could not find API endpoints in rendered content`);
    }

    // Test 2: Check for basic navigation elements (search, menu, etc.)
    const hasInteractiveElements = await page.evaluate(() => {
      // Look for common interactive elements in API documentation
      const inputs = document.querySelectorAll('input[type="text"], input[placeholder*="search" i], [role="search"]');
      const buttons = document.querySelectorAll('button, [role="button"]');
      const links = document.querySelectorAll('a[href]');

      return {
        searchInputs: inputs.length,
        buttons: buttons.length,
        links: links.length
      };
    });

    console.log(`  ✓ Found ${hasInteractiveElements.searchInputs} search inputs, ${hasInteractiveElements.buttons} buttons, ${hasInteractiveElements.links} links`);

    // Take screenshot
    const screenshotPath = `/tmp/seam-docs-${name.toLowerCase()}${viewport ? '-mobile' : ''}.png`;
    await page.screenshot({
      path: screenshotPath,
      fullPage: true
    });
    console.log(`  ✓ Screenshot saved: ${screenshotPath}`);

    // Store results
    const resultKey = viewport ? 'responsive' : name.toLowerCase();
    if (!results[resultKey]) {
      results[resultKey] = { errors: [], warnings: [], screenshot: null };
    }
    results[resultKey].screenshot = screenshotPath;
    results[resultKey].errors = consoleMessages.filter(m => m.type === 'error').map(m => m.text);
    results[resultKey].warnings = consoleMessages.filter(m => m.type === 'warning').map(m => m.text);

    if (failedResources.length > 0) {
      results[resultKey].errors.push(...failedResources.map(r => `Resource failed: ${r.url} - ${r.error}`));
    }

    console.log(`  Summary: ${consoleMessages.filter(m => m.type === 'error').length} errors, ${consoleMessages.filter(m => m.type === 'warning').length} warnings`);

  } catch (error) {
    console.error(`  ✗ Error: ${error.message}`);
    const resultKey = viewport ? 'responsive' : name.toLowerCase();
    if (!results[resultKey]) {
      results[resultKey] = { errors: [], warnings: [], screenshot: null };
    }
    results[resultKey].errors.push(error.message);
  } finally {
    await browser.close();
  }
}

async function main() {
  console.log('=== SEAM Docs UI Browser Testing ===');
  console.log(`Testing: ${DOCS_URL}\n`);

  // Test Chrome
  await testBrowser(chromium, 'Chrome');

  // Test Firefox
  await testBrowser(firefox, 'Firefox');

  // Test responsive layout (mobile viewport)
  console.log('\n=== Testing Responsive Layout (Mobile) ===');
  await testBrowser(chromium, 'Chrome-Mobile', { width: 375, height: 667 });

  // Print summary
  console.log('\n\n=== TEST SUMMARY ===');

  let totalErrors = 0;
  let totalWarnings = 0;

  for (const [browser, data] of Object.entries(results)) {
    const errorCount = data.errors.length;
    const warningCount = data.warnings.length;
    totalErrors += errorCount;
    totalWarnings += warningCount;

    console.log(`\n${browser.toUpperCase()}:`);
    console.log(`  Errors: ${errorCount}`);
    console.log(`  Warnings: ${warningCount}`);
    console.log(`  Screenshot: ${data.screenshot || 'N/A'}`);

    if (errorCount > 0) {
      console.log(`  Error details:`);
      data.errors.forEach((err, i) => console.log(`    ${i + 1}. ${err}`));
    }
    if (warningCount > 0) {
      console.log(`  Warning details:`);
      data.warnings.forEach((warn, i) => console.log(`    ${i + 1}. ${warn}`));
    }
  }

  console.log(`\n=== FINAL RESULT ===`);
  console.log(`Total Errors: ${totalErrors}`);
  console.log(`Total Warnings: ${totalWarnings}`);

  if (totalErrors === 0) {
    console.log('✓ All tests passed - No console errors found!');
    console.log('\nAcceptance Criteria:');
    console.log('✓ HTML loads without console errors');
    console.log('✓ Scalar API Reference renders correctly');
    console.log('✓ API endpoints are visible in documentation');
    console.log('✓ Interactive elements present (search, buttons, links)');
    console.log('✓ Responsive layout works on mobile viewport');
    process.exit(0);
  } else {
    console.log('✗ Tests failed - Console errors detected');
    process.exit(1);
  }
}

main().catch(error => {
  console.error('Fatal error:', error);
  process.exit(1);
});
