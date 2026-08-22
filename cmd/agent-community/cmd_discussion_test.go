package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scubbo/agent-community/internal/community"
	"github.com/scubbo/agent-community/internal/communityserver"
	"github.com/scubbo/agent-community/internal/connection"
)

type discussionCLIFixture struct {
	stateRoot  string
	workspace  string
	runtime    *communityserver.Runtime
	socketRoot string
}

func newDiscussionCLIFixture(t *testing.T) discussionCLIFixture {
	t.Helper()
	base := t.TempDir()
	stateHome := filepath.Join(base, "state")
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv(community.EnvVar, "")
	root, err := community.Init(community.InitOptions{Name: "remote-test"})
	if err != nil {
		t.Fatalf("init community: %v", err)
	}
	workspace := t.TempDir()
	if _, err := community.Join(community.JoinOptions{Name: "remote-test", Workspace: workspace}); err != nil {
		t.Fatalf("join community: %v", err)
	}
	socketRoot, err := os.MkdirTemp("/tmp", "agent-community-discussion-")
	if err != nil {
		t.Fatalf("create socket root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketRoot) })
	stateRoot := filepath.Join(stateHome, "agent-community")
	runtime, err := communityserver.Start(communityserver.RuntimeOptions{
		CommunityName:     "remote-test",
		CommunityRoot:     root,
		StateRoot:         stateRoot,
		ListenAddress:     "127.0.0.1:0",
		PublicURL:         "http://127.0.0.1:7337",
		SocketPath:        filepath.Join(socketRoot, "control.sock"),
		AllowLoopbackHTTP: true,
	})
	if err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	return discussionCLIFixture{stateRoot: stateRoot, workspace: workspace, runtime: runtime, socketRoot: socketRoot}
}

func (f discussionCLIFixture) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var output bytes.Buffer
	err := runDiscussion(context.Background(), append([]string{"--workspace", f.workspace}, args...), &output)
	return output.String(), err
}

func TestDiscussionCreateReturnsInvitationAndStoresOnlyLocalConnection(t *testing.T) {
	fixture := newDiscussionCLIFixture(t)
	output, err := fixture.run(t,
		"create",
		"--ttl", "10m",
		"--participant", "interviewer=read,post,manage",
		"--participant", "goat=read,post,subscribe",
		"--self", "interviewer",
		"--json",
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var result struct {
		Connection string `json:"connection"`
		Discussion struct {
			ID        string    `json:"id"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"discussion"`
		Invitations map[string]struct {
			BaseURL          string    `json:"base_url"`
			DiscussionID     string    `json:"discussion_id"`
			ParticipantToken string    `json:"participant_token"`
			ExpiresAt        time.Time `json:"expires_at"`
		} `json:"invitations"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output %q: %v", output, err)
	}
	if result.Connection == "" || result.Discussion.ID == "" {
		t.Errorf("missing connection or discussion: %#v", result)
	}
	goat := result.Invitations["goat"]
	if goat.BaseURL != "http://127.0.0.1:7337" || goat.DiscussionID != result.Discussion.ID || goat.ParticipantToken == "" {
		t.Errorf("unexpected goat invitation: %#v", goat)
	}
	if _, exists := result.Invitations["interviewer"]; exists {
		t.Error("output exposes local participant as an invitation")
	}

	stored, err := connection.Load(fixture.stateRoot, "remote-test", result.Connection)
	if err != nil {
		t.Fatalf("load connection: %v", err)
	}
	if stored.ParticipantID != "interviewer" || stored.Capability == "" {
		t.Errorf("unexpected local connection: %#v", stored)
	}
	if stored.Capability == goat.ParticipantToken {
		t.Error("local connection stored remote participant capability")
	}
}

func TestDiscussionCreateRequiresExplicitJSON(t *testing.T) {
	fixture := newDiscussionCLIFixture(t)
	_, err := fixture.run(t,
		"create",
		"--participant", "interviewer=read,post,manage",
		"--participant", "goat=read,post,subscribe",
		"--self", "interviewer",
	)
	if err == nil || !strings.Contains(err.Error(), "--json") {
		t.Fatalf("got %v want --json error", err)
	}
}

func TestDiscussionCreateRequiresLocalManagePermission(t *testing.T) {
	fixture := newDiscussionCLIFixture(t)
	_, err := fixture.run(t,
		"create",
		"--participant", "interviewer=read,post",
		"--participant", "goat=read,post,subscribe",
		"--self", "interviewer",
		"--json",
	)
	if err == nil || !strings.Contains(err.Error(), "manage") {
		t.Fatalf("got %v want manage permission error", err)
	}
}

func TestDiscussionPostReadAndEnd(t *testing.T) {
	fixture := newDiscussionCLIFixture(t)
	createdJSON, err := fixture.run(t,
		"create",
		"--participant", "interviewer=read,post,manage",
		"--participant", "goat=read,post,subscribe",
		"--self", "interviewer",
		"--json",
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created struct {
		Connection string `json:"connection"`
	}
	if err := json.Unmarshal([]byte(createdJSON), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	postedJSON, err := fixture.run(t, "post", created.Connection, "--body", "Why this design?\nExplain.", "--json")
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	var posted struct {
		IdempotencyKey string `json:"idempotency_key"`
		Message        struct {
			Sequence int    `json:"sequence"`
			Author   string `json:"author"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(postedJSON), &posted); err != nil {
		t.Fatalf("decode post: %v", err)
	}
	if posted.IdempotencyKey == "" || posted.Message.Sequence != 1 || posted.Message.Author != "interviewer" {
		t.Errorf("unexpected post: %#v", posted)
	}

	readJSON, err := fixture.run(t, "read", created.Connection, "--after-sequence", "0", "--limit", "50", "--json")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var read struct {
		Messages []struct {
			Body string `json:"body"`
		} `json:"messages"`
		LastSequence int `json:"last_sequence"`
	}
	if err := json.Unmarshal([]byte(readJSON), &read); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if len(read.Messages) != 1 || read.Messages[0].Body != "Why this design?\nExplain." || read.LastSequence != 1 {
		t.Errorf("unexpected read: %#v", read)
	}

	endedJSON, err := fixture.run(t, "end", created.Connection, "--json")
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if !strings.Contains(endedJSON, `"status":"ended"`) {
		t.Errorf("unexpected end output: %s", endedJSON)
	}
	if _, err := connection.Load(fixture.stateRoot, "remote-test", created.Connection); err != nil {
		t.Fatalf("connection should remain for transcript reads: %v", err)
	}
}

func TestDiscussionCommandRejectsUnknownSubcommand(t *testing.T) {
	fixture := newDiscussionCLIFixture(t)
	_, err := fixture.run(t, "nope")
	if err == nil || !strings.Contains(err.Error(), "unknown discussion command") {
		t.Fatalf("got %v want unknown command error", err)
	}
}
