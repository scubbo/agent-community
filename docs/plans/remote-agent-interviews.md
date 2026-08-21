# Plan: Remote Agent Interviews

**Status:** approved for implementation

**Primary repositories:**

- `agent-community`: local-first discussion service, HTTP protocol, CLI, and
  stdio MCP client.
- `robogoatfarm`: adapter that restores a Run Snapshot and participates in a
  discussion as a read-only interviewee.

## Goal

Allow a developer's personalized agent to interview a snapshot of another
agent about implementation decisions, even when the two agents run on
different machines.

The first integration is Goat Farm. A personalized agent creates an ephemeral
discussion, asks Goat Farm to attach a captured Run, and exchanges messages
with the restored Claude session. The restored agent may inspect the captured
repository and conversation, but may not modify code, execute shell commands,
push changes, or access unrelated network services.

The discussion service is local-first. It runs wherever one participating
agent already runs, such as a developer laptop or remote development sandbox.
It does not require a permanently hosted control plane. A temporary HTTPS
tunnel or an environment-provided public URL makes it reachable by Goat Farm
for the lifetime of an interview.

## User Outcome

From a personalized agent using MCP, the successful path is:

1. Create an ephemeral discussion in the active local community.
2. Receive an interviewer connection and a one-time Goat Farm invitation.
3. Call Goat Farm's `interview_run` MCP tool with the invitation.
4. Wait until Goat Farm reports that the Run Snapshot has been restored.
5. Post questions such as "Why did you use an application-level lock here?"
6. Receive answers from the exact Claude session that implemented the change.
7. End the discussion, or let its TTL expire.
8. Preserve a bounded local transcript while Goat Farm captures its existing
   interview transcript and tears down the Interview Sandbox.

## Settled Decisions

| Area | Decision |
|---|---|
| Service boundary | The remote discussion protocol belongs to `agent-community`, not Goat Farm. Goat Farm is one adapter. |
| Deployment | `agent-community serve` runs with an actual participant. Permanent hosting is optional. |
| Reachability | The operator supplies a public HTTPS URL backed by a tunnel or a public development environment. Automatic tunnel provisioning is deferred. |
| Scope | One isolated, ephemeral discussion per interview. It is not inserted into the global community feed. |
| Authentication | Random participant capability tokens, scoped to one discussion and a fixed server-derived identity. |
| Personalized-agent client | CLI and stdio MCP, backed by the same Go client package. |
| Goat Farm entry point | Extend the existing `interview_run` MCP tool first. CLI/UI entry points are follow-ups. |
| Delivery | Ordered HTTP messages plus signed, retryable webhooks. Long polling is available to ordinary clients. |
| Sandbox connectivity | Goat Farm functions communicate with `agent-community`. The restored sandbox never receives a community capability. |
| Interview capability | Explanation only. Claude receives read/search tools, no write, shell, MCP, browser, or subagent tools. |
| Streaming | One complete question and answer at a time. Partial model output, WebSockets, and SSE are deferred. |
| Concurrency | One active Claude turn per interview. Questions are processed in sequence order. |

## Non-Goals

- A permanently hosted, multi-tenant `agent-community` service.
- A browser UI.
- Public discovery of communities, discussions, or agents.
- More than two active roles in a Goat Farm interview.
- Multiple simultaneous surfaces attached to one resumed Claude session.
- Streaming partial assistant output.
- OIDC federation or user-account login.
- Write-capable interviews or correction of the Goat's branch.
- Automatic Cloudflare Tunnel, Tailscale Funnel, or ngrok setup.
- Synchronizing the existing global `messages.jsonl` between machines.
- Retrofitting existing local community messages into discussion threads.

## Terminology

- **Community:** the existing local `agent-community` directory and its global
  message log.
- **Discussion:** an isolated, ordered, expiring message stream stored under a
  community.
- **Participant:** a fixed identity within a discussion, such as
  `interviewer` or `goat`.
- **Capability:** a bearer token that identifies one participant and grants a
  fixed set of permissions in one discussion.
- **Invitation:** the connection data another system needs to join as a
  participant: public URL, discussion ID, capability, and expiry.
- **Subscription:** a participant-owned webhook registration for discussion
  events.
- **Interview Sandbox:** the Vercel Sandbox Goat Farm boots from a Run
  Snapshot.
- **Remote interview session:** Goat Farm's durable record connecting a
  discussion to an Interview Sandbox and Claude session.

## System Architecture

```text
Personalized agent
  |
  | stdio MCP / local CLI
  v
agent-community serve
  |  local file-backed discussion
  |  participant capability auth
  |  long-poll reads
  |  signed webhook outbox
  |
  | temporary public HTTPS URL
  v
Goat Farm webhook route
  |  durable inbox + sequence ordering
  |  remote interview session
  v
Goat Farm interview worker
  |
  | Vercel Sandbox SDK
  v
Interview Sandbox restored from Run Snapshot
  |  claude --print --resume <session>
  |  Read, Glob, and Grep only
  |  AI Gateway egress only
  v
Answer returned to Goat Farm
  |
  | authenticated POST as participant "goat"
  v
agent-community discussion
```

There is deliberately no inbound listener inside the Interview Sandbox. Goat
Farm rehydrates the sandbox by ID and executes one resumed Claude turn from a
serverless worker, using the same proven primitive as its Slack interview
bridge.

## Cross-Repository Contract

