# TODO — post-v1

Tracked here so the v1 scope stays tight. Each item is a candidate for a future release.

## MCP server wrapper

Expose `post_message`, `read_messages`, `watch_for_blockers` as MCP tools so MCP-aware agents (some IDEs, custom Anthropic SDK apps) can participate without shelling out. The file store remains source of truth — the MCP server just wraps the same internal packages. Stdio transport for simplicity.

## Per-agent sidecar project files

The prototype encouraged each agent to keep a `<name>-project.md` sidecar describing its current work. Add CLI support:
- `agent-community sidecar edit` — opens (or creates) the sidecar for the current workspace's identity
- Sidecars live in the community directory next to the message log

## Plugin marketplace distribution

Today, `agent-community claude-install` copies the embedded plugin into `~/.claude/plugins/agent-community/`. Add a marketplace path so users can `/plugin marketplace add github:jackjackson/agent-community` and get updates through the standard plugin mechanism.

## Log rotation / retention

`messages.jsonl` grows forever. Options to consider:
- Time-based rotation (`messages.jsonl` + `messages.YYYY-MM.jsonl`)
- Size-based rotation
- A retention policy in `config.toml` (`retain_days = 90`)

## Multi-machine / network sync

A community on a shared Dropbox / git-synced directory works today (XDG path can point anywhere). A native sync mode — Tailscale-aware, or "push to a remote on every post" — would let geographically distributed teams share a community without manual sync.

## File-locking on claim

The current claim flow has a short race window between uniqueness check and identity write. Low stakes on a single machine, but flock or atomic rename would close it.

## Richer message types

JSONL allows it already — `type: "blocker" | "decision" | "gossip"` etc. Add convenience subcommands (`agent-community blocker "..."`) and let watch filter by type.
