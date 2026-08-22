package communitymcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/scubbo/agent-community/internal/communityserver"
	"github.com/scubbo/agent-community/internal/discussionapp"
)

func TestMCPToolsEndToEnd(t *testing.T) {
	root := t.TempDir()
	stateRoot := t.TempDir()
	socketRoot, err := os.MkdirTemp("/tmp", "agent-community-mcp-")
	if err != nil {
		t.Fatalf("socket root: %v", err)
	}
	defer os.RemoveAll(socketRoot)
	runtime, err := communityserver.Start(communityserver.RuntimeOptions{
		CommunityName: "test-community", CommunityRoot: root, StateRoot: stateRoot,
		ListenAddress: "127.0.0.1:0", PublicURL: "http://127.0.0.1:7337",
		SocketPath: filepath.Join(socketRoot, "control.sock"), AllowLoopbackHTTP: true,
	})
	if err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	defer runtime.Close(context.Background())

	server := New(&discussionapp.Service{StateRoot: stateRoot, CommunityName: "test-community"})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatalf("connect server: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("connect client: %v", err)
	}
	defer session.Close()

	var toolNames []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		toolNames = append(toolNames, tool.Name)
	}
	sort.Strings(toolNames)
	wantTools := []string{"create_discussion", "end_discussion", "post_message", "read_messages", "wait_for_message"}
	if stringJSON(toolNames) != stringJSON(wantTools) {
		t.Fatalf("tools: got %v want %v", toolNames, wantTools)
	}

	createdResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_discussion", Arguments: map[string]any{"ttl_seconds": 600, "remote_participant": "goat"},
	})
	if err != nil || createdResult.IsError {
		t.Fatalf("create tool: result=%#v err=%v", createdResult, err)
	}
	var created CreateDiscussionOutput
	decodeStructured(t, createdResult.StructuredContent, &created)
	if created.Connection == "" || created.Invitation.ParticipantToken == "" || created.Invitation.DiscussionID == "" {
		t.Errorf("unexpected create output: %#v", created)
	}

	postResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "post_message", Arguments: map[string]any{"connection": created.Connection, "body": "Why?"},
	})
	if err != nil || postResult.IsError {
		t.Fatalf("post tool: result=%#v err=%v", postResult, err)
	}
	var posted PostMessageOutput
	decodeStructured(t, postResult.StructuredContent, &posted)
	if posted.Message.Sequence != 1 || posted.IdempotencyKey == "" {
		t.Errorf("unexpected post output: %#v", posted)
	}

	readResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "read_messages", Arguments: map[string]any{"connection": created.Connection, "after_sequence": 0, "limit": 50},
	})
	if err != nil || readResult.IsError {
		t.Fatalf("read tool: result=%#v err=%v", readResult, err)
	}
	var read ReadMessagesOutput
	decodeStructured(t, readResult.StructuredContent, &read)
	if len(read.Messages) != 1 || read.Messages[0].Body != "Why?" || read.LastSequence != 1 {
		t.Errorf("unexpected read output: %#v", read)
	}

	endResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "end_discussion", Arguments: map[string]any{"connection": created.Connection},
	})
	if err != nil || endResult.IsError {
		t.Fatalf("end tool: result=%#v err=%v", endResult, err)
	}
	var ended EndDiscussionOutput
	decodeStructured(t, endResult.StructuredContent, &ended)
	if ended.Status != "ended" {
		t.Errorf("status: got %q want ended", ended.Status)
	}
}

func TestWaitToolCapsDuration(t *testing.T) {
	if _, _, err := handleWait(context.Background(), nil, WaitForMessageInput{WaitSeconds: 26}); err == nil {
		t.Fatal("expected wait duration validation error")
	}
}

func decodeStructured(t *testing.T, value any, destination any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal structured output: %v", err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatalf("decode structured output: %v", err)
	}
}

func stringJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}
