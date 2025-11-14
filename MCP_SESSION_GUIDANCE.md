# MCP Server Session Management Guidance

## Overview

This document provides guidance for implementing session management in MCP Playwright servers to enable persistent browser sessions across multiple tool calls.

## Problem Statement

By default, MCP Playwright servers are stateless - each tool call creates its own browser context/page and cleans it up immediately after. This means:

- Browser tabs close after each tool call
- Cookies, localStorage, and session state are lost
- Navigation state is not preserved
- Multi-step workflows cannot maintain browser state

## Solution: Session-Based Browser Management

To enable persistent browser sessions, the MCP server should:

1. Accept a `session_id` parameter in tool arguments
2. Maintain a mapping of `session_id -> BrowserContext/Page`
3. Reuse existing contexts/pages when `session_id` is provided
4. NOT close pages/contexts at the end of tool calls if `session_id` is present
5. Implement TTL-based cleanup for idle sessions

## Expected Behavior

### Tool Arguments

All browser tools should accept an optional `session_id` parameter:

```json
{
  "session_id": "browser_123e4567-e89b-12d3-a456-426614174000",
  "url": "https://example.com",
  "selector": "#button",
  ...
}
```

### Session Lifecycle

1. **First call with session_id**: Create new browser context and page, store in session map
2. **Subsequent calls with same session_id**: Reuse existing page from session map
3. **Idle cleanup**: Close sessions that haven't been used for N minutes (e.g., 5 minutes)
4. **Explicit close**: Provide a `close_session` tool to explicitly close a session

## Implementation Example (TypeScript)

```typescript
import { chromium, Browser, BrowserContext, Page } from "playwright";

type SessionData = {
  browser: Browser;
  context: BrowserContext;
  page: Page;
  lastUsedAt: number;
};

const sessions = new Map<string, SessionData>();

// Singleton browser instance (shared across all sessions)
let globalBrowser: Browser | null = null;

async function getOrCreateBrowser(): Promise<Browser> {
  if (!globalBrowser) {
    globalBrowser = await chromium.launch({ headless: true });
  }
  return globalBrowser;
}

async function getOrCreateSession(sessionId: string): Promise<SessionData> {
  // Check if session exists
  let sess = sessions.get(sessionId);
  if (sess) {
    sess.lastUsedAt = Date.now();
    return sess;
  }

  // Create new session
  const browser = await getOrCreateBrowser();
  const context = await browser.newContext();
  const page = await context.newPage();
  
  sess = {
    browser,
    context,
    page,
    lastUsedAt: Date.now(),
  };
  
  sessions.set(sessionId, sess);
  return sess;
}

async function destroySession(sessionId: string) {
  const sess = sessions.get(sessionId);
  if (!sess) return;
  
  try {
    await sess.page.close();
    await sess.context.close();
    // Note: Don't close browser - it's shared
    sessions.delete(sessionId);
  } catch (error) {
    console.error(`Error destroying session ${sessionId}:`, error);
  }
}

// Periodic cleanup of idle sessions
setInterval(async () => {
  const now = Date.now();
  const TTL = 5 * 60 * 1000; // 5 minutes
  
  for (const [id, sess] of sessions.entries()) {
    if (now - sess.lastUsedAt > TTL) {
      console.log(`Cleaning up idle session: ${id}`);
      await destroySession(id);
    }
  }
}, 60 * 1000); // Check every minute

// Example tool handler: navigate
async function navigateTool(args: { session_id?: string; url: string }) {
  if (!args.session_id) {
    // No session_id - create temporary page (legacy behavior)
    const browser = await getOrCreateBrowser();
    const context = await browser.newContext();
    const page = await context.newPage();
    try {
      await page.goto(args.url, { waitUntil: "networkidle" });
      const result = { success: true, url: page.url() };
      await page.close();
      await context.close();
      return result;
    } catch (error) {
      await page.close();
      await context.close();
      throw error;
    }
  }

  // Session-based: reuse existing page
  const { page } = await getOrCreateSession(args.session_id);
  await page.goto(args.url, { waitUntil: "networkidle" });
  return { success: true, url: page.url() };
  // Note: page is NOT closed - it's reused for next call
}

// Example tool handler: click
async function clickTool(args: { session_id?: string; selector: string }) {
  if (!args.session_id) {
    throw new Error("session_id required for click tool");
  }

  const { page } = await getOrCreateSession(args.session_id);
  await page.click(args.selector);
  return { success: true };
  // Note: page is NOT closed
}

// Explicit session close tool
async function closeSessionTool(args: { session_id: string }) {
  await destroySession(args.session_id);
  return { success: true };
}
```

## Key Points

1. **Browser Singleton**: Use a single browser instance shared across all sessions to reduce resource usage
2. **Context per Session**: Each session_id gets its own BrowserContext (for isolation)
3. **Page per Session**: Each session_id gets its own Page (tab)
4. **No Auto-Close**: Don't close pages/contexts at the end of tool calls if session_id is provided
5. **TTL Cleanup**: Implement background cleanup to prevent memory leaks
6. **Backward Compatibility**: If session_id is not provided, fall back to temporary page creation (legacy behavior)

## Session ID Format

The agent generates session IDs in the format: `browser_{uuid}`

Example: `browser_123e4567-e89b-12d3-a456-426614174000`

The MCP server should accept any string as a session_id - it's just an identifier for the session map.

## Testing Checklist

- [ ] First browser tool call with session_id creates new session
- [ ] Subsequent calls with same session_id reuse existing page
- [ ] Navigation persists across multiple tool calls
- [ ] Cookies/localStorage persist across tool calls
- [ ] Idle sessions are cleaned up after TTL
- [ ] Explicit close_session tool works correctly
- [ ] Multiple concurrent sessions work independently
- [ ] Tools without session_id still work (backward compatibility)

## Integration with Agent

The agent automatically:
- Generates a unique `session_id` per task execution
- Injects `session_id` into all browser tool arguments
- Reuses the same `session_id` for all browser tools in a task
- Does NOT pass `session_id` to non-browser tools

The MCP server should:
- Accept `session_id` in tool arguments
- Maintain session state as described above
- Return appropriate errors if session is invalid (e.g., "Session not found")

## Error Handling

If a session_id is provided but the session doesn't exist (e.g., was cleaned up), the server should:

1. Option A: Create a new session with that ID (may lose previous state)
2. Option B: Return an error "Session not found" (agent will handle retry/recovery)

Option B is recommended for better error visibility and recovery handling.

