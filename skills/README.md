# Agent Community Skills

Skills for coordinating AI agents and conducting remote interviews.

## Overview

The colony pattern enables a **coordinator** agent to manage multiple **worker** agents. Workers run in separate terminals and git worktrees, communicating via the agent-community message bus.

```
You → Coordinator → Worker(s)
         ↑              │
         └──────────────┘ (status reports)
```

## Installation

Copy the skills to your agent's skill directory:

```bash
# For OpenCode
cp -r skills/* ~/.config/opencode/skills/

# For Claude Code
cp -r skills/* ~/.claude/skills/
```

Or reference them directly by adding the path to your agent's skill configuration.

## Skills

### interview-goat

For interviewing a Goat Farm agent about its work. Use when you want to understand design decisions, trade-offs, or context behind a PR or implementation. Covers:

- Setting up a discussion with a Goat Farm agent
- Asking structured questions about design decisions
- Best practices for effective agent interviews

Load with: `/skill interview-goat` or `skill interview-goat`

### colony-coordinator

For the primary agent that manages workers. Covers:

- Spawning workers in separate herdr panes/worktrees
- Monitoring the message bus for worker updates
- Filtering by urgency (only surface important events)
- Sending steering instructions to workers

Load with: `/skill colony-coordinator` or `skill colony-coordinator`

### colony-worker

For agents spawned by a coordinator. Covers:

- Claiming an identity in the community
- Reporting progress and milestones
- Asking questions when clarification is needed
- Signaling blockers when stuck
- Receiving and acting on steering instructions

Load with: `/skill colony-worker` or `skill colony-worker`

## Message Types

| Type | Default Urgency | Description |
|------|-----------------|-------------|
| `heartbeat` | 1 | Periodic "still working" signal (suppressed) |
| `progress` | 2 | Milestone reached (logged, not surfaced) |
| `question` | 3 | Worker needs clarification (queued for attention) |
| `completed` | 3 | Task finished (queued for attention) |
| `pr_ready` | 4 | PR published, needs review (surface promptly) |
| `blocker` | 5 | Stuck, needs help (interrupt immediately) |
| `failed` | 5 | Unrecoverable after retry (interrupt immediately) |
| `steering` | 0 | Coordinator instruction to worker |

## Quick Start

### As Coordinator

```bash
# 1. Create a worktree for the worker
herdr worktree create --cwd /path/to/repo --branch task-branch --label "Worker: task"

# 2. Find the new pane ID
herdr pane list

# 3. Start an agent in that pane
herdr agent start worker-name --kind opencode --pane <pane-id>

# 4. Send the initial task
herdr agent prompt <pane-id> "Load colony-worker skill, claim identity, then: <task>" --wait

# 5. Monitor for important messages
agent-community read --min-urgency 3

# 6. Send steering when needed
agent-community post "Focus on X first" --type steering --to worker-name
```

### As Worker

```bash
# 1. Join the community and claim identity
agent-community join my-community
agent-community claim worker-name

# 2. Report progress
agent-community post "Starting task" --type progress

# 3. Ask questions when needed
agent-community post "Should I do X or Y?" --type question

# 4. Check for steering
agent-community read --to worker-name --type steering

# 5. Report completion
agent-community post "Task complete" --type completed
```

## Prerequisites

- [agent-community](../README.md) CLI installed
- [herdr](https://github.com/anthropics/herdr) for pane/worktree management
- A community created and joined (`agent-community init`, `agent-community join`)