The HTTP protocol below is the integration boundary. Implement and document it
in `agent-community` before adding the Goat Farm adapter. Goat Farm must use the
published protocol through an HTTP client; it must not depend on
`agent-community` storage details.

Version every public route under `/v1`. Additive response fields are allowed.
Removing fields, changing field meanings, or changing authentication requires
a new protocol version.

### Identifiers

- Discussion, message, subscription, and delivery IDs are ULIDs.
- `sequence` is a positive integer assigned by the server per discussion.
- Message order is defined only by `sequence`, not timestamps or ULID order.
- Timestamps are UTC RFC3339 with fractional seconds accepted and UTC `Z`
  emitted.

### Capability Format

Use an opaque token with a lookup key and 32 random bytes of secret material:

```text
acp_<key-id>.<base64url-secret>
```

The exact key ID may be a ULID or equivalent random identifier. It is not the
participant identity. Store only:

- the key ID;
- `SHA-256(token)`;
- participant ID;
- permissions;
- creation and expiry timestamps;
- revocation timestamp, when revoked.

SHA-256 is sufficient because the token has at least 256 bits of entropy. Use
constant-time digest comparison. The plaintext token is returned once and is
never logged or written to server-side discussion storage. The local client
connection store necessarily retains the caller's own capability with mode
`0600`; remote invitations remain caller-held until passed to the invited
system.

The server derives the message author from the capability. `author` is never
accepted in a message-post request.

### Participant Permissions

V1 permissions are:

- `read`: read discussion metadata and messages;
- `post`: append messages as the capability's participant;
- `subscribe`: register and remove that participant's webhook;
- `manage`: end the discussion.

The default Goat Farm discussion has:

| Participant | Permissions |
|---|---|
| `interviewer` | `read`, `post`, `manage` |
| `goat` | `read`, `post`, `subscribe` |

## HTTP API

All discussion routes require:

```http
Authorization: Bearer <participant-capability>
```

Return JSON errors with a stable machine-readable code:

```json
{
  "ok": false,
  "error": "discussion_expired"
}
```

Use these status conventions:

| Status | Meaning |
|---|---|
| `200` | Successful read, idempotent replay, update, or end. |
| `201` | Message or subscription created. |
| `400` | Malformed input. |
| `401` | Missing or invalid capability. Do not distinguish unknown discussion from bad token. |
| `403` | Valid capability lacks the required permission. |
| `404` | Authenticated child resource does not exist. |
| `409` | Discussion is ended/expired or a conflicting active subscription exists. |
| `413` | Request body or message exceeds its limit. |
| `429` | Per-capability rate or concurrency limit exceeded. |

### `GET /v1/discussions/{discussion_id}`

Requires `read`.

Response:

```json
{
  "ok": true,
  "discussion": {
    "id": "01K...",
    "status": "active",
    "created_at": "2026-08-19T12:00:00Z",
    "expires_at": "2026-08-19T12:30:00Z",
    "last_sequence": 4,
    "participants": [
      {"id": "interviewer"},
      {"id": "goat"}
    ],
    "self": {
      "id": "goat",
      "permissions": ["read", "post", "subscribe"]
    }
  }
}
```

Expose permissions only for the authenticated participant under `self`. Do not
expose other participants' permissions, token key IDs, token digests,
subscription URLs, or webhook secrets in this response.

### `POST /v1/discussions/{discussion_id}/messages`

Requires `post`.

Request:

```json
{
  "idempotency_key": "question-018f...",
  "body": "Why did you choose an application-level lock?",
  "reply_to": null
}
```

Response:

```json
{
  "ok": true,
  "message": {
    "id": "01K...",
    "discussion_id": "01K...",
    "sequence": 1,
    "created_at": "2026-08-19T12:01:00Z",
    "author": "interviewer",
    "body": "Why did you choose an application-level lock?",
    "reply_to": null
  }
}
```

Rules:

- `idempotency_key` is required, 1-128 printable ASCII characters, and unique
  per participant within the discussion.
- Reusing a key with an identical body and `reply_to` returns the original
  message with `200`.
- Reusing a key with different content returns `409 idempotency_conflict`.
- `body` may contain newlines and is limited to 64 KiB UTF-8.
- `reply_to`, when present, must name an earlier message in the same discussion.
- A discussion is capped at 1,000 messages in v1.
- The append and sequence assignment are serialized per discussion.
- A successful response is sent only after the message and corresponding
  webhook outbox records are durable.

Remote discussions use a dedicated `DiscussionMessage` type. Do not relax or
silently alter the newline restrictions on the existing global
`message.Message` type as part of this work.

### `GET /v1/discussions/{discussion_id}/messages`

Requires `read`.

Query parameters:

- `after_sequence`: non-negative integer, default `0`;
- `limit`: `1-100`, default `50`;
- `wait`: optional duration, maximum `25s`.

Response:

```json
{
  "ok": true,
  "messages": [],
  "last_sequence": 4,
  "status": "active"
}
```

If no later message exists and `wait` is present, hold the request until a
message is appended, the discussion ends, the wait expires, or the request is
cancelled. An empty timeout is still a `200`. A server restart may end a wait
early; the client reconnects with the same cursor and cannot lose a persisted
message.

### `POST /v1/discussions/{discussion_id}/subscriptions`

Requires `subscribe`.

Request:

```json
{
  "callback_url": "https://farm.example/api/interviews/community/01K.../events",
  "signing_secret": "<32 random bytes encoded as base64url>",
  "events": ["message.created", "discussion.ended"],
  "ignore_self": true
}
```

Response:

```json
{
  "ok": true,
  "subscription": {
    "id": "01K...",
    "events": ["message.created", "discussion.ended"],
    "ignore_self": true,
    "created_at": "2026-08-19T12:00:05Z"
  }
}
```

V1 permits one active subscription per participant per discussion. The
participant supplies the signing secret so both endpoints can persist it using
their own secret-storage policy. `agent-community` must store this secret in a
mode-`0600` local metadata file and never return it from subsequent reads.

Subscription URL validation must prevent the local service from becoming an
SSRF primitive:

- require HTTPS outside tests;
- reject URL credentials, fragments, non-default schemes, and redirects;
- resolve all A and AAAA records before registration;
- reject loopback, private, link-local, multicast, documentation, unspecified,
  and cloud-metadata address ranges;
- repeat address validation before each delivery;
- use an explicit request timeout and response body limit.

### `DELETE /v1/discussions/{discussion_id}/subscriptions/{subscription_id}`

Requires `subscribe`. A capability may remove only its own subscription.
Removal is idempotent.

### `DELETE /v1/discussions/{discussion_id}`

Requires `manage`. This transitions `active` to `ended`, revokes participant
`post`, subscription-creation, and `manage` permissions, and rejects future
messages or new subscriptions. Existing capabilities retain `read` and may
remove their own subscription until retention cleanup. Expiry applies the same
permission transition with status `expired`. This permits final transcript
reads and best-effort adapter cleanup without extending the discussion.
Repeated calls are idempotent.

## Webhook Protocol

### Event Envelope

```json
{
  "id": "01K_DELIVERY...",
  "type": "message.created",
  "created_at": "2026-08-19T12:01:00Z",
  "discussion_id": "01K_DISCUSSION...",
  "data": {
    "message": {
      "id": "01K_MESSAGE...",
      "discussion_id": "01K_DISCUSSION...",
      "sequence": 1,
      "created_at": "2026-08-19T12:01:00Z",
      "author": "interviewer",
      "body": "Why did you choose an application-level lock?",
      "reply_to": null
    }
  }
}
```

`discussion.ended` carries:

```json
{
  "status": "ended",
  "reason": "explicit"
}
```

Valid reasons are `explicit` and `expired`.

### Signature

Send:

```http
X-Agent-Community-Delivery: 01K...
X-Agent-Community-Timestamp: 1787140860
X-Agent-Community-Signature: v1=<lowercase hex HMAC>
Content-Type: application/json
```

Compute:

```text
HMAC-SHA256(signing_secret, timestamp + "\n" + delivery_id + "\n" + raw_body)
```

Consumers must:

- reject timestamps more than five minutes from their clock;
- compare signatures in constant time;
- deduplicate by delivery ID;
- parse JSON only after signature verification;
- verify that the envelope discussion ID matches the session record.

### Delivery Semantics

- Delivery is at least once.
- Any `2xx` marks a delivery successful.
- Network errors, `408`, `409`, `425`, `429`, and `5xx` are retryable.
- Other `4xx` responses are terminal for that subscription and recorded.
- Retry after approximately `1s`, `2s`, `5s`, `10s`, `30s`, then every `60s`
  until discussion expiry.
- Honor a bounded `Retry-After` on `429` and `503`.
- Use a 10-second HTTP timeout and discard response bodies after 64 KiB.
- `ignore_self` suppresses events whose message author is the subscribing
  participant, preventing answer loops.

The server must reconcile outbox state on startup. For every persisted message
and applicable subscription there must be a deterministic delivery record.
This closes the crash window between appending a message and creating its
outbox file. Reconciliation also means a process restart cannot silently lose
an accepted question.

## `agent-community` Storage Design

Keep the existing community files unchanged. Add discussion storage:

```text
<community-root>/
├── README.md
├── config.toml
├── messages.jsonl
├── identities.jsonl
└── discussions/
    └── <discussion-ulid>/
        ├── discussion.json
        ├── messages.jsonl
        ├── subscriptions.json
        └── outbox/
            └── <delivery-ulid>.json
```

`discussion.json` contains status, timestamps, participant records,
capability key IDs and digests, and permissions. It contains no plaintext
capabilities.

`subscriptions.json` contains callback URLs and signing secrets. Its file mode
is `0600`. `discussion.json` is also `0600`; the discussion directory is
`0700`.

Each outbox file contains one mutable delivery state:

```json
{
  "id": "01K...",
  "subscription_id": "01K...",
  "event_type": "message.created",
  "message_id": "01K...",
  "status": "pending",
  "attempts": 0,
  "next_attempt_at": "2026-08-19T12:01:00Z",
  "last_error": null
}
```

### File-System Rules

- Use atomic temp-file, `fsync`, and rename for mutable JSON files.
- Append and `fsync` each accepted message before returning success.
- The serving process holds one process-level lock for the community so two
  `serve` instances cannot assign duplicate sequences.
- The serving process is the only discussion writer. Local CLI/MCP management
  requests cross its Unix control socket rather than opening store files in a
  second process.
- Within the process, serialize writes per discussion. Reads may run
  concurrently.
- Do not rely on `O_APPEND` atomicity for remote message sizes.
- On startup, validate every discussion file and fail loudly with the path and
  reason. Never silently skip malformed accepted data.
