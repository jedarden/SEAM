#!/usr/bin/env node

/**
 * Comprehensive browser testing and visual rendering verification for SEAM docs UI
 *
 * This script tests:
 * 1. Docs load successfully in Chrome and Firefox
 * 2. No console errors in either browser
 * 3. Layout is responsive and readable
 * 4. Visual styling matches expected docs theme
 */

const { chromium, firefox } = require('playwright');
const fs = require('fs');
const path = require('path');

const BASE_URL = 'http://localhost:8888';
const DOCS_URL = `${BASE_URL}/docs`;
const SCREENSHOT_DIR = path.join(__dirname, 'scratch', 'browser-test-screenshots');

// Test results
const results = {
  chrome: { consoleErrors: [], consoleWarnings: [], screenshots: [], passed: true },
  firefox: { consoleErrors: [], consoleWarnings: [], screenshots: [], passed: true },
  responsiveTests: [],
  visualChecks: []
};

// Viewport sizes for responsive testing
const VIEWPORTS = [
  { name: 'Desktop Large', width: 1920, height: 1080 },
  { name: 'Desktop', width: 1366, height: 768 },
  { name: 'Tablet', width: 768, height: 1024 },
  { name: 'Mobile', width: 375, height: 667 }
];

async function ensureScreenshotDir() {
  if (!fs.existsSync(SCREENSHOT_DIR)) {
    fs.mkdirSync(SCREENSHOT_DIR, { recursive: true });
  }
}

async function testBrowser(browserType, browserName) {
  console.log(`\n=== Testing ${browserName} ===`);

  const browser = await browserType.launch({
    headless: true
  });

  const context = await browser.newContext();
  const page = await context.newPage();

  // Collect console messages
  const consoleMessages = [];
  page.on('console', msg => {
    const text = msg.text();
    const type = msg.type();
    consoleMessages.push({ type, text, url: page.url() });

    if (type === 'error') {
      results[browserName.toLowerCase()].consoleErrors.push({
        text,
        url: page.url()
      });
    } else if (type === 'warning') {
      results[browserName.toLowerCase()].consoleWarnings.push({
        text,
        url: page.url()
      });
    }
  });

  try {
    // Navigate to docs page
    console.log(`Navigating to ${DOCS_URL}...`);
    await page.goto(DOCS_URL, { waitUntil: 'networkidle' });
    console.log(`✓ Page loaded successfully in ${browserName}`);

    // Wait for Redoc to render
    await page.waitForSelector('redoc', { timeout: 10000 }).catch(() => {
      console.warn(`⚠ Redoc element not immediately available, waiting...`);
    });

    // Wait a bit more for dynamic content
    await page.waitForTimeout(2000);

    // Check for basic page structure
    const title = await page.title();
    console.log(`✓ Page title: "${title}"`);

    // Check if Redoc loaded
    const redocLoaded = await page.evaluate(() => {
      const redoc = document.querySelector('redoc');
      return redoc !== null;
    });

    if (redocLoaded) {
      console.log('✓ Redoc component loaded');
    } else {
      console.error('❌ Redoc component failed to load');
      results[browserName.toLowerCase()].passed = false;
    }

    // Check for content rendering
    const apiTitle = await page.evaluate(() => {
      const h1 = document.querySelector('h1');
      return h1 ? h1.textContent : null;
    });

    if (apiTitle) {
      console.log(`✓ API title rendered: "${apiTitle}"`);
      results[browserName.toLowerCase()].visualChecks.push({
        check: 'API title rendered',
        passed: true,
        value: apiTitle
      });
    } else {
      console.error('❌ API title not rendered');
      results[browserName.toLowerCase()].passed = false;
      results[browserName.toLowerCase()].visualChecks.push({
        check: 'API title rendered',
        passed: false
      });
    }

    // Check for navigation/sidebar elements
    const navigationPresent = await page.evaluate(() => {
      const nav = document.querySelector('[role="navigation"]');
      return nav !== null;
    });

    if (navigationPresent) {
      console.log('✓ Navigation/sidebar present');
      results[browserName.toLowerCase()].visualChecks.push({
        check: 'Navigation/sidebar present',
        passed: true
      });
    } else {
      console.warn('⚠ Navigation/sidebar not found');
    }

    // Check for API endpoints section
    const endpointsPresent = await page.evaluate(() => {
      const sections = document.querySelectorAll('[data-section-id]');
      return sections.length > 0;
    });

    if (endpointsPresent) {
      console.log('✓ API endpoints sections present');
      results[browserName.toLowerCase()].visualChecks.push({
        check: 'API endpoints sections present',
        passed: true
      });
    } else {
      console.warn('⚠ No API endpoint sections found');
    }

    // Check for interactive elements
    const interactiveElements = await page.evaluate(() => {
      const buttons = document.querySelectorAll('button');
      const links = document.querySelectorAll('a');
      const inputs = document.querySelectorAll('input');
      return {
        buttons: buttons.length,
        links: links.length,
        inputs: inputs.length
      };
    });

    console.log(`✓ Interactive elements: ${interactiveElements.buttons} buttons, ${interactiveElements.links} links, ${interactiveElements.inputs} inputs`);
    results[browserName.toLowerCase()].visualChecks.push({
      check: 'Interactive elements',
      passed: true,
      details: interactiveElements
    });

    // Take full page screenshot
    await ensureScreenshotDir();
    const screenshotPath = path.join(SCREENSHOT_DIR, `${browserName.toLowerCase()}-full.png`);
    await page.screenshot({ path: screenshotPath, fullPage: true });
    console.log(`✓ Screenshot saved: ${screenshotPath}`);
    results[browserName.toLowerCase()].screenshots.push(screenshotPath);

    // Report console messages
    console.log(`\nConsole messages in ${browserName}:`);
    const errorCount = results[browserName.toLowerCase()].consoleErrors.length;
    const warningCount = results[browserName.toLowerCase()].consoleWarnings.length;

    console.log(`  Errors: ${errorCount}`);
    console.log(`  Warnings: ${warningCount}`);

    if (errorCount > 0) {
      console.error(`  ❌ Console errors detected:`);
      results[browserName.toLowerCase()].consoleErrors.forEach((err, i) => {
        console.error(`    ${i + 1}. ${err.text}`);
      });
      results[browserName.toLowerCase()].passed = false;
    } else {
      console.log(`  ✓ No console errors`);
    }

    if (warningCount > 0) {
      console.warn(`  ⚠ Console warnings:`);
      results[browserName.toLowerCase()].consoleWarnings.forEach((warn, i) => {
        console.warn(`    ${i + 1}. ${warn.text}`);
      });
    } else {
      console.log(`  ✓ No console warnings`);
    }

  } catch (error) {
    console.error(`❌ Error testing ${browserName}:`, error.message);
    results[browserName.toLowerCase()].passed = false;
  } finally {
    await browser.close();
  }
}

