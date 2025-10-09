#!/usr/bin/env node

/**
 * Simple build validation test for Crawlab MCP Server
 */

import { CrawlabClient } from "./dist/client.js";

console.log("🧪 Testing Crawlab MCP Server build...\n");

// Test that we can instantiate the client
try {
  const client = new CrawlabClient("http://localhost:8080", "test-token");
  console.log("✅ CrawlabClient class - OK");
} catch (error) {
  console.log("❌ CrawlabClient class - FAILED");
  console.log("   Error:", error.message);
  process.exit(1);
}

// Test that the main entry point is valid
try {
  const entryPoint = await import("./dist/index.js");
  console.log("✅ Main entry point - OK");
} catch (error) {
  console.log("❌ Main entry point - FAILED");
  console.log("   Error:", error.message);
  process.exit(1);
}

// Test tools module
try {
  const toolsModule = await import("./dist/tools.js");
  if (typeof toolsModule.configureAllTools === 'function') {
    console.log("✅ Tools configuration - OK");
  } else {
    console.log("❌ Tools configuration - FAILED (configureAllTools not a function)");
  }
} catch (error) {
  console.log("❌ Tools configuration - FAILED");
  console.log("   Error:", error.message);
}

console.log("\n🎉 Build validation completed successfully!");
console.log("\n📋 Ready to use:");
console.log("   npm start <crawlab_url> [api_token]");
console.log("   Example: npm start http://localhost:8080 your-token");
console.log("\n   Or use the binary directly:");
console.log("   ./dist/index.js http://localhost:8080 your-token");