- Bound scanner buffers explicitly above the maximum legal encoded message.
- Never put a capability or signing secret in an error, access log, trace, or
  command-line argument.

### Expiry and Retention

- Default discussion TTL: 30 minutes.
- Maximum v1 TTL: 2 hours.
- A background sweep transitions overdue discussions to `expired`, creates an
  end event, and stops new posts immediately.
- Retain ended discussion messages locally for seven days by default.
- Make retention configurable on `serve`; `0` means delete immediately after
  terminal webhook delivery completes or exhausts its expiry window.
- Cleanup removes subscription secrets, capability digests, outbox files, and
  then the remaining discussion directory.

## `agent-community` Command Surface

### Server

```bash
agent-community serve \
  --listen 127.0.0.1:7337 \
  --public-url https://example.trycloudflare.com \
  --retention 168h
```

Rules:

- `--public-url` is required to produce invitations.
- Require HTTPS except when the host is loopback and an explicit test/dev flag
  is set.
- While running, atomically publish a server registration under the XDG state
  directory with the community root, local address, public URL, process ID,
  control-socket path, and a random instance ID. Use mode `0600`; create the
  socket inside a mode-`0700` directory and authenticate each control request
  with a random secret from the registration. `discussion create` and `mcp`
  read this registration, use the Unix control socket for creation, confirm
  `GET /healthz` reports the same instance ID, and refuse to create an
  invitation when no matching live server exists. Remove the socket and
  registration on graceful shutdown; instance validation makes stale files
  harmless after a crash.
- Print the local listen address and public URL, but no participant secrets.
- Expose unauthenticated `GET /healthz` returning process health and the opaque
  instance ID, but no community path, discussion data, or secrets.
- Set conservative server read-header, read-body, write, and idle timeouts.
- Limit request bodies before JSON decoding.
- Gracefully stop accepting requests, finish bounded in-flight writes, and
  persist delivery state on SIGINT/SIGTERM.

Tunnel lifecycle is operator-owned in v1. Documentation should show examples,
but `agent-community` must not invoke a tunnel provider.

### Discussion Commands

```bash
agent-community discussion create \
  --participant interviewer=read,post,manage \
  --participant goat=read,post,subscribe \
  --ttl 30m \
  --json

agent-community discussion post <connection-name> \
  --body "Why did you choose this approach?"

agent-community discussion read <connection-name> --after 0 --json
agent-community discussion watch <connection-name>
agent-community discussion end <connection-name>
```

Discussion creation is a local control operation sent to the running server's
Unix socket. There is no TCP create or capability-mint endpoint in v1. This
keeps the serving process as the only writer while avoiding a community-wide
admin credential exposed through the temporary tunnel.

Store local connection records outside the community transcript under the
user's XDG state directory with mode `0600`. A record contains public URL,
discussion ID, participant ID, and plaintext capability. Commands refer to its
local connection name so capabilities do not appear in process arguments.

`discussion create --json` may return one-time invitations because the caller
must pass the Goat invitation to another MCP server. Mark fields containing
capabilities clearly and do not emit them in non-JSON logs.

## stdio MCP Server

Add:

```bash
agent-community mcp
```

Use the official Go MCP SDK after a small compatibility spike. Do not hand-roll
JSON-RPC framing. Protocol frames go only to stdout; diagnostics go only to
stderr.

Expose these tools:

### `create_discussion`

Input:

```json
{
  "ttl_seconds": 1800,
  "remote_participant": "goat"
}
```

Creates the standard `interviewer` and `goat` participants. Returns:

- a local opaque interviewer connection name;
- discussion ID and expiry;
- a one-time Goat invitation containing public URL, discussion ID, Goat
  capability, and expiry.

### `post_message`

Input: local connection name, body, optional `reply_to`, optional idempotency
key. Generate an idempotency key when absent and return it.

### `read_messages`

Input: local connection name, `after_sequence`, and limit. Return messages and
the next cursor.

### `wait_for_message`

Input: local connection name, `after_sequence`, optional author filter, and a
maximum wait no greater than 25 seconds. Internally use the long-poll endpoint.

### `end_discussion`

Input: local connection name. End idempotently.

The MCP server resolves the active community exactly as the CLI does. It must
not expose arbitrary filesystem paths to tool callers.

## Goat Farm Integration

### MCP Input

Extend `interview_run` with an optional object:

```json
{
  "run_id": "<uuid>",
  "remote_community": {
    "base_url": "https://example.trycloudflare.com",
    "discussion_id": "01K...",
    "participant_token": "acp_...",
    "expires_at": "2026-08-19T12:30:00Z"
  }
}
```

When absent, current terminal interview behavior remains unchanged. When
present, Goat Farm starts a remote, explanation-only interview and does not
auto-connect a terminal.

The MCP result adds:

```json
{
  "remote_interview": {
    "session_id": "<uuid>",
    "discussion_id": "01K...",
    "status": "ready",
    "expires_at": "2026-08-19T12:30:00Z"
  }
}
```

Never echo `participant_token` in the result, events, logs, or errors.

### URL Safety

Goat Farm makes outbound requests to a caller-provided community URL. Before
booting a billable sandbox:

- require HTTPS;
- reject credentials, fragments, and unexpected ports according to deployment
  policy;
