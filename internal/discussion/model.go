// Package discussion stores isolated, expiring conversations between remote
// agents. It is separate from the global community message log because remote
// discussions have capability authentication, sequence cursors, and multiline
// message bodies.
package discussion

import (
	"errors"
	"time"
)

const (
	DiscussionsDir = "discussions"
	DiscussionFile = "discussion.json"
	MessagesFile   = "messages.jsonl"

	MaxBodyBytes   = 64 * 1024
	MaxMessages    = 1000
	MaxReadLimit   = 100
	MaxTTL         = 2 * time.Hour
	maxEncodedLine = MaxBodyBytes + 8*1024
)

var (
	ErrUnauthorized        = errors.New("unauthorized")
	ErrForbidden           = errors.New("forbidden")
	ErrInvalidInput        = errors.New("invalid input")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrDiscussionEnded     = errors.New("discussion ended")
	ErrDiscussionExpired   = errors.New("discussion expired")
	ErrMessageLimit        = errors.New("discussion message limit reached")
)

type Status string

const (
	StatusActive  Status = "active"
	StatusEnded   Status = "ended"
	StatusExpired Status = "expired"
)

type Permission string

const (
	PermissionRead      Permission = "read"
	PermissionPost      Permission = "post"
	PermissionSubscribe Permission = "subscribe"
	PermissionManage    Permission = "manage"
)

type Participant struct {
	ID string `json:"id"`
}

type AuthenticatedParticipant struct {
	ID          string       `json:"id"`
	Permissions []Permission `json:"permissions"`
}

type Discussion struct {
	ID           string        `json:"id"`
	Status       Status        `json:"status"`
	CreatedAt    time.Time     `json:"created_at"`
	ExpiresAt    time.Time     `json:"expires_at"`
	LastSequence int           `json:"last_sequence"`
	Participants []Participant `json:"participants"`
}

type ParticipantInput struct {
	ID          string
	Permissions []Permission
}

type CreateInput struct {
	TTL          time.Duration
	Participants []ParticipantInput
}

type Message struct {
	ID           string    `json:"id"`
	DiscussionID string    `json:"discussion_id"`
	Sequence     int       `json:"sequence"`
	CreatedAt    time.Time `json:"created_at"`
	Author       string    `json:"author"`
	Body         string    `json:"body"`
	ReplyTo      string    `json:"reply_to,omitempty"`
}

type PostInput struct {
	IdempotencyKey string
	Body           string
	ReplyTo        string
}

type ReadOptions struct {
	AfterSequence int
	Limit         int
}
