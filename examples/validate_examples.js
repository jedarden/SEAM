const Ajv = require("ajv");
const addFormats = require("ajv-formats");
const fs = require("fs");
const path = require("path");

// Read the schema
const schema = JSON.parse(fs.readFileSync(path.join(__dirname, "../spec/route-fragment-schema.json"), "utf8"));

// Initialize AJV with draft-2020-12 support
const ajv = new Ajv({
  allErrors: true,
  strict: false,
  validateFormats: false
});
// Add draft-2020-12 meta-schema support
const addMetaSchema2020 = require("ajv/dist/refs/json-schema-2020-12/index.js").default;
addMetaSchema2020.call(ajv);
addFormats(ajv);

// Compile the schema
const validate = ajv.compile(schema);

// Examples to validate
const examples = [
  "argocd-read-only-proxy.json",
  "simple-secret-injection.json",
  "credential-probing.json",
  "cost-quota-limits.json",
  "complex-multi-extension.json",
  "rate-limiting-monitoring.json"
];

console.log("=== Validating SEAM Route Fragment Examples ===\n");

let passed = 0;
let failed = 0;

for (const exampleFile of examples) {
  const examplePath = path.join(__dirname, exampleFile);
  console.log(`\n🔍 Validating: ${exampleFile}`);

  try {
    const example = JSON.parse(fs.readFileSync(examplePath, "utf8"));
    const result = validate(example);

    if (result) {
      console.log(`✅ ${exampleFile} - VALID\n`);
      passed++;
    } else {
      console.log(`❌ ${exampleFile} - INVALID`);
      console.log("Errors:");
      console.log(ajv.errorsText(validate.errors, { separator: "\n  • ", indent: "  " }));
      console.log();
      failed++;
    }
  } catch (error) {
    console.log(`❌ ${exampleFile} - ERROR: ${error.message}\n`);
    failed++;
  }
}

console.log("=== Validation Summary ===");
console.log(`✅ Passed: ${passed}/${examples.length}`);
console.log(`❌ Failed: ${failed}/${examples.length}`);

if (failed > 0) {
  process.exit(1);
}

console.log("\n✅ All example fragments are valid!");