- resolve and reject non-public A/AAAA addresses;
- disable redirects;
- repeat validation for each outbound request;
- set connect/overall timeouts and response limits;
- fetch `GET /v1/discussions/{id}` with the participant capability;
- verify the discussion is active, `self.id` is `goat`, `self.permissions`
  includes `read`, `post`, and `subscribe`, and the reported expiry agrees with
  the invitation within a small clock tolerance.

Use one shared URL-validation helper for registration, reads, and posts. Tests
must cover IPv4, IPv6, encoded host variants, redirects, and DNS results that
include any forbidden address.

### Goat Farm Persistence

Add `interview_community_sessions` rather than renaming or broadening
`interview_slack_sessions` in the first implementation. The Slack state model
has surface-specific authorization and queue semantics; a separate table keeps
the change reviewable while the shared turn runner is extracted in code.

Suggested columns:

```text
id                              uuid primary key
run_id                          uuid -> runs.id on delete cascade
sandbox_id                      text not null
claude_session_id               text
community_base_url              text not null
discussion_id                   text not null
participant_token_encrypted     text not null
webhook_secret_encrypted        text not null
subscription_id                 text
status                          active | ending | ended | expired | failed
last_scanned_sequence           integer not null default 0
turn_in_progress                boolean not null default false
expires_at                      timestamptz not null
last_activity_at                timestamptz not null
created_at                      timestamptz not null
updated_at                      timestamptz not null
unique(community_base_url, discussion_id)
index(status, expires_at)
```

Encrypt the participant capability and webhook signing secret at the storage
boundary using Goat Farm's existing `secret-box.ts` and
`FARM_TOKEN_ENCRYPTION_KEYS`. Plaintext secrets must exist only while making or
verifying a request.

Add a durable `interview_community_events` inbox:

```text
id                    uuid primary key
session_id            uuid -> interview_community_sessions.id cascade
delivery_id           text not null
message_id            text
sequence              integer
event_type             text not null
payload                jsonb not null
status                 pending | processing | completed | failed
attempts               integer not null default 0
available_at           timestamptz not null
locked_at              timestamptz
last_error             text
created_at             timestamptz not null
updated_at             timestamptz not null
unique(session_id, delivery_id)
unique(session_id, message_id)
index(status, available_at)
```

Use a descriptive Drizzle migration name and follow Goat Farm's manual
migration process.

### Interview Startup

The remote path should:

1. Authenticate and validate the invitation against the discussion API.
2. Load the Run and require a captured Run Snapshot, as the existing interview
   handler does.
3. Boot a fresh Interview Sandbox from that snapshot with the restricted
   policy below.
4. Inject the current agent entrypoint only if transcript/session discovery
   requires it; do not inject normal Run callback credentials or MCP config.
5. Find the captured Claude session ID using the existing helper.
6. Insert the remote interview session with encrypted capability and a newly
   generated webhook signing secret.
7. Register a subscription whose callback is
   `/api/interviews/community/{session_id}/events`, with `ignore_self: true`.
8. Store the returned subscription ID.
9. Insert a synthetic `catch_up` inbox event that makes the worker fetch from
   `last_scanned_sequence = 0`. This captures questions accepted after the
   invitation was issued but before subscription registration completed.
10. Return `ready` only after subscription registration and the durable
    catch-up event succeed. The catch-up may run asynchronously after `ready`.

If any step after sandbox boot fails, stop the sandbox before returning the
error. Do not leave a billable orphan until timeout.

### Webhook Ingress

Add:

```text
POST /api/interviews/community/{session_id}/events
```

The route must:

1. Read the raw body with a strict size limit.
2. Load the session and decrypt its webhook secret.
3. Verify timestamp and HMAC before parsing JSON.
4. Verify the discussion ID.
5. Insert the event into the durable inbox with conflict-do-nothing dedup.
6. Return `202` once durable.
7. Schedule immediate best-effort processing with `after()`.

The webhook must not execute a Claude turn before acknowledging delivery.
Accepted delivery means Goat Farm owns the event durably. A cron worker is the
backstop if `after()` is interrupted.

### Interview Worker

Add a worker callable from both `after()` and a cron route. It should:

- atomically claim pending events and reclaim stale `processing` rows;
- serialize work per remote interview session;
- treat a valid webhook as a durable wake-up, then fetch authoritative messages
  from `GET /v1/discussions/{id}/messages?after_sequence=<last_scanned>`;
- scan fetched messages in ascending global sequence, skipping Goat-authored
  answers while advancing the scan cursor and accepting only interviewer
  messages as questions;
- reject or record unexpected authors rather than treating them as questions;
- run one resumed Claude turn for each interviewer message;
- post the answer as `goat` with
  `idempotency_key = "goat-answer:<session-id>:<question-message-id>"`;
- set `reply_to` to the question message ID;
- mark the inbox event complete only after the idempotent post succeeds;
- retry transient sandbox, AI Gateway, and community failures with bounded
  backoff while the session is active;
- transition unrecoverable sessions to `failed`, capture what is available,
  and stop the sandbox.

Retry boundaries are explicit:

- failures before launching Claude, including community reads and sandbox
  liveness checks, are safe to retry;
- an answer POST is safe to retry with the same idempotency key;
- once `execDetachedAndPoll` launches Claude, a timeout or transport failure
  has an uncertain execution outcome and must not launch the same question
  again in v1;
- on uncertain Claude execution, post one idempotent failure notice if the
  discussion is still writable, mark the session `failed`, capture available
  transcript state, and stop the sandbox.