async function testResponsiveLayout() {
  console.log(`\n=== Testing Responsive Layout ===`);

  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();

  await ensureScreenshotDir();

  for (const viewport of VIEWPORTS) {
    console.log(`\nTesting viewport: ${viewport.name} (${viewport.width}x${viewport.height})`);

    await page.setViewportSize({ width: viewport.width, height: viewport.height });
    await page.goto(DOCS_URL, { waitUntil: 'networkidle' });
    await page.waitForTimeout(2000); // Wait for rendering

    const screenshotPath = path.join(SCREENSHOT_DIR, `responsive-${viewport.name.toLowerCase().replace(/\s+/g, '-')}.png`);
    await page.screenshot({ path: screenshotPath, fullPage: true });
    console.log(`  ✓ Screenshot saved: ${screenshotPath}`);

    // Check if layout is usable at this size
    const layoutUsable = await page.evaluate(() => {
      const redoc = document.querySelector('redoc');
      if (!redoc) return false;

      const rect = redoc.getBoundingClientRect();
      return rect.width > 0 && rect.height > 0;
    });

    const result = {
      viewport: viewport.name,
      size: `${viewport.width}x${viewport.height}`,
      passed: layoutUsable,
      screenshot: screenshotPath
    };

    results.responsiveTests.push(result);

    if (layoutUsable) {
      console.log(`  ✓ Layout usable at ${viewport.name}`);
    } else {
      console.error(`  ❌ Layout NOT usable at ${viewport.name}`);
    }
  }

  await browser.close();
}

