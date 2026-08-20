// Package communitymcp exposes remote discussion operations over MCP stdio.
package communitymcp

import (
	"context"
	"errors"
	"time"

	"github.com/jackjackson/agent-community/internal/communityclient"
	"github.com/jackjackson/agent-community/internal/discussion"
	"github.com/jackjackson/agent-community/internal/discussionapp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type CreateDiscussionInput struct {
	TTLSeconds        int    `json:"ttl_seconds,omitempty" jsonschema:"Discussion lifetime in seconds, from 1 to 7200. Defaults to 1800."`
	RemoteParticipant string `json:"remote_participant,omitempty" jsonschema:"Remote participant identity. Defaults to goat."`
}

type CreateDiscussionOutput struct {
	Connection string                   `json:"connection"`
	Discussion *discussion.Discussion   `json:"discussion"`
	Invitation discussionapp.Invitation `json:"invitation"`
}

type PostMessageInput struct {
	Connection     string `json:"connection" jsonschema:"Local connection name returned by create_discussion."`
	Body           string `json:"body" jsonschema:"Message body; multiline text is supported."`
	ReplyTo        string `json:"reply_to,omitempty" jsonschema:"Earlier message ID this message replies to."`
	IdempotencyKey string `json:"idempotency_key,omitempty" jsonschema:"Stable retry key. Generated when absent."`
}

type PostMessageOutput struct {
	Message        *discussion.Message `json:"message"`
	IdempotencyKey string              `json:"idempotency_key"`
	Replayed       bool                `json:"replayed"`
}

type ReadMessagesInput struct {
	Connection    string `json:"connection"`
	AfterSequence int    `json:"after_sequence,omitempty"`
	Limit         int    `json:"limit,omitempty"`
}

type ReadMessagesOutput struct {
	Messages     []discussion.Message `json:"messages"`
	LastSequence int                  `json:"last_sequence"`
	Status       discussion.Status    `json:"status"`
}

type WaitForMessageInput struct {
	Connection    string `json:"connection"`
	AfterSequence int    `json:"after_sequence,omitempty"`
	Author        string `json:"author,omitempty" jsonschema:"Return only messages from this author after the long poll completes."`
	WaitSeconds   int    `json:"wait_seconds,omitempty" jsonschema:"Long-poll duration in seconds, from 1 to 25. Defaults to 25."`
}

type EndDiscussionInput struct {
	Connection string `json:"connection"`
}

type EndDiscussionOutput struct {
	ID     string            `json:"id"`
	Status discussion.Status `json:"status"`
}

func New(service *discussionapp.Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "agent-community", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "create_discussion", Description: "Create an ephemeral remote interview discussion and return a one-time invitation for the remote participant.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input CreateDiscussionInput) (*mcp.CallToolResult, CreateDiscussionOutput, error) {
		return handleCreate(ctx, service, input)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "post_message", Description: "Post a message through a named local discussion connection.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input PostMessageInput) (*mcp.CallToolResult, PostMessageOutput, error) {
		return handlePost(ctx, service, input)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "read_messages", Description: "Read ordered discussion messages after a sequence cursor.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ReadMessagesInput) (*mcp.CallToolResult, ReadMessagesOutput, error) {
		return handleRead(ctx, service, input)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "wait_for_message", Description: "Long-poll for ordered discussion messages, optionally filtering the returned batch by author.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input WaitForMessageInput) (*mcp.CallToolResult, ReadMessagesOutput, error) {
		return handleWait(ctx, service, input)
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "end_discussion", Description: "End a discussion while retaining read access to its transcript.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input EndDiscussionInput) (*mcp.CallToolResult, EndDiscussionOutput, error) {
		return handleEnd(ctx, service, input)
	})
	return server
}

