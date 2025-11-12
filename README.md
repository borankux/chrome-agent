# Chrome Agent

A Go-based agent that uses Microsoft Playwright MCP to control browser instances, manage tasks with subtasks, handle exceptions requiring human intervention, and coordinate tool usage via LLM reasoning.

## Features

- **MCP Integration**: Connects to Playwright MCP server using standard MCP JSON definition
- **LLM Coordination**: Uses OpenAI API for intelligent task breakdown and tool selection
- **Task Management**: Parses tasks from `task.txt`, breaks into subtasks, tracks execution
- **Loop Support**: Handles time-based and quota-based repeating tasks
- **Exception Handling**: Detects exceptions, classifies them, and pauses for human intervention when needed
- **State Persistence**: SQLite database for storing tasks, subtasks, loops, and exceptions
- **Formatted Logging**: Human-readable, non-noisy logs with context

## Prerequisites

- Go 1.21 or later
- OpenAI API key (set as `OPENAI_API_KEY` environment variable)
- Playwright MCP server (or compatible MCP server)

## Setup

1. Clone the repository and navigate to the directory:
```bash
cd chrome-agent
```

2. Install dependencies:
```bash
go mod download
```

3. Set your OpenAI API key:
```bash
export OPENAI_API_KEY=your-api-key-here
```

4. Create MCP configuration file (`mcp-config.json`):
   - Copy `mcp-config.json.example` to `mcp-config.json`
   - Update the configuration to match your MCP server setup
   - Ensure the transport command points to your Playwright MCP server

5. Create task file (`task.txt`):
   - See `task.txt` for an example
   - Define your task objective, subtasks, loop conditions, and exception rules

## Usage

Run the agent:
```bash
go run main.go
```

Or build and run:
```bash
go build -o chrome-agent .
./chrome-agent
```

### Command Line Options

- `-task`: Path to task specification file (default: `task.txt`)
- `-mcp`: Path to MCP configuration file (default: `mcp-config.json`)
- `-db`: Path to SQLite database file (default: `agent.db`)
- `-log`: Log level: DEBUG, INFO, WARN, ERROR (default: INFO)

Example:
```bash
./chrome-agent -task mytask.txt -mcp my-mcp-config.json -log DEBUG
```

## Task File Format

The `task.txt` file uses a simple text format:

```
Objective: Your main task objective here

Loop: for 1 hour
# OR
Loop: find 10 items

Subtask: Subtask Name
- Step 1 description
- Step 2 description
- Step 3 description

Exception: pattern, intervention: true, retryable: false
Exception: network timeout, intervention: false, retryable: true, maxretries: 3
```

### Loop Types

- **Time-based**: `Loop: for 1 hour` or `Loop: for 30 minutes`
- **Quota-based**: `Loop: find 10 items` or `Loop: collect 5 creators`

### Exception Rules

- `pattern`: Error message pattern to match (regex)
- `intervention`: Whether human intervention is required (true/false)
- `retryable`: Whether the exception can be retried (true/false)
- `maxretries`: Maximum number of retry attempts (default: 3)

## MCP Configuration

The MCP configuration file (`mcp-config.json`) follows the standard MCP JSON definition format:

```json
{
  "name": "server-name",
  "version": "1.0.0",
  "description": "Server description",
  "transport": {
    "type": "stdio",
    "command": ["command", "args"],
    "env": {}
  },
  "tools": [...]
}
```

The agent will:
1. Load and validate the MCP definition on startup
2. Connect to the MCP server
3. Output a status report showing available tools and connection status
4. Use the tools during task execution

## Database Schema

The SQLite database (`agent.db`) stores:

- **tasks**: Main tasks with status and results
- **subtasks**: Individual subtasks with cycle numbers
- **loops**: Loop tracking (time/quota progress)
- **exceptions**: Exception records with intervention flags

## Architecture

The agent is structured for future web service migration:

- `internal/mcp/`: MCP client and definition parsing
- `internal/llm/`: LLM coordinator for reasoning
- `internal/task/`: Task parsing, management, and execution
- `internal/state/`: SQLite database operations
- `internal/loop/`: Loop execution management
- `internal/exception/`: Exception handling and intervention
- `pkg/logger/`: Formatted logging utility

## Human Intervention

When an exception requires human intervention:

1. The agent pauses execution
2. Logs the exception details
3. Updates task status to `paused`
4. Waits for user to resolve the issue
5. User can restart the agent to resume (exceptions are marked as resolved)

## Example Task

See `task.txt` for a complete example of watching videos and interacting with comments for 1 hour.

## Future Enhancements

- Web service API for queue-based task processing
- Structured JSON logging option
- Enhanced exception recovery strategies
- Task resumption after intervention
- Web UI for monitoring and control

## License

MIT

