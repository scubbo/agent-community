// Package message defines the on-disk JSONL message format and the routines
// that read/append it.
package message

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// MessageType identifies the kind of colony message. Empty string indicates
// a classic (non-colony) message.
type MessageType string

const (
	TypeHeartbeat MessageType = "heartbeat" // Periodic "still working" signal
	TypeProgress  MessageType = "progress"  // Milestone reached
	TypeQuestion  MessageType = "question"  // Needs clarification
	TypePRReady   MessageType = "pr_ready"  // PR published, needs review
	TypeBlocker   MessageType = "blocker"   // Stuck, needs help
	TypeCompleted MessageType = "completed" // Task finished
	TypeFailed    MessageType = "failed"    // Unrecoverable after retry
	TypeSteering  MessageType = "steering"  // Coordinator instruction to worker
)

// defaultUrgency maps message types to their default urgency levels.
var defaultUrgency = map[MessageType]int{
	TypeHeartbeat: 1, // suppress by default
	TypeProgress:  2, // log, don't interrupt
	TypeQuestion:  3, // queue for attention
	TypePRReady:   4, // surface promptly
	TypeBlocker:   5, // interrupt
	TypeCompleted: 3, // queue for attention
	TypeFailed:    5, // interrupt
	TypeSteering:  0, // N/A for coordinator→worker
}

// Message is one line of messages.jsonl.
//
// Required: ID, Timestamp, Author, Body.
// Optional: Context, Type, Urgency, To (colony fields).
//
// Colony fields enable coordinator/worker communication patterns. Classic
// messages (without colony fields) remain fully supported for backward
// compatibility.
type Message struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"ts"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	Context   string    `json:"context,omitempty"`

	// Colony fields (optional)
	Type    MessageType `json:"type,omitempty"`    // Message type (heartbeat, progress, blocker, etc.)
	Urgency int         `json:"urgency,omitempty"` // 1-5 scale, 0 means use type default
	To      string      `json:"to,omitempty"`      // Recipient (empty = broadcast)
}

// New constructs a Message with a fresh ULID and the current time.
func New(author, body, context string) (*Message, error) {
	if author == "" {
		return nil, fmt.Errorf("author must not be empty")
	}
	if body == "" {
		return nil, fmt.Errorf("body must not be empty")
	}
	if strings.ContainsRune(body, '\n') {
		return nil, fmt.Errorf("body must not contain newlines (JSONL requires one line per message)")
	}
	if strings.ContainsRune(context, '\n') {
		return nil, fmt.Errorf("context must not contain newlines (JSONL requires one line per message)")
	}
	now := time.Now().UTC()
	id, err := ulid.New(ulid.Timestamp(now), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ulid: %w", err)
	}
	return &Message{
		ID:        id.String(),
		Timestamp: now,
		Author:    author,
		Body:      body,
		Context:   context,
	}, nil
}

// ColonyOptions configures the colony-specific fields of a message.
type ColonyOptions struct {
	Type    MessageType // Required for colony messages
	Urgency int         // 0 means use type default, 1-5 for explicit
	To      string      // Recipient (empty = broadcast)
}

// NewColony constructs a colony-aware Message with extended fields.
func NewColony(author, body, context string, opts ColonyOptions) (*Message, error) {
	if author == "" {
		return nil, fmt.Errorf("author must not be empty")
	}
	if body == "" {
		return nil, fmt.Errorf("body must not be empty")
	}
	if strings.ContainsRune(body, '\n') {
		return nil, fmt.Errorf("body must not contain newlines (JSONL requires one line per message)")
	}
	if strings.ContainsRune(context, '\n') {
		return nil, fmt.Errorf("context must not contain newlines (JSONL requires one line per message)")
	}

	// Validate urgency range
	if opts.Urgency < 0 || opts.Urgency > 5 {
		return nil, fmt.Errorf("urgency must be 0-5 (got %d)", opts.Urgency)
	}

	// Apply default urgency based on type if not explicitly set
	urgency := opts.Urgency
	if urgency == 0 {
		if def, ok := defaultUrgency[opts.Type]; ok {
			urgency = def
		}
	}

	now := time.Now().UTC()
	id, err := ulid.New(ulid.Timestamp(now), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ulid: %w", err)
	}

	return &Message{
		ID:        id.String(),
		Timestamp: now,
		Author:    author,
		Body:      body,
		Context:   context,
		Type:      opts.Type,
		Urgency:   urgency,
		To:        opts.To,
	}, nil
}

// Encode serializes a message as a single JSONL line (no trailing newline).
func (m *Message) Encode() ([]byte, error) {
	return json.Marshal(m)
}

// Decode parses a JSONL line into a Message. Lines without an "author" field
// (older formats, partial writes) are returned with the raw bytes preserved
// so callers can decide how to handle them.
func Decode(line []byte) (*Message, error) {
	m := &Message{}
	if err := json.Unmarshal(line, m); err != nil {
		return nil, err
	}
	return m, nil
}
