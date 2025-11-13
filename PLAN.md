# MCP Connection Retry and Health Monitoring Plan

## Problem
- After tool calling fails (either tool execution fails or retrieving tools fails), agent does NOT actively retry restoring communication with MCP server
- After MCP refuses to talk after certain times, agent does NOT deem MCP dead and stop entire task execution
- Agent can get stuck in infinite retry loops when MCP server is completely dead

## Solution Architecture

### 1. MCP Connection Health Tracking
- Add `mcpHealth` tracking to `internal/mcp/client.go`
- Track: consecutive connection failures, last successful call, connection state
- Detect patterns: connection lost, server unresponsive, server dead

### 2. Automatic Connection Retry
- Add `Reconnect()` method to MCP client
- Retry connection on failures (with exponential backoff)
- Track retry attempts and success/failure

### 3. MCP Health Monitoring
- Add `CheckMCPHealth()` method that tests connection
- Called periodically or on failures
- Determines: `healthy`, `degraded`, `unresponsive`, `dead`

### 4. LLM-Driven MCP Death Detection
- Add `EvaluateMCPHealth()` to `internal/llm/coordinator.go`
- LLM decides: `continue`, `retry_connection`, `stop_task`
- Context includes: failure patterns, retry attempts, task progress

### 5. Task Termination on MCP Death
- When MCP is deemed dead by LLM, stop entire task execution
- Save task state before termination
- Provide clear error message about MCP failure

### 6. Integration Points
- `internal/mcp/client.go`: Add health tracking and retry logic
- `internal/task/executor.go`: Check MCP health before tool calls, retry on failure
- `internal/llm/coordinator.go`: Add MCP health evaluation method
- `main.go`, `internal/loop/manager.go`: Handle MCP death and task termination

## Implementation Steps

1. **Add MCP Health Tracking** (`internal/mcp/client.go`)
   - Add health metrics: consecutive failures, last success, retry count
   - Add `Reconnect()` method with retry logic
   - Add `CheckMCPHealth()` method

2. **Add LLM MCP Health Evaluation** (`internal/llm/coordinator.go`)
   - Add `EvaluateMCPHealth()` method
   - LLM decides: continue, retry connection, or stop task

3. **Integrate Health Checks** (`internal/task/executor.go`)
   - Check MCP health before tool calls
   - Retry connection on failures
   - Call LLM when MCP health is poor

4. **Add Task Termination Logic** (`main.go`, `internal/loop/manager.go`)
   - Detect MCP death from LLM evaluation
   - Stop task execution gracefully
   - Save state and provide clear error

## Key Principles
- **Proactive Health Monitoring**: Check MCP health before failures occur
- **Automatic Retry**: Retry connection automatically with backoff
- **LLM-Driven Decisions**: LLM decides when MCP is dead vs recoverable
- **Graceful Termination**: Stop task cleanly when MCP is dead, don't loop forever

