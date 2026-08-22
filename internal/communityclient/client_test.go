package communityclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/scubbo/agent-community/internal/communityserver"
	"github.com/scubbo/agent-community/internal/connection"
	"github.com/scubbo/agent-community/internal/discussion"
)

func newClientFixture(t *testing.T) (*Client, *connection.Connection, func()) {
	t.Helper()
	store, err := discussion.NewStore(t.TempDir(), discussion.StoreOptions{})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	created, capabilities, err := store.Create(discussion.CreateInput{
		TTL: 30 * time.Minute,
		Participants: []discussion.ParticipantInput{
			{ID: "interviewer", Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionPost, discussion.PermissionManage}},
		},
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	handler, err := communityserver.New(store, communityserver.Options{InstanceID: "client-test"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	server := httptest.NewServer(handler)
	return New(nil), &connection.Connection{
		Name:          "interview-test",
		CommunityName: "test-community",
		LocalURL:      server.URL,
		PublicURL:     "https://community.example",
		DiscussionID:  created.ID,
		ParticipantID: "interviewer",
		Capability:    capabilities["interviewer"],
		ExpiresAt:     created.ExpiresAt,
	}, server.Close
}

func TestPostReadGetAndEnd(t *testing.T) {
	client, target, closeServer := newClientFixture(t)
	defer closeServer()
	ctx := context.Background()

	posted, replayed, err := client.Post(ctx, target, PostInput{
		IdempotencyKey: "question-1",
		Body:           "Why?",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if replayed || posted.Sequence != 1 || posted.Author != "interviewer" {
		t.Errorf("unexpected post: replayed=%v message=%#v", replayed, posted)
	}
	_, replayed, err = client.Post(ctx, target, PostInput{IdempotencyKey: "question-1", Body: "Why?"})
	if err != nil || !replayed {
		t.Fatalf("replay: replayed=%v err=%v", replayed, err)
	}

	read, err := client.Read(ctx, target, ReadOptions{AfterSequence: 0, Limit: 50})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(read.Messages) != 1 || read.LastSequence != 1 || read.Status != discussion.StatusActive {
		t.Errorf("unexpected read: %#v", read)
	}

	metadata, err := client.Get(ctx, target)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if metadata.Discussion.ID != target.DiscussionID || metadata.Self.ID != "interviewer" {
		t.Errorf("unexpected metadata: %#v", metadata)
	}

	ended, err := client.End(ctx, target)
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if ended.Status != discussion.StatusEnded {
		t.Errorf("status: got %q want ended", ended.Status)
	}
}

func TestReadWaitsForMessage(t *testing.T) {
	client, target, closeServer := newClientFixture(t)
	defer closeServer()

	type result struct {
		read *ReadResult
		err  error
	}
	done := make(chan result, 1)
	go func() {
		read, err := client.Read(context.Background(), target, ReadOptions{Wait: time.Second})
		done <- result{read: read, err: err}
	}()
	time.Sleep(30 * time.Millisecond)
	if _, _, err := client.Post(context.Background(), target, PostInput{IdempotencyKey: "wake", Body: "wake"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("read: %v", result.err)
		}
		if len(result.read.Messages) != 1 || result.read.Messages[0].Body != "wake" {
			t.Errorf("unexpected read: %#v", result.read)
		}
	case <-time.After(time.Second):
		t.Fatal("read did not wake")
	}
}

func TestAPIErrorPreservesCodeAndStatus(t *testing.T) {
	client, target, closeServer := newClientFixture(t)
	defer closeServer()
	_, _, err := client.Post(context.Background(), target, PostInput{IdempotencyKey: "", Body: "body"})
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("got %v want APIError", err)
	}
	if apiError.Status != http.StatusBadRequest || apiError.Code != "invalid_input" {
		t.Errorf("unexpected API error: %#v", apiError)
	}
}

func TestClientRejectsOversizedAndMalformedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "oversized", body: strings.Repeat("x", MaxResponseBodyBytes+1)},
		{name: "malformed", body: `{`},
		{name: "unknown field", body: `{"ok":true,"messages":[],"last_sequence":0,"status":"active","unknown":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client := New(nil)
			target := &connection.Connection{LocalURL: server.URL, DiscussionID: "id", Capability: "token"}
			if _, err := client.Read(context.Background(), target, ReadOptions{}); err == nil {
				t.Fatal("expected response error")
			}
		})
	}
}

func TestClientHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client := New(nil)
	target := &connection.Connection{LocalURL: server.URL, DiscussionID: "id", Capability: "token"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Read(ctx, target, ReadOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v want context.Canceled", err)
	}
}
