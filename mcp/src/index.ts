#!/usr/bin/env node

import { McpServer } from '@modelcontextprotocol/sdk/server/mcp.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { configurePrompts } from './prompts.js';
import { configureAllTools } from './tools.js';
import { CrawlabClient } from './client.js';
import { packageVersion } from './version.js';

const args = process.argv.slice(2);
if (args.length < 1) {
  console.error('Usage: mcp-server-crawlab <crawlab_api_endpoint> [api_token]');
  console.error('Example (Docker): mcp-server-crawlab http://localhost:8080/api');
  console.error('Example (Local dev): mcp-server-crawlab http://localhost:8000');
  console.error(
    'Example with token: mcp-server-crawlab http://localhost:8080/api your-api-token'
  );
  process.exit(1);
}

const crawlabApiEndpoint = args[0] || process.env.CRAWLAB_API_URL || process.env.CRAWLAB_API_ENDPOINT;
const apiToken = args[1] || process.env.CRAWLAB_API_TOKEN;

// Check for missing API URL
if (!crawlabApiEndpoint) {
  console.error('❌ ERROR: Crawlab API URL is required!');
  console.error('   Provide API URL via:');
  console.error('   1. Command argument: mcp-server-crawlab <api_endpoint> [token]');
  console.error('   2. Environment variable: export CRAWLAB_API_ENDPOINT=http://localhost:8080/api');
  console.error('   Examples:');
  console.error('   - Docker: http://localhost:8080/api');
  console.error('   - Local dev: http://localhost:8000');
  console.error('');
  process.exit(1);
}

// Warning if API token is missing
if (!apiToken) {
  console.error('⚠️  WARNING: CRAWLAB_API_TOKEN is not set!');
  console.error('   This may cause authentication issues with the Crawlab API.');
  console.error('   To fix this:');
  console.error('   1. Set environment variable: export CRAWLAB_API_TOKEN=your_token');
  console.error('   2. Or pass token as argument: mcp-server-crawlab <api_endpoint> <token>');
  console.error('   3. Or add to your shell profile (~/.zshrc, ~/.bashrc)');
  console.error('');
}

async function main() {
  const server = new McpServer({
    name: 'Crawlab MCP Server',
    version: packageVersion,
  });

  // Initialize Crawlab client
  const client = new CrawlabClient(crawlabApiEndpoint!, apiToken);

  // Configure prompts and tools
  configurePrompts(server);
  configureAllTools(server, client);

  const transport = new StdioServerTransport();
  console.info(`Crawlab MCP Server version: ${packageVersion}`);
  console.info(`Crawlab API endpoint: ${crawlabApiEndpoint}`);
  console.info(`API token: ${apiToken ? `${apiToken.substring(0, 8)}...` : 'Not provided'}`);

  await server.connect(transport);
}

main().catch(error => {
  console.error('Fatal error in main():', error);
  process.exit(1);
});
