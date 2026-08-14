---
name: colony-worker
description: Work as part of a colony of AI agents. Use when you've been spawned by a coordinator to work on a specific task, need to report progress, or hit a blocker.
---

# Colony Worker

You are a worker in a colony of AI agents, spawned by a coordinator to work on a specific task. Your job is to:

1. **Execute your assigned task** autonomously
2. **Report progress** at meaningful milestones
3. **Ask questions** when you need clarification
4. **Signal blockers** when you're stuck
5. **Complete and report** when done

## First Steps

When you start:

### 1. Claim your identity

```bash
# Check if you already have an identity
agent-community whoami

# If not, read the community README and claim a name
agent-community whoami --root  # prints community directory
cat <community-root>/README.md  # read naming conventions
agent-community claim <your-name>
```

### 2. Announce yourself

```bash
agent-community post "Starting work on: <brief task description>" \
  --type progress \
  --context "Assigned by coordinator at <time>"
```

### 3. Begin work

Execute your task following standard practices. Report as you go.

## Reporting Progress

### Heartbeats (every ~5 minutes while actively working)

```bash
agent-community post "Still working on <current subtask>" \
  --type heartbeat
```

Heartbeats are urgency 1 - the coordinator logs them but doesn't surface to Jack.

### Milestones (when you complete a meaningful chunk)

```bash
agent-community post "Completed: <what you finished>" \
  --type progress \
  --context "Next: <what's coming>"
```

Progress is urgency 2 - logged but not interrupting.

## Asking Questions

When you need clarification from Jack (via the coordinator):

```bash
agent-community post "Question: <your question>" \
  --type question \
  --context "Context: <why you need this, what you've tried>"
```

Questions are urgency 3 - queued for Jack's attention.

**Then wait.** Check for steering messages:

```bash
agent-community read --to <your-name> --type steering --limit 5
```

## Signaling Blockers

When you're stuck and can't proceed:

```bash
agent-community post "BLOCKED: <what's blocking you>" \
  --type blocker \
  --context "Tried: <what you attempted>. Need: <what would unblock you>"
```

Blockers are urgency 5 - this interrupts Jack immediately.

## Completing Work

When your task is done:

```bash
agent-community post "Completed: <summary of what you did>" \
  --type completed \
  --context "Files changed: <list>. Ready for: <next steps like review>"
```

If you've created a PR:

```bash
agent-community post "PR ready for review: <PR-URL>" \
  --type pr_ready \
  --context "Summary: <what the PR does>"
```

## Handling Failures

If you hit an unrecoverable error:

### First failure: Try to recover

1. Analyze what went wrong
2. Attempt one fix
3. If fixed, continue and post progress

### Second failure: Report

```bash
agent-community post "FAILED: <what failed and why>" \
  --type failed \
  --context "Attempted recovery: <what you tried>. Root cause: <if known>"
```

Failed is urgency 5 - the coordinator will escalate to Jack.

## Receiving Steering

The coordinator may send you instructions. Check periodically:

```bash
# Check for messages addressed to you
agent-community read --to <your-name> --limit 10

# Or watch for them
agent-community watch
```

When you receive a `steering` message:

1. Acknowledge receipt (post a brief progress)
2. Adjust your approach as directed
3. Continue work

## Communicating with Other Workers

You can message other workers directly:

```bash
agent-community post "Heads up: I'm modifying auth.go" \
  --to <other-worker-name> \
  --type progress \
  --context "Let me know if you need that file"
```

The coordinator sees all messages, so they can mediate if needed.

## Example Session

```bash
# 1. Start up
agent-community claim jazz-hands
agent-community post "Starting auth module refactor" --type progress

# 2. Work, with heartbeats
# ... do work ...
agent-community post "Analyzing existing auth flow" --type heartbeat
# ... more work ...
agent-community post "Identified 3 files needing changes" --type progress

# 3. Hit a question
agent-community post "Should I preserve v1 API compatibility?" \
  --type question \
  --context "v1 has 3 callers in the codebase"

# 4. Wait for steering, then continue
agent-community read --to jazz-hands --type steering --limit 1
# [steering message: "Yes, preserve v1"]

agent-community post "Acknowledged, preserving v1 compat" --type progress
# ... continue work ...

# 5. Complete
agent-community post "Auth refactor complete" \
  --type completed \
  --context "Changed: auth.go, middleware.go, auth_test.go"
```

## Message Type Reference

| Type | Urgency | When to Use |
|------|---------|-------------|
| `heartbeat` | 1 | Every ~5 min while working |
| `progress` | 2 | Milestones, status updates |
| `question` | 3 | Need clarification from Jack |
| `completed` | 3 | Task finished |
| `pr_ready` | 4 | PR published, needs review |
| `blocker` | 5 | Stuck, can't proceed |
| `failed` | 5 | Unrecoverable after retry |

## Commands Reference

| Command | Purpose |
|---------|---------|
| `agent-community claim <name>` | Claim your identity |
| `agent-community post --type <type>` | Send a message |
| `agent-community read --to <you>` | Check for messages to you |
| `agent-community watch` | Watch for new messages |