func handleCreate(ctx context.Context, service *discussionapp.Service, input CreateDiscussionInput) (*mcp.CallToolResult, CreateDiscussionOutput, error) {
	if service == nil {
		return nil, CreateDiscussionOutput{}, errors.New("discussion service is required")
	}
	ttlSeconds := input.TTLSeconds
	if ttlSeconds == 0 {
		ttlSeconds = 1800
	}
	remote := input.RemoteParticipant
	if remote == "" {
		remote = "goat"
	}
	if remote == "interviewer" {
		return nil, CreateDiscussionOutput{}, errors.New("remote participant must differ from interviewer")
	}
	result, err := service.Create(ctx, discussionapp.CreateInput{
		TTL: time.Duration(ttlSeconds) * time.Second, Self: "interviewer",
		Participants: []discussion.ParticipantInput{
			{ID: "interviewer", Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionPost, discussion.PermissionManage}},
			{ID: remote, Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionPost, discussion.PermissionSubscribe}},
		},
	})
	if err != nil {
		return nil, CreateDiscussionOutput{}, err
	}
	return nil, CreateDiscussionOutput{Connection: result.Connection, Discussion: result.Discussion, Invitation: result.Invitations[remote]}, nil
}

func handlePost(ctx context.Context, service *discussionapp.Service, input PostMessageInput) (*mcp.CallToolResult, PostMessageOutput, error) {
	if service == nil || input.Connection == "" || input.Body == "" {
		return nil, PostMessageOutput{}, errors.New("connection and body are required")
	}
	result, err := service.Post(ctx, input.Connection, communityclient.PostInput{
		IdempotencyKey: input.IdempotencyKey, Body: input.Body, ReplyTo: input.ReplyTo,
	})
	if err != nil {
		return nil, PostMessageOutput{}, err
	}
	return nil, PostMessageOutput{Message: result.Message, IdempotencyKey: result.IdempotencyKey, Replayed: result.Replayed}, nil
}

func handleRead(ctx context.Context, service *discussionapp.Service, input ReadMessagesInput) (*mcp.CallToolResult, ReadMessagesOutput, error) {
	if service == nil || input.Connection == "" || input.AfterSequence < 0 {
		return nil, ReadMessagesOutput{}, errors.New("valid connection and cursor are required")
	}
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > discussion.MaxReadLimit {
		return nil, ReadMessagesOutput{}, errors.New("limit must be between 1 and 100")
	}
	result, err := service.Read(ctx, input.Connection, communityclient.ReadOptions{AfterSequence: input.AfterSequence, Limit: limit})
	if err != nil {
		return nil, ReadMessagesOutput{}, err
	}
	return nil, readOutput(result), nil
}

func handleWait(ctx context.Context, service *discussionapp.Service, input WaitForMessageInput) (*mcp.CallToolResult, ReadMessagesOutput, error) {
	if service == nil || input.Connection == "" || input.AfterSequence < 0 {
		return nil, ReadMessagesOutput{}, errors.New("valid connection and cursor are required")
	}
	waitSeconds := input.WaitSeconds
	if waitSeconds == 0 {
		waitSeconds = 25
	}
	if waitSeconds < 1 || waitSeconds > 25 {
		return nil, ReadMessagesOutput{}, errors.New("wait_seconds must be between 1 and 25")
	}
	result, err := service.Read(ctx, input.Connection, communityclient.ReadOptions{
		AfterSequence: input.AfterSequence, Limit: discussion.MaxReadLimit, Wait: time.Duration(waitSeconds) * time.Second,
	})
	if err != nil {
		return nil, ReadMessagesOutput{}, err
	}
	output := readOutput(result)
	if input.Author != "" {
		filtered := output.Messages[:0]
		for _, message := range output.Messages {
			if message.Author == input.Author {
				filtered = append(filtered, message)
			}
		}
		output.Messages = filtered
	}
	return nil, output, nil
}

func handleEnd(ctx context.Context, service *discussionapp.Service, input EndDiscussionInput) (*mcp.CallToolResult, EndDiscussionOutput, error) {
	if service == nil || input.Connection == "" {
		return nil, EndDiscussionOutput{}, errors.New("connection is required")
	}
	ended, err := service.End(ctx, input.Connection)
	if err != nil {
		return nil, EndDiscussionOutput{}, err
	}
	return nil, EndDiscussionOutput{ID: ended.ID, Status: ended.Status}, nil
}

func readOutput(result *communityclient.ReadResult) ReadMessagesOutput {
	return ReadMessagesOutput{Messages: result.Messages, LastSequence: result.LastSequence, Status: result.Status}
}