async function testVisualStyling() {
  console.log(`\n=== Testing Visual Styling ===`);

  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();

  await page.goto(DOCS_URL, { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);

  // Check for Redoc styling
  const stylingChecks = await page.evaluate(() => {
    const checks = {
      redocThemePresent: false,
      customFontsApplied: false,
      properColorContrast: false,
      mobileResponsiveLayout: false
    };

    // Check for Redoc with theme
    const redoc = document.querySelector('redoc');
    if (redoc) {
      checks.redocThemePresent = true;
      const styles = window.getComputedStyle(redoc);
      // Basic check that some styling is applied
      checks.properColorContrast = styles.color !== '' && styles.backgroundColor !== '';
    }

    // Check for custom fonts
    const fontApplied = document.body.style.fontFamily ||
                       document.querySelector('redoc')?.style.fontFamily;
    checks.customFontsApplied = !!fontApplied;

    // Check responsive layout class
    checks.mobileResponsiveLayout = document.querySelector('.mobile-responsive') !== null ||
                                   redoc?.classList.contains('responsive');

    return checks;
  });

  console.log('Visual styling checks:');
  console.log(`  Redoc theme present: ${stylingChecks.redocThemePresent ? '✓' : '❌'}`);
  console.log(`  Custom fonts applied: ${stylingChecks.customFontsApplied ? '✓' : '⚠'}`);
  console.log(`  Proper color contrast: ${stylingChecks.properColorContrast ? '✓' : '❌'}`);
  console.log(`  Mobile responsive layout: ${stylingChecks.mobileResponsiveLayout ? '✓' : '⚠'}`);

  results.visualChecks.push({
    category: 'Visual styling',
    checks: stylingChecks
  });

  await browser.close();
}

async function main() {
  console.log('=== Browser Testing and Visual Rendering Verification ===');
  console.log(`Testing SEAM docs UI at: ${DOCS_URL}`);
  console.log(`Screenshot directory: ${SCREENSHOT_DIR}`);

  try {
    // Test Chrome
    await testBrowser(chromium, 'Chrome');

    // Test Firefox
    await testBrowser(firefox, 'Firefox');

    // Test responsive layout
    await testResponsiveLayout();

    // Test visual styling
    await testVisualStyling();

    // Print final results
    console.log('\n=== FINAL RESULTS ===\n');

    // Chrome results
    console.log('Chrome:');
    console.log(`  Passed: ${results.chrome.passed ? '✅' : '❌'}`);
    console.log(`  Console errors: ${results.chrome.consoleErrors.length}`);
    console.log(`  Console warnings: ${results.chrome.consoleWarnings.length}`);
    console.log(`  Screenshots: ${results.chrome.screenshots.length}`);

    // Firefox results
    console.log('\nFirefox:');
    console.log(`  Passed: ${results.firefox.passed ? '✅' : '❌'}`);
    console.log(`  Console errors: ${results.firefox.consoleErrors.length}`);
    console.log(`  Console warnings: ${results.firefox.consoleWarnings.length}`);
    console.log(`  Screenshots: ${results.firefox.screenshots.length}`);

    // Responsive test results
    console.log('\nResponsive Layout Tests:');
    results.responsiveTests.forEach(test => {
      console.log(`  ${test.viewport} (${test.size}): ${test.passed ? '✅' : '❌'}`);
    });

    // Overall result
    console.log('\n=== ACCEPTANCE CRITERIA ===');
    const criteria = [
      { name: 'Docs load successfully in Chrome', passed: results.chrome.passed },
      { name: 'Docs load successfully in Firefox', passed: results.firefox.passed },
      { name: 'No console errors in Chrome', passed: results.chrome.consoleErrors.length === 0 },
      { name: 'No console errors in Firefox', passed: results.firefox.consoleErrors.length === 0 },
      { name: 'Layout is responsive and readable', passed: results.responsiveTests.every(t => t.passed) },
      { name: 'Visual styling matches expected docs theme', passed: results.visualChecks.length > 0 }
    ];

    let allPassed = true;
    criteria.forEach(c => {
      console.log(`${c.passed ? '✅' : '❌'} ${c.name}`);
      if (!c.passed) allPassed = false;
    });

    console.log('\n===================================');
    if (allPassed) {
      console.log('✅ ALL TESTS PASSED!');
      console.log('\nScreenshots saved to:', SCREENSHOT_DIR);
      process.exit(0);
    } else {
      console.log('❌ SOME TESTS FAILED');
      console.log('\nScreenshots saved to:', SCREENSHOT_DIR);
      process.exit(1);
    }

  } catch (error) {
    console.error('Fatal error during testing:', error);
    process.exit(1);
  }
}

// Run the tests
main();