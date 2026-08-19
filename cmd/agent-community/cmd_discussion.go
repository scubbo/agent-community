package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackjackson/agent-community/internal/community"
	"github.com/jackjackson/agent-community/internal/communityclient"
	"github.com/jackjackson/agent-community/internal/communityserver"
	"github.com/jackjackson/agent-community/internal/connection"
	"github.com/jackjackson/agent-community/internal/discussion"
	"github.com/oklog/ulid/v2"
)

func cmdDiscussion(args []string) error {
	return runDiscussion(context.Background(), args, os.Stdout)
}

func runDiscussion(ctx context.Context, args []string, output io.Writer) error {
	workspace, remaining, err := extractDiscussionWorkspace(args)
	if err != nil {
		return usageErr("%v", err)
	}
	if len(remaining) == 0 {
		return usageErr("discussion requires a command: create, post, read, or end")
	}
	command := remaining[0]
	commandArgs := remaining[1:]

	ws, err := resolveWorkspace(workspace)
	if err != nil {
		return err
	}
	resolved, err := community.Resolve(ws)
	if err != nil {
		if errors.Is(err, community.ErrNoCommunity) {
			return noCommunityErr(err)
		}
		return err
	}
	stateRoot, err := community.StateHome()
	if err != nil {
		return err
	}

	switch command {
	case "create":
		return runDiscussionCreate(ctx, commandArgs, output, stateRoot, resolved)
	case "post":
		return runDiscussionPost(ctx, commandArgs, output, stateRoot, resolved.Name)
	case "read":
		return runDiscussionRead(ctx, commandArgs, output, stateRoot, resolved.Name)
	case "end":
		return runDiscussionEnd(ctx, commandArgs, output, stateRoot, resolved.Name)
	default:
		return usageErr("unknown discussion command %q", command)
	}
}

func runDiscussionCreate(ctx context.Context, args []string, output io.Writer, stateRoot string, resolved *community.Resolved) error {
	fs := flag.NewFlagSet("discussion create", flag.ContinueOnError)
	ttl := fs.Duration("ttl", 30*time.Minute, "Discussion lifetime, up to 2h.")
	self := fs.String("self", "", "Participant identity to store as the local connection.")
	name := fs.String("name", "", "Local connection name. Default: generated from the discussion ID.")
	emitJSON := fs.Bool("json", false, "Emit the one-time participant invitations as JSON. Required.")
	var participants participantFlags
	fs.Var(&participants, "participant", "Participant and permissions: ID=read,post,... Repeat for each participant.")
	fs.SetOutput(io.Discard)
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() != 0 {
		return usageErr("discussion create accepts no positional arguments")
	}
	if !*emitJSON {
		return usageErr("discussion create requires --json because invitations contain one-time participant capabilities")
	}
	if *ttl < time.Second || *ttl > discussion.MaxTTL {
		return validationErr("--ttl must be between 1s and %s", discussion.MaxTTL)
	}
	if len(participants) == 0 {
		return validationErr("at least one --participant is required")
	}
	if *self == "" {
		return validationErr("--self is required")
	}
	selfPermissions, exists := participants[*self]
	if !exists {
		return validationErr("--self %q is not one of the participants", *self)
	}
	if !containsPermission(selfPermissions, discussion.PermissionManage) {
		return validationErr("--self participant %q must have manage permission", *self)
	}

	registration, err := communityserver.LoadRegistration(stateRoot, resolved.Name)
	if err != nil {
		return fmt.Errorf("load live server for community %q: %w; start `agent-community serve` first", resolved.Name, err)
	}
	participantInputs := participants.inputs()
	created, err := communityserver.CreateThroughControl(ctx, registration, discussion.CreateInput{
		TTL:          *ttl,
		Participants: participantInputs,
	})
	if err != nil {
		return err
	}
	connectionName := *name
	if connectionName == "" {
		connectionName, err = connection.NewName(created.Discussion.ID)
		if err != nil {
			return err
		}
	}
	localCapability := created.Capabilities[*self]
	localConnection := connection.Connection{
		Name:          connectionName,
		CommunityName: resolved.Name,
		LocalURL:      registration.LocalURL,
		PublicURL:     registration.PublicURL,
		DiscussionID:  created.Discussion.ID,
		ParticipantID: *self,
		Capability:    localCapability,
		ExpiresAt:     created.Discussion.ExpiresAt,
	}
	if err := connection.Save(stateRoot, localConnection); err != nil {
		// The server accepted creation but local persistence failed. End through
		// the in-memory self connection so the orphan cannot remain active.
		_, _ = communityclient.New(nil).End(ctx, &localConnection)
		return fmt.Errorf("save local connection: %w", err)
	}

	type invitation struct {
		BaseURL          string    `json:"base_url"`
		DiscussionID     string    `json:"discussion_id"`
		ParticipantToken string    `json:"participant_token"`
		ExpiresAt        time.Time `json:"expires_at"`
	}
	invitations := make(map[string]invitation, len(created.Capabilities)-1)
	for participantID, capability := range created.Capabilities {
		if participantID == *self {
			continue
		}
		invitations[participantID] = invitation{
			BaseURL:          registration.PublicURL,
			DiscussionID:     created.Discussion.ID,
			ParticipantToken: capability,
			ExpiresAt:        created.Discussion.ExpiresAt,
		}
	}
	return writeCommandJSON(output, map[string]any{
		"connection":  connectionName,
		"discussion":  created.Discussion,
		"invitations": invitations,
	})
}