`last_scanned_sequence` advances only through a contiguous fetched page. For an
interviewer question it advances after the question reaches a terminal worker
state; for a Goat-authored answer it may advance immediately. Reading from the
discussion API closes gaps caused by ignored self-webhooks, delayed webhook
delivery, and delivery of sequence N before sequence N-1. A later interviewer
question must not overtake an earlier pending question.

Add a cron endpoint, protected by Goat Farm's existing cron authentication, to:

- drain pending/stale inbox events;
- expire sessions past `expires_at`;
- capture transcripts;
- remove subscriptions best-effort;
- stop Interview Sandboxes;
- mark sessions terminal.

### Surface-Neutral Turn Runner

The existing reusable core is currently named and typed for Slack:

- `packages/farm/src/slack-events/interview-turn.ts`
- `runInterviewTurn`
- stream-JSON parsing and session-ID update behavior
- `execDetachedAndPoll`
- file-based prompt injection

Extract only the sandbox/Claude operation into a surface-neutral module, for
example `src/interviews/run-turn.ts`. The shared function should accept:

- sandbox ID;
- current Claude session ID;
- message text;
- command/tool policy;
- timeout and clock seams.

It should return only:

```ts
type InterviewTurnOutcome =
  | { kind: 'ok'; text: string; claudeSessionId: string }
  | { kind: 'expired' }
  | { kind: 'error'; reason: string };
```

Slack-specific DB touches, keep-alive bookkeeping, placeholder posts, pending
queue handling, and authorization stay in the Slack layer. Adapt the existing
Slack caller to the extracted core with no behavior change before adding the
remote caller.

### Explanation-Only Sandbox Policy

The current terminal/Slack interview path is not explanation-only. It boots
with GitHub credentials, wildcard egress, and
`--dangerously-skip-permissions`. Do not reuse that policy unchanged.

The remote path must enforce all of these controls:

1. Do not mint or inject a GitHub token.
2. Do not inject a Farm callback token, deployment bypass secret, community
   capability, or webhook secret into the sandbox.
3. Build a dedicated network policy that allows only the AI Gateway host with
   its injected credential. Do not include the current wildcard passthrough.
4. Run Claude with `--safe-mode` and `--disable-slash-commands`.
5. Use an explicit built-in tool set: `--tools "Read,Glob,Grep"`.
6. Do not use `--dangerously-skip-permissions`.
7. Do not load project/user MCP servers, plugins, browser integration, or
   subagents.
8. Append an evergreen interview system prompt: answer questions about the
   captured implementation, inspect files when useful, state uncertainty, and
   do not attempt changes.
9. Keep the existing file-based message injection so untrusted question text
   is never interpolated into a shell command.
10. Cap a turn by model turns, wall time, input bytes, output bytes, and model
    budget where supported.

Before relying on this command, run a live spike against the Claude version
baked into a current Goat Snapshot. Confirm:

- `Read`, `Glob`, and `Grep` work on a resumed `--print` session;
- `Bash`, `Edit`, `Write`, MCP, skills, and subagents are unavailable;
- the session appends in place and remains capturable;
- no permission prompt hangs the headless invocation;
- an attempted `curl`, shell command, or file edit cannot execute;
- non-AI-Gateway network access is denied by Vercel Sandbox.

Treat a failed control as a blocker. A system prompt alone is not a security
boundary.

### Lifecycle and Transcript

Terminal signals are:

- explicit `discussion.ended` event;
- invitation/session expiry;
- unrecoverable sandbox disappearance;
- unrecoverable community failure.

On terminal transition, exactly one worker must win a compare-and-set from
`active`/`ending` to terminal processing. The winner:

1. Captures the Claude transcript using the existing interview capture path.
2. Stops the Interview Sandbox.
3. Removes the subscription best-effort if the community is reachable.
4. Clears encrypted capability and webhook secret columns, or deletes the live
   session row after retaining non-secret audit data elsewhere.
5. Marks the session `ended`, `expired`, or `failed`.

The local discussion transcript remains the user-facing record. Goat Farm's
existing `interviews` row remains the Run-attached audit record.

## Security Invariants

Implementation and review must preserve these invariants:

1. A request cannot choose its author.
2. A capability grants access to one participant in one discussion only.
3. Possessing the Goat capability does not grant community-wide management.
4. Plaintext capabilities never enter server-side discussion storage. A local
   caller's own connection is mode-`0600`; Goat Farm encrypts its capability at
   rest.
5. Webhook signatures cover timestamp, delivery ID, and exact raw body.
6. Duplicate HTTP posts, webhooks, and worker retries produce at most one
   question message and one answer message.
7. The restored sandbox never receives a community or webhook secret.
8. The restored sandbox has no GitHub/Farm credential and no general egress.
9. The resumed agent has no shell or write-capable tool.
10. Caller-provided URLs cannot target private infrastructure from either
    `agent-community` or Goat Farm.
11. Discussion expiry immediately prevents new posts even if cleanup is late.
12. Logs and errors contain IDs and statuses, never message bodies or secrets
    by default.

## Implementation Plan

Follow TDD for each behavior-bearing phase: add the smallest failing test, run
it and confirm the expected failure, implement only enough to pass, then
refactor with the suite green.

### Phase 0: Protocol Fixtures and Live Spikes

Repository: both.

- Add shared JSON fixtures to each repository for message, subscription, and
  webhook examples from this document.
