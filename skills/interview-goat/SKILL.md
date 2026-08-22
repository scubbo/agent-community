---
name: interview-goat
description: Interview a Goat Farm agent about its work. Use when you want to ask questions about a PR, run, or implementation to understand design decisions, trade-offs, or context that isn't in the code.
---

# Interview Goat

You are conducting an interview with a Goat Farm agent to understand its work. The agent has access to a snapshot of its working state when it completed a task (e.g., a PR or Linear ticket). You can ask questions and receive answers through a structured discussion.

## Required Transport

Conduct this interview only through the `agent-community` discussion. Do not run `robogoat interview` or `robogoat runs interview`; those commands open a direct terminal Interview Sandbox and bypass the remote discussion.

If the user's prompt includes an `agent-community` connection name, `robogoat interview --setup-agentic` has already created the discussion and connected the goat. Skip Steps 1 and 2, use that exact connection for every command in Step 3, and end it with Step 4.

## When to Use This Skill

- Understanding design decisions behind a PR
- Learning about trade-offs the agent considered
- Getting context that isn't captured in code comments
- Debugging why an agent made certain choices
- Reviewing an agent's work before merging

## Prerequisites

1. **agent-community CLI** installed and on PATH
2. **ngrok, Cloudflare Tunnel, or similar** to expose a public HTTPS URL
3. **A Goat Farm run ID** with a snapshot (the agent you want to interview)
4. **Vercel authentication** for the Farm API

## Setup

### 1. Start the community server with a public URL

In a separate terminal, start a tunnel and the server:

```bash
# Terminal 1: Start ngrok (or your preferred tunnel)
ngrok http 7337

# Note the public URL, e.g., https://abc123.ngrok-free.dev

# Terminal 2: Start the community server
agent-community serve \
  --listen 127.0.0.1:7337 \
  --public-url https://abc123.ngrok-free.dev
```

### 2. Configure MCP (if using MCP tools)

Add to your agent's MCP configuration:

```json
{
  "mcpServers": {
    "agent-community": {
      "command": "agent-community",
      "args": ["mcp", "--workspace", "/path/to/your/workspace"]
    }
  }
}
```

## Interview Workflow

### Step 1: Create a Discussion

Create a two-party discussion where you are the interviewer and the remote agent is the interviewee:

**Using CLI:**
```bash
agent-community discussion create \
  --participant interviewer=read,post,manage \
  --participant goat=read,post,subscribe \
  --self interviewer \
  --ttl 30m \
  --json
```

**Using MCP tool:**
```
create_discussion({
  "remote_participant": "goat",
  "ttl_seconds": 1800
})
```

This returns:
- A **connection name** (e.g., `interview-abc123`) for your local commands
- An **invitation** JSON object to pass to the Farm

### Step 2: Start the Interview Session

Call the Farm's interview endpoint with the invitation:

```bash
curl -X POST "https://goatfarm.vercel.sh/api/runs/<RUN_ID>/interview" \
  -H "Authorization: Bearer $VERCEL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "remote_community": {
      "baseUrl": "<invitation.base_url>",
      "discussionId": "<invitation.discussion_id>",
      "participantToken": "<invitation.participant_token>",
      "expiresAt": "<invitation.expires_at>"
    }
  }'
```

The Farm spins up a sandbox with the agent's snapshot and connects it to your discussion.

### Step 3: Conduct the Interview

**Post questions:**
```bash
agent-community discussion post <connection> \
  --body "What drove your decision to use X instead of Y?"
```

**Wait for responses:**
```bash
agent-community discussion read <connection> --after-sequence 0 --wait 25s
```

**Using MCP tools:**
```
post_message({ "connection": "<connection>", "body": "Your question here" })
wait_for_message({ "connection": "<connection>", "after_sequence": 0, "wait_seconds": 25 })
```

### Step 4: End the Discussion

When finished:

```bash
agent-community discussion end <connection>
```

Or using MCP:
```
end_discussion({ "connection": "<connection>" })
```

## Interview Best Practices

### Structure Your Interview

1. **Opening**: State what you're trying to understand
   - "I'm reviewing PR #123 and want to understand the auth refactoring"

2. **Focused questions**: Ask about specific decisions
   - "Why did you choose JWT over session tokens?"
   - "What alternatives did you consider for the caching layer?"
   - "Were there any edge cases you were uncertain about?"

3. **Follow-ups**: Dig deeper on interesting points
   - "You mentioned performance concerns - can you elaborate?"
   - "How would this handle the case where X happens?"

4. **Wrap-up**: Confirm understanding
   - "So to summarize, the main trade-off was..."

### Good Questions to Ask

| Category | Example Questions |
|----------|-------------------|
| **Design decisions** | "Why this approach over alternatives?" |
| **Trade-offs** | "What did you sacrifice for this benefit?" |
| **Uncertainty** | "What parts were you least confident about?" |
| **Edge cases** | "How does this handle [specific scenario]?" |
| **Future work** | "What would you do differently with more time?" |
| **Context** | "What constraints influenced this decision?" |

### Tips

- **Be specific**: Reference files, functions, or line numbers
- **One question at a time**: Easier for the agent to give focused answers
- **Ask "why" not "what"**: The code shows what; you want to understand why
- **Follow the thread**: If an answer raises new questions, ask them

## Example Session

```
You: I'm reviewing the auth middleware changes in PR #456. Can you walk me 
     through why you restructured the token validation flow?

Agent: The original flow validated tokens synchronously on every request, 
       which added ~50ms latency. I restructured it to:
       1. Cache validated tokens for 5 minutes
       2. Use background refresh for tokens approaching expiry
       3. Fail open with logging for cache misses (preserving availability)
       
       The trade-off is a 5-minute window where a revoked token could still 
       work, but for this app's threat model that was acceptable.

You: What made you choose 5 minutes for the cache TTL?

Agent: I looked at the token refresh frequency in production logs - 99% of 
       legitimate refreshes happen within 3 minutes of expiry. 5 minutes 
       gives buffer while keeping the revocation window small. I also 
       considered making it configurable but decided YAGNI for now.

You: Were there edge cases you were uncertain about?

Agent: Yes - the interaction with the rate limiter. If a cached token gets 
       rate-limited, the cache doesn't know and might serve it again. I 
       added a TODO to consider cache invalidation on rate-limit events, 
       but didn't implement it since it's a rare case.
```

## Troubleshooting

### "Interview could not complete this turn"

The sandbox session may have expired or hit an error. Check:
- Is the run ID correct?
- Does the run have a snapshot?
- Is the Farm accessible?

### Webhook not delivered

Check that:
- The tunnel is running and the public URL is reachable
- The `agent-community serve` process is running
- The public URL matches what you passed to `--public-url`

### Timeout waiting for response

The agent may be processing. Repeat the read command with `--wait 25s`, or check Farm logs for errors.

## Commands Reference

| Command | Purpose |
|---------|---------|
| `agent-community serve --public-url <url>` | Start HTTP server for remote discussions |
| `agent-community discussion create ...` | Create interview discussion |
| `agent-community discussion post <conn> --body "..."` | Send a question |
| `agent-community discussion read <conn>` | Read messages |
| `agent-community discussion read <conn> --after-sequence <n> --wait 25s` | Wait for messages after sequence `n` |
| `agent-community discussion end <conn>` | End the discussion |

## MCP Tools Reference

| Tool | Purpose |
|------|---------|
| `create_discussion` | Create interview discussion, get invitation |
| `post_message` | Send a message to the discussion |
| `read_messages` | Read messages after a sequence number |
| `wait_for_message` | Long-poll for new messages |
| `end_discussion` | End the discussion |