func runDiscussionPost(ctx context.Context, args []string, output io.Writer, stateRoot, communityName string) error {
	fs := flag.NewFlagSet("discussion post", flag.ContinueOnError)
	body := fs.String("body", "", "Message body. Multiline text is supported.")
	replyTo := fs.String("reply-to", "", "Earlier message ID this message replies to.")
	idempotencyKey := fs.String("idempotency-key", "", "Stable retry key. Default: generated.")
	emitJSON := fs.Bool("json", false, "Emit JSON.")
	fs.SetOutput(io.Discard)
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() != 1 {
		return usageErr("discussion post requires one connection name")
	}
	if *body == "" {
		return validationErr("--body is required")
	}
	key := *idempotencyKey
	if key == "" {
		var err error
		key, err = newIdempotencyKey()
		if err != nil {
			return err
		}
	}
	target, err := connection.Load(stateRoot, communityName, fs.Arg(0))
	if err != nil {
		return err
	}
	message, replayed, err := communityclient.New(nil).Post(ctx, target, communityclient.PostInput{
		IdempotencyKey: key,
		Body:           *body,
		ReplyTo:        *replyTo,
	})
	if err != nil {
		return err
	}
	if *emitJSON {
		return writeCommandJSON(output, map[string]any{"message": message, "idempotency_key": key, "replayed": replayed})
	}
	fmt.Fprintf(output, "%d | %s | %s\n", message.Sequence, message.Author, message.Body)
	return nil
}

func runDiscussionRead(ctx context.Context, args []string, output io.Writer, stateRoot, communityName string) error {
	fs := flag.NewFlagSet("discussion read", flag.ContinueOnError)
	after := fs.Int("after", 0, "Read messages after this sequence.")
	limit := fs.Int("limit", 50, "Maximum messages, 1-100.")
	wait := fs.Duration("wait", 0, "Long-poll duration, up to 25s.")
	emitJSON := fs.Bool("json", false, "Emit JSON.")
	fs.SetOutput(io.Discard)
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() != 1 {
		return usageErr("discussion read requires one connection name")
	}
	if *after < 0 || *limit < 1 || *limit > discussion.MaxReadLimit || *wait < 0 || *wait > 25*time.Second {
		return validationErr("invalid --after, --limit, or --wait")
	}
	target, err := connection.Load(stateRoot, communityName, fs.Arg(0))
	if err != nil {
		return err
	}
	result, err := communityclient.New(nil).Read(ctx, target, communityclient.ReadOptions{
		AfterSequence: *after,
		Limit:         *limit,
		Wait:          *wait,
	})
	if err != nil {
		return err
	}
	if *emitJSON {
		return writeCommandJSON(output, result)
	}
	for _, message := range result.Messages {
		fmt.Fprintf(output, "%d | %s | %s\n", message.Sequence, message.Author, message.Body)
	}
	return nil
}

func runDiscussionEnd(ctx context.Context, args []string, output io.Writer, stateRoot, communityName string) error {
	fs := flag.NewFlagSet("discussion end", flag.ContinueOnError)
	emitJSON := fs.Bool("json", false, "Emit JSON.")
	fs.SetOutput(io.Discard)
	if err := parse(fs, args); err != nil {
		return usageErr("%v", err)
	}
	if fs.NArg() != 1 {
		return usageErr("discussion end requires one connection name")
	}
	target, err := connection.Load(stateRoot, communityName, fs.Arg(0))
	if err != nil {
		return err
	}
	ended, err := communityclient.New(nil).End(ctx, target)
	if err != nil {
		return err
	}
	if *emitJSON {
		return writeCommandJSON(output, ended)
	}
	fmt.Fprintf(output, "Ended discussion %s\n", ended.ID)
	return nil
}

type participantFlags map[string][]discussion.Permission

func (p *participantFlags) String() string {
	return ""
}

func (p *participantFlags) Set(value string) error {
	participantID, rawPermissions, ok := strings.Cut(value, "=")
	if !ok || participantID == "" || rawPermissions == "" {
		return errors.New("participant must use ID=permission,permission format")
	}
	if *p == nil {
		*p = make(participantFlags)
	}
	if _, exists := (*p)[participantID]; exists {
		return fmt.Errorf("participant %q was provided more than once", participantID)
	}
	parts := strings.Split(rawPermissions, ",")
	permissions := make([]discussion.Permission, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return errors.New("participant permission must not be empty")
		}
		permissions = append(permissions, discussion.Permission(part))
	}
	(*p)[participantID] = permissions
	return nil
}

func (p participantFlags) inputs() []discussion.ParticipantInput {
	inputs := make([]discussion.ParticipantInput, 0, len(p))
	for id, permissions := range p {
		inputs = append(inputs, discussion.ParticipantInput{ID: id, Permissions: permissions})
	}
	return inputs
}

func containsPermission(permissions []discussion.Permission, target discussion.Permission) bool {
	for _, permission := range permissions {
		if permission == target {
			return true
		}
	}
	return false
}

func extractDiscussionWorkspace(args []string) (string, []string, error) {
	var workspace string
	remaining := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		argument := args[i]
		if argument == "--workspace" {
			if i+1 >= len(args) {
				return "", nil, errors.New("--workspace requires a value")
			}
			workspace = args[i+1]
			i++
			continue
		}
		if strings.HasPrefix(argument, "--workspace=") {
			workspace = strings.TrimPrefix(argument, "--workspace=")
			continue
		}
		remaining = append(remaining, argument)
	}
	return workspace, remaining, nil
}

func newIdempotencyKey() (string, error) {
	id, err := ulid.New(ulid.Timestamp(time.Now().UTC()), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate idempotency key: %w", err)
	}
	return "cli-" + id.String(), nil
}

func writeCommandJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