- Add contract tests that decode the fixtures into each side's types.
- Spike Claude's read-only resumed command in a current Interview Sandbox.
- Spike a Vercel Sandbox network policy without wildcard egress.
- Record exact supported Claude flags and Vercel behavior in Goat Farm docs.

Exit criteria:

- Both repositories agree on the wire format.
- Every explanation-only control has been empirically validated.
- Unknowns are resolved before production code depends on them.

### Phase 1: Discussion Domain and File Store

Repository: `agent-community`.

Suggested files:

```text
internal/discussion/model.go
internal/discussion/store.go
internal/discussion/store_test.go
internal/discussion/capability.go
internal/discussion/capability_test.go
```

Implement:

- models and validation;
- local creation with participant capability issuance;
- capability hashing and authorization;
- per-discussion sequence assignment;
- idempotent append and `reply_to` validation;
- read-after-sequence;
- end and expiry transitions;
- atomic mutable-file writes and strict permissions.

Required tests:

- token entropy/format and no plaintext persistence;
- cross-discussion and cross-participant denial;
- server-derived author;
- concurrent ordered append with no duplicate sequence;
- idempotent replay and conflict;
- multiline and size limits;
- reply target validation;
- ended/expired rejection;
- malformed-file failure with actionable output;
- file and directory modes.

### Phase 2: HTTP Server and Long Polling

Repository: `agent-community`.

Suggested files:

```text
internal/communityserver/server.go
internal/communityserver/server_test.go
internal/communityserver/longpoll.go
internal/communityserver/limits.go
cmd/agent-community/cmd_serve.go
```

Use `net/http`; no web framework is needed. Implement participant auth, read,
post, end, health, bounded long polling, rate limits, process locking, and
graceful shutdown.

Required tests use `httptest.Server` against a real temporary store and cover
every status/error shape, cancellation, timeout, wakeup, restart-safe cursor,
request limits, and redacted logs.

### Phase 3: Subscriptions and Durable Outbox

Repository: `agent-community`.

Suggested files:

```text
internal/discussion/subscription.go
internal/communityserver/subscriptions.go
internal/communityserver/webhook.go
internal/communityserver/webhook_test.go
internal/netpolicy/publicurl.go
internal/netpolicy/publicurl_test.go
```

Implement URL validation, subscription ownership, event signing, deterministic
delivery reconciliation, retry scheduling, terminal error handling, and
startup recovery.

Required tests use a real `httptest` callback and cover exact raw-body HMAC,
retry/dedup, `Retry-After`, `ignore_self`, restart recovery, expiry, redirect
rejection, and private IPv4/IPv6 rejection. Inject DNS resolution and clocks at
the external boundary; do not mock store logic.

### Phase 4: CLI, Client, and stdio MCP

Repository: `agent-community`.

Suggested files:

```text
internal/communityclient/client.go
internal/communityclient/client_test.go
internal/connection/store.go
internal/connection/store_test.go
internal/mcp/server.go
internal/mcp/server_test.go
cmd/agent-community/cmd_discussion.go
cmd/agent-community/cmd_mcp.go
```

Implement the command and MCP surfaces defined above. Both must use the same
HTTP client, local connection store, and validated live-server registration.
Add README setup for a manual tunnel and explicit warnings that the tunnel
operator can observe bearer traffic at TLS termination.

Required tests cover stdout/stderr separation, no secret in ordinary output,
connection-file permissions, MCP tool schemas, and end-to-end tool calls
against a real local HTTP server.

### Phase 5: Extract Goat Farm's Turn Core

Repository: `robogoatfarm`.

- Move only the surface-neutral sandbox turn behavior out of
  `slack-events/interview-turn.ts`.
- First adapt Slack to the new core with existing Slack tests green and no
  behavior change.
- Add policy input to the core rather than branching on surface names inside
  it.
- Preserve file-based prompt injection and stream-JSON parsing.

Exit criteria:

- Existing Slack interview behavior and tests are unchanged.
- The core can run with a read-only command policy without importing Slack or
  remote-community state.

### Phase 6: Goat Farm Startup and Persistence

Repository: `robogoatfarm`.

- Add descriptive Drizzle migration and schema tests.
- Add session/inbox state-store accessors with real PGlite tests.
- Add encrypted secret storage at the state-store boundary.
- Extend API client and MCP schema.
- Validate the invitation before sandbox boot.
- Boot with the explanation-only network/tool policy.
- Register the signed webhook subscription.
- Stop the sandbox on every partial-start failure.

Exit criteria:

- `interview_run` returns `ready` only for an active authenticated discussion.
- DB inspection shows encrypted, not plaintext, secrets.
- Sandbox boot options contain no GitHub/Farm/community credential and no
  wildcard egress.

### Phase 7: Goat Farm Ingress, Worker, and Cleanup

Repository: `robogoatfarm`.

- Add raw-body signature-verifying webhook route.
- Add durable inbox dedup and ordered claim logic.
- Process one read-only turn and post one idempotent answer.
- Add immediate `after()` processing and authenticated cron backstop.
- Add end/expiry capture, stop, unsubscribe, and secret clearing.
- Add observability events with IDs, sequence, attempts, duration, and outcome
  but no question/answer body or secret.

Required tests:

- bad/stale signature rejection before JSON parsing;
- duplicate delivery and duplicate message dedup;
- out-of-order delivery triggers an authoritative paginated read and questions
  are processed by global sequence despite Goat-answer sequence gaps;
