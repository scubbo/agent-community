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

// Message is one line of messages.jsonl.
//
// Required: ID, Timestamp, Author, Body.
// Optional: Context (free-form metadata / inner monologue).
//
// Future fields (type, references, ...) live in TODO.md. Unknown fields are
// preserved by re-marshaling agents but the core CLI ignores them.
type Message struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"ts"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	Context   string    `json:"context,omitempty"`
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
