# Crawlab MCP Server

A Model Context Protocol (MCP) server for interacting with [Crawlab](https://github.com/crawlab-team/crawlab), a distributed web crawler management platform. This server provides tools to manage spiders, tasks, schedules, and monitor your Crawlab cluster through an AI assistant.

## Features

### Spider Management
- List, create, update, and delete spiders
- Run spiders with custom parameters
- Browse and edit spider files
- View spider execution history

### Task Management
- Monitor running and completed tasks
- Cancel, restart, and delete tasks
- View task logs and results
- Filter tasks by spider, status, or time range

### Schedule Management
- Create and manage cron-based schedules
- Enable/disable schedules
- View scheduled task history

### Node Monitoring
- List cluster nodes and their status
- Monitor node health and availability

### System Monitoring
- Health checks and system status
- Comprehensive cluster overview

## Installation

```bash
npm install
npm run build
```

## Usage

### Basic Usage

```bash
# Start the MCP server
mcp-server-crawlab <crawlab_url> [api_token]

# Examples:
mcp-server-crawlab http://localhost:8080
mcp-server-crawlab https://crawlab.example.com your-api-token
```

### Environment Variables

You can also set the API token via environment variable:

```bash
export CRAWLAB_API_TOKEN=your-api-token
mcp-server-crawlab http://localhost:8080
```

### With MCP Inspector

For development and testing, you can use the MCP Inspector:

```bash
npm run inspect
```

### Integration with AI Assistants

This MCP server is designed to work with AI assistants that support the Model Context Protocol. Configure your AI assistant to connect to this server to enable Crawlab management capabilities.

## Available Tools

### Spider Tools
- `crawlab_list_spiders` - List all spiders with optional pagination
- `crawlab_get_spider` - Get detailed information about a specific spider
- `crawlab_create_spider` - Create a new spider
- `crawlab_update_spider` - Update spider configuration
- `crawlab_delete_spider` - Delete a spider
- `crawlab_run_spider` - Execute a spider
- `crawlab_list_spider_files` - Browse spider files and directories
- `crawlab_get_spider_file_content` - Read spider file content
- `crawlab_save_spider_file` - Save content to spider files

### Task Tools
- `crawlab_list_tasks` - List tasks with filtering options
- `crawlab_get_task` - Get detailed task information
- `crawlab_cancel_task` - Cancel a running task
- `crawlab_restart_task` - Restart a completed or failed task
- `crawlab_delete_task` - Delete a task
- `crawlab_get_task_logs` - Retrieve task execution logs
- `crawlab_get_task_results` - Get data collected by a task

### Schedule Tools
- `crawlab_list_schedules` - List all schedules
- `crawlab_get_schedule` - Get schedule details
- `crawlab_create_schedule` - Create a new cron schedule
- `crawlab_update_schedule` - Update schedule configuration
- `crawlab_delete_schedule` - Delete a schedule
- `crawlab_enable_schedule` - Enable a schedule
- `crawlab_disable_schedule` - Disable a schedule

### Node Tools
- `crawlab_list_nodes` - List cluster nodes
- `crawlab_get_node` - Get node details and status

### System Tools
- `crawlab_health_check` - Check system health
- `crawlab_system_status` - Get comprehensive system overview

## Available Prompts

The server includes several helpful prompts for common workflows:

### `spider-analysis`
Analyze spider performance and provide optimization insights.

**Parameters:**
- `spider_id` (required) - ID of the spider to analyze
- `time_range` (optional) - Time range for analysis (e.g., '7d', '30d', '90d')

### `task-debugging`
Debug failed tasks and identify root causes.

**Parameters:**
- `task_id` (required) - ID of the failed task

### `spider-setup`
Guide for creating and configuring new spiders.

**Parameters:**
- `spider_name` (required) - Name for the new spider
- `target_website` (optional) - Target website to scrape
- `spider_type` (optional) - Type of spider (scrapy, selenium, custom)

### `system-monitoring`
Monitor system health and performance.

**Parameters:**
- `focus_area` (optional) - Area to focus on (nodes, tasks, storage, overall)

## Example Interactions

### Create and Run a Spider
```
AI: I'll help you create a new spider for scraping news articles.

[Uses crawlab_create_spider with appropriate parameters]
[Uses crawlab_run_spider to test the spider]
[Uses crawlab_get_task_logs to check execution]
```

### Debug a Failed Task
```
User: "My task abc123 failed, can you help me debug it?"

[Uses task-debugging prompt]
[AI retrieves task details, logs, and provides analysis]
```

### Monitor System Health
```
User: "How is my Crawlab cluster performing?"

[Uses system-monitoring prompt]
[AI provides comprehensive health overview and recommendations]
```

## Configuration

### Crawlab Setup

Ensure your Crawlab instance is accessible and optionally configure API authentication:

1. Make sure Crawlab is running and accessible at the specified URL
2. If using authentication, obtain an API token from your Crawlab instance
3. Configure the token via command line argument or environment variable

### MCP Client Configuration

Add this server to your MCP client configuration:

```json
{
  "servers": {
    "crawlab": {
      "command": "mcp-server-crawlab",
      "args": ["http://localhost:8080", "your-api-token"]
    }
  }
}
```

## Development

### Building
```bash
npm run build
```

### Watching for Changes
```bash
npm run watch
```

### Testing
```bash
npm test
```

### Linting
```bash
npm run lint
npm run lint:fix
```

## Requirements

- Node.js 18+
- A running Crawlab instance
- Valid network access to the Crawlab API

## License

MIT License

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests if applicable
5. Submit a pull request

## Support

For issues and questions:
- Check the [Crawlab documentation](https://docs.crawlab.cn)
- Review the [MCP specification](https://modelcontextprotocol.io)
- Open an issue in this repository