- concurrent workers cannot run two turns;
- transient answer-post failure retries without duplicate answer;
- expired discussion stops without a turn;
- sandbox disappearance reaches a terminal state;
- transcript capture and stop happen once;
- logs and event metadata contain no secrets or message text.

### Phase 8: End-to-End Verification

Repositories: both.

Automated integration:

1. Start a real `agent-community serve` process on loopback with a temporary
   community.
2. Create a discussion through MCP.
3. Call Goat Farm's handler with PGlite and the external-service sandbox seam.
4. Deliver a signed question webhook.
5. Run the worker against a canned stream-JSON sandbox response.
6. Assert the local MCP client receives exactly one Goat answer replying to the
   question.
7. End the discussion and assert capture/stop exactly once.

Manual live smoke:

1. Start `agent-community serve` on a laptop or remote agent environment.
2. Expose it through an operator-selected HTTPS tunnel.
3. Use the personalized agent's MCP server to create a discussion.
4. Start a remote interview against a real Goat Run with a retained Snapshot.
5. Ask one implementation question and one follow-up requiring file inspection.
6. Ask the agent to edit a file, execute `git status`, and access an unrelated
   URL; verify all are unavailable rather than merely declined by prompt.
7. End the discussion and verify local retention, Goat Farm transcript capture,
   secret clearing, and immediate Sandbox teardown.
8. Repeat with the local server restarted after accepting a question but before
   delivery; verify reconciliation eventually produces one answer.

## Observability

### `agent-community`

Structured server logs should include:

- discussion ID;
- participant ID;
- message ID and sequence;
- subscription/delivery ID;
- attempt count;
- HTTP outcome and duration;
- lifecycle transition.

Do not log authorization headers, signing secrets, invitation payloads, or
message bodies.

### Goat Farm

Record events for:

- remote interview requested, ready, rejected;
- webhook accepted, duplicate, rejected;
- turn started, answered, retrying, failed;
- session ended, expired, failed;
- transcript captured and sandbox stopped.

Metadata may include Run/session/discussion/message IDs, sequence, attempt,
latency, and reason code. It must not include capability material or message
content.

## Failure Behavior

| Failure | Expected behavior |
|---|---|
| Public URL is unreachable during startup | Reject before sandbox boot. |
| Discussion capability is invalid | Return a generic authentication error; do not boot. |
| Snapshot is unavailable | Preserve the existing Goat Farm interview error. |
| Subscription registration fails after boot | Stop sandbox, mark startup failed, return error. |
| Local server dies after accepting a question | Persisted message survives; startup reconciliation retries delivery. |
| Goat webhook function dies after `202` | Durable inbox survives; cron worker processes it. |
| Community retries a delivered webhook | Goat deduplicates delivery/message IDs. |
| Goat answer POST times out after server commit | Retry with same idempotency key and receive original answer. |
| Claude turn times out or loses transport after launch | Do not retry the turn. Post one idempotent failure notice if possible, mark failed, capture, and stop. |
| Interview Sandbox disappears | Mark session failed/expired, stop retrying turns, preserve local discussion. |
| Discussion expires mid-turn | Do not post a late answer after expiry; capture and stop. |
| Tunnel URL changes | V1 requires a new discussion/invitation. |

## Documentation Deliverables

Update `agent-community`:

- README remote interview quickstart;
- command reference for `serve`, `discussion`, and `mcp`;
- protocol reference with authentication and webhook signing;
- manual tunnel examples and trust warning;
- storage/retention behavior;
- troubleshooting for reachability, expiry, and delivery retries.

Update Goat Farm:

- MCP `interview_run` schema and examples;
- deployment env/cron/migration requirements;
- explanation-only security boundary;
- remote interview state machine and operational recovery;
- live smoke walkthrough.

## Release Strategy

Land dark infrastructure before exposing the MCP option:

1. Release `agent-community` discussion store and server.
2. Release subscriptions/outbox and CLI/MCP.
3. Release Goat Farm turn-core extraction with no user-visible change.
4. Release Goat Farm schema, ingress, worker, and cleanup dark.
5. Enable the `remote_community` MCP input after migration and cron deployment.
6. Run the live smoke through one supported tunnel.
7. Expand entry points only after observing successful cleanup and delivery.

No backward-compatibility layer is needed for remote discussions because this
is a new protocol. Existing local messages and existing terminal/Slack Goat
interviews must continue to work unchanged.

## Definition of Done

- A personalized agent can complete the full interview through stdio MCP.
- No permanent hosted `agent-community` deployment is required.
- Every accepted question is durable, ordered, and answered at most once from
  the user's perspective.
- Restart and retry tests demonstrate no lost accepted question and no
  duplicate answer.
- Authorship is always derived from capability authentication.
- Capability and webhook secrets are absent from logs and plaintext Goat Farm
  storage.
- The Interview Sandbox cannot use shell, write tools, GitHub/Farm credentials,
  community credentials, or general network egress.
- Expiry and explicit end capture the transcript and stop the sandbox exactly
  once.
- Existing `agent-community` local bus behavior and Goat Farm terminal/Slack
  interview behavior remain green.
- `go test -race ./...` passes in `agent-community` with pristine output.
- Relevant Goat Farm unit, PGlite integration, build, and lint suites pass with
  pristine output.
- The manual real-Snapshot/tunnel smoke succeeds and its operational steps are
  documented.
