import { chromium, firefox, webkit } from 'playwright';
import fs from 'fs';

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
      throw new Error(`HTTP ${response.status()}: ${response.statusText()}`);
    }

    console.log(`  ✓ Page loaded successfully (HTTP ${response.status()})`);

    // Wait for ReDoc to fully render
    await page.waitForSelector('#redoc-container', { timeout: 10000 });
    console.log(`  ✓ ReDoc container found`);

    // Check if content is rendered
    const apiDocExists = await page.locator('#redoc-container').count() > 0;
    if (!apiDocExists) {
      throw new Error('ReDoc container not found');
    }

    // Wait a bit more for dynamic content
    await page.waitForTimeout(3000);

    // Check for actual content
    const hasContent = await page.evaluate(() => {
      const apiDoc = document.getElementById('redoc-container');
      return apiDoc && apiDoc.children.length > 0;
    });

    if (hasContent) {
      console.log(`  ✓ ReDoc content rendered`);
    } else {
      console.warn(`  ⚠ ReDoc container exists but appears empty`);
    }

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
    results[name.toLowerCase()].errors.push(error.message);
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
  } else {
    console.log('✗ Tests failed - Console errors detected');
    process.exit(1);
  }
}

main().catch(error => {
  console.error('Fatal error:', error);
  process.exit(1);
});
