// Package discussionapp coordinates local server registration, participant
// connections, and the remote discussion client for CLI and MCP surfaces.
package discussionapp

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/scubbo/agent-community/internal/communityclient"
	"github.com/scubbo/agent-community/internal/communityserver"
	"github.com/scubbo/agent-community/internal/connection"
	"github.com/scubbo/agent-community/internal/discussion"
)

type Service struct {
	StateRoot     string
	CommunityName string
	Client        *communityclient.Client
}

type CreateInput struct {
	TTL          time.Duration
	Participants []discussion.ParticipantInput
	Self         string
	Name         string
}

type Invitation struct {
	BaseURL          string    `json:"base_url"`
	DiscussionID     string    `json:"discussion_id"`
	ParticipantToken string    `json:"participant_token"`
	ExpiresAt        time.Time `json:"expires_at"`
}

type CreateResult struct {
	Connection  string                 `json:"connection"`
	Discussion  *discussion.Discussion `json:"discussion"`
	Invitations map[string]Invitation  `json:"invitations"`
}

type PostResult struct {
	Message        *discussion.Message `json:"message"`
	IdempotencyKey string              `json:"idempotency_key"`
	Replayed       bool                `json:"replayed"`
}

func (s *Service) Create(ctx context.Context, input CreateInput) (*CreateResult, error) {
	if input.TTL < time.Second || input.TTL > discussion.MaxTTL || len(input.Participants) == 0 || input.Self == "" {
		return nil, fmt.Errorf("%w: invalid discussion creation input", discussion.ErrInvalidInput)
	}
	var selfPermissions []discussion.Permission
	for _, participant := range input.Participants {
		if participant.ID == input.Self {
			selfPermissions = participant.Permissions
			break
		}
	}
	if !containsPermission(selfPermissions, discussion.PermissionManage) {
		return nil, fmt.Errorf("%w: local participant must have manage permission", discussion.ErrInvalidInput)
	}
	registration, err := communityserver.LoadRegistration(s.StateRoot, s.CommunityName)
	if err != nil {
		return nil, fmt.Errorf("load live server for community %q: %w; start `agent-community serve` first", s.CommunityName, err)
	}
	created, err := communityserver.CreateThroughControl(ctx, registration, discussion.CreateInput{TTL: input.TTL, Participants: input.Participants})
	if err != nil {
		return nil, err
	}
	connectionName := input.Name
	if connectionName == "" {
		connectionName, err = connection.NewName(created.Discussion.ID)
		if err != nil {
			return nil, err
		}
	}
	localCapability := created.Capabilities[input.Self]
	localConnection := connection.Connection{
		Name: connectionName, CommunityName: s.CommunityName,
		LocalURL: registration.LocalURL, PublicURL: registration.PublicURL,
		DiscussionID: created.Discussion.ID, ParticipantID: input.Self,
		Capability: localCapability, ExpiresAt: created.Discussion.ExpiresAt,
	}
	if err := connection.Save(s.StateRoot, localConnection); err != nil {
		_, _ = s.client().End(ctx, &localConnection)
		return nil, fmt.Errorf("save local connection: %w", err)
	}
	invitations := make(map[string]Invitation, len(created.Capabilities)-1)
	for participantID, capability := range created.Capabilities {
		if participantID == input.Self {
			continue
		}
		invitations[participantID] = Invitation{
			BaseURL: registration.PublicURL, DiscussionID: created.Discussion.ID,
			ParticipantToken: capability, ExpiresAt: created.Discussion.ExpiresAt,
		}
	}
	return &CreateResult{Connection: connectionName, Discussion: created.Discussion, Invitations: invitations}, nil
}

func (s *Service) Post(ctx context.Context, connectionName string, input communityclient.PostInput) (*PostResult, error) {
	if input.IdempotencyKey == "" {
		var err error
		input.IdempotencyKey, err = newIdempotencyKey()
		if err != nil {
			return nil, err
		}
	}
	target, err := connection.Load(s.StateRoot, s.CommunityName, connectionName)
	if err != nil {
		return nil, err
	}
	message, replayed, err := s.client().Post(ctx, target, input)
	if err != nil {
		return nil, err
	}
	return &PostResult{Message: message, IdempotencyKey: input.IdempotencyKey, Replayed: replayed}, nil
}

func (s *Service) Read(ctx context.Context, connectionName string, opts communityclient.ReadOptions) (*communityclient.ReadResult, error) {
	target, err := connection.Load(s.StateRoot, s.CommunityName, connectionName)
	if err != nil {
		return nil, err
	}
	return s.client().Read(ctx, target, opts)
}

func (s *Service) End(ctx context.Context, connectionName string) (*discussion.Discussion, error) {
	target, err := connection.Load(s.StateRoot, s.CommunityName, connectionName)
	if err != nil {
		return nil, err
	}
	return s.client().End(ctx, target)
}

func (s *Service) client() *communityclient.Client {
	if s.Client != nil {
		return s.Client
	}
	return communityclient.New(nil)
}

func containsPermission(permissions []discussion.Permission, target discussion.Permission) bool {
	for _, permission := range permissions {
		if permission == target {
			return true
		}
	}
	return false
}

func newIdempotencyKey() (string, error) {
	id, err := ulid.New(ulid.Timestamp(time.Now().UTC()), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate idempotency key: %w", err)
	}
	return "agent-community-" + id.String(), nil
}
