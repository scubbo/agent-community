package communityserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackjackson/agent-community/internal/discussion"
)

type serverFixture struct {
	server       *httptest.Server
	discussionID string
	capabilities map[string]string
}

func newServerFixture(t *testing.T) serverFixture {
	t.Helper()
	store, err := discussion.NewStore(t.TempDir(), discussion.StoreOptions{})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	created, capabilities, err := store.Create(discussion.CreateInput{
		TTL: 30 * time.Minute,
		Participants: []discussion.ParticipantInput{
			{ID: "interviewer", Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionPost, discussion.PermissionManage}},
			{ID: "goat", Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionPost, discussion.PermissionSubscribe}},
		},
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	handler, err := New(store, Options{InstanceID: "instance-test"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return serverFixture{
		server:       httptest.NewServer(handler),
		discussionID: created.ID,
		capabilities: capabilities,
	}
}

func (f serverFixture) close() {
	f.server.Close()
}

func (f serverFixture) request(t *testing.T, method, path, participant string, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if participant != "" {
		req.Header.Set("Authorization", "Bearer "+f.capabilities[participant])
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatalf("decode response %q: %v", data, err)
	}
}

func TestHealthExposesOnlyProcessIdentity(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()

	response := fixture.request(t, http.MethodGet, "/healthz", "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", response.StatusCode)
	}
	var body map[string]any
	decodeResponse(t, response, &body)
	if body["ok"] != true || body["instance_id"] != "instance-test" {
		t.Errorf("unexpected health body: %#v", body)
	}
	if len(body) != 2 {
		t.Errorf("health leaked unexpected fields: %#v", body)
	}
}

func TestDiscussionMetadataIncludesOnlyAuthenticatedPermissions(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()

	response := fixture.request(t, http.MethodGet, "/v1/discussions/"+fixture.discussionID, "goat", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", response.StatusCode)
	}
	var body struct {
		OK         bool `json:"ok"`
		Discussion struct {
			ID           string `json:"id"`
			LastSequence int    `json:"last_sequence"`
			Participants []struct {
				ID string `json:"id"`
			} `json:"participants"`
			Self struct {
				ID          string   `json:"id"`
				Permissions []string `json:"permissions"`
			} `json:"self"`
		} `json:"discussion"`
	}
	decodeResponse(t, response, &body)
	if !body.OK || body.Discussion.ID != fixture.discussionID {
		t.Errorf("unexpected metadata: %#v", body)
	}
	if body.Discussion.Self.ID != "goat" {
		t.Errorf("self id: got %q want goat", body.Discussion.Self.ID)
	}
	if got := strings.Join(body.Discussion.Self.Permissions, ","); got != "read,post,subscribe" {
		t.Errorf("self permissions: got %q", got)
	}
	if len(body.Discussion.Participants) != 2 {
		t.Errorf("participants: got %d want 2", len(body.Discussion.Participants))
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	if strings.Contains(string(encoded), "manage") || strings.Contains(string(encoded), "token") {
		t.Errorf("metadata leaked another participant permission or token data: %s", encoded)
	}
}

func TestAuthenticationAndMethodRouting(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()
	path := "/v1/discussions/" + fixture.discussionID

	tests := []struct {
		name       string
		method     string
		path       string
		authorize  string
		wantStatus int
		wantError  string
	}{
		{name: "missing bearer", method: http.MethodGet, path: path, wantStatus: http.StatusUnauthorized, wantError: "unauthorized"},
		{name: "malformed bearer", method: http.MethodGet, path: path, authorize: "Basic nope", wantStatus: http.StatusUnauthorized, wantError: "unauthorized"},
		{name: "unknown route", method: http.MethodGet, path: "/v1/unknown", authorize: "Bearer " + fixture.capabilities["goat"], wantStatus: http.StatusNotFound, wantError: "not_found"},
		{name: "unsupported method", method: http.MethodPatch, path: path, authorize: "Bearer " + fixture.capabilities["goat"], wantStatus: http.StatusMethodNotAllowed, wantError: "method_not_allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, fixture.server.URL+tt.path, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			if tt.authorize != "" {
				req.Header.Set("Authorization", tt.authorize)
			}
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if response.StatusCode != tt.wantStatus {
				t.Fatalf("status: got %d want %d", response.StatusCode, tt.wantStatus)
			}
			var body struct {
				Error string `json:"error"`
			}
			decodeResponse(t, response, &body)
			if body.Error != tt.wantError {
				t.Errorf("error: got %q want %q", body.Error, tt.wantError)
			}
		})
	}
}

func TestPostAndCursorRead(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()
	messagesPath := "/v1/discussions/" + fixture.discussionID + "/messages"

	post := fixture.request(t, http.MethodPost, messagesPath, "interviewer", `{"idempotency_key":"question-1","body":"Why?\nPlease explain."}`)
	if post.StatusCode != http.StatusCreated {
		t.Fatalf("post status: got %d want 201", post.StatusCode)
	}
	var posted struct {
		Message struct {
			ID       string `json:"id"`
			Sequence int    `json:"sequence"`
			Author   string `json:"author"`
			Body     string `json:"body"`
		} `json:"message"`
	}
	decodeResponse(t, post, &posted)
	if posted.Message.Sequence != 1 || posted.Message.Author != "interviewer" || posted.Message.Body != "Why?\nPlease explain." {
		t.Errorf("unexpected posted message: %#v", posted)
	}

	replay := fixture.request(t, http.MethodPost, messagesPath, "interviewer", `{"idempotency_key":"question-1","body":"Why?\nPlease explain."}`)
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("replay status: got %d want 200", replay.StatusCode)
	}
	replay.Body.Close()

	read := fixture.request(t, http.MethodGet, messagesPath+"?after_sequence=0&limit=1", "goat", "")
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status: got %d want 200", read.StatusCode)
	}
	var result struct {
		Messages []struct {
			ID       string `json:"id"`
			Sequence int    `json:"sequence"`
		} `json:"messages"`
		LastSequence int    `json:"last_sequence"`
		Status       string `json:"status"`
	}
	decodeResponse(t, read, &result)
	if len(result.Messages) != 1 || result.Messages[0].ID != posted.Message.ID || result.LastSequence != 1 || result.Status != "active" {
		t.Errorf("unexpected read: %#v", result)
	}
}

func TestPostErrorsAndRequestLimits(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()
	path := "/v1/discussions/" + fixture.discussionID + "/messages"

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{name: "malformed", body: `{`, wantStatus: http.StatusBadRequest, wantError: "invalid_body"},
		{name: "unknown field", body: `{"idempotency_key":"x","body":"x","author":"goat"}`, wantStatus: http.StatusBadRequest, wantError: "invalid_body"},
		{name: "empty body", body: `{"idempotency_key":"x","body":""}`, wantStatus: http.StatusBadRequest, wantError: "invalid_input"},
		{name: "message too large", body: fmt.Sprintf(`{"idempotency_key":"x","body":%q}`, strings.Repeat("x", discussion.MaxBodyBytes+1)), wantStatus: http.StatusRequestEntityTooLarge, wantError: "message_too_large"},
		{name: "request too large", body: `{"idempotency_key":"x","body":"x","padding":"` + strings.Repeat("x", MaxRequestBodyBytes) + `"}`, wantStatus: http.StatusRequestEntityTooLarge, wantError: "request_too_large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := fixture.request(t, http.MethodPost, path, "interviewer", tt.body)
			if response.StatusCode != tt.wantStatus {
				data, _ := io.ReadAll(response.Body)
				response.Body.Close()
				t.Fatalf("status: got %d want %d body=%s", response.StatusCode, tt.wantStatus, data)
			}
			var body struct {
				Error string `json:"error"`
			}
			decodeResponse(t, response, &body)
			if body.Error != tt.wantError {
				t.Errorf("error: got %q want %q", body.Error, tt.wantError)
			}
		})
	}

	first := fixture.request(t, http.MethodPost, path, "interviewer", `{"idempotency_key":"same","body":"first"}`)
	first.Body.Close()
	conflict := fixture.request(t, http.MethodPost, path, "interviewer", `{"idempotency_key":"same","body":"second"}`)
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status: got %d want 409", conflict.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	decodeResponse(t, conflict, &body)
	if body.Error != "idempotency_conflict" {
		t.Errorf("conflict error: got %q", body.Error)
	}
}

func TestReadValidatesQuery(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()
	base := "/v1/discussions/" + fixture.discussionID + "/messages?"
	for _, query := range []string{"after_sequence=-1", "after_sequence=nope", "limit=101", "wait=26s", "wait=nope", "unknown=x"} {
		response := fixture.request(t, http.MethodGet, base+query, "goat", "")
		if response.StatusCode != http.StatusBadRequest {
			data, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Errorf("query %q status: got %d want 400 body=%s", query, response.StatusCode, data)
			continue
		}
		response.Body.Close()
	}
}

func TestLongPollWakesForNewMessage(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()
	path := "/v1/discussions/" + fixture.discussionID + "/messages"

	type result struct {
		response *http.Response
		err      error
	}
	resultChannel := make(chan result, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, fixture.server.URL+path+"?after_sequence=0&wait=2s", nil)
		if err != nil {
			resultChannel <- result{err: err}
			return
		}
		req.Header.Set("Authorization", "Bearer "+fixture.capabilities["goat"])
		response, err := http.DefaultClient.Do(req)
		resultChannel <- result{response: response, err: err}
	}()

	time.Sleep(50 * time.Millisecond)
	posted := fixture.request(t, http.MethodPost, path, "interviewer", `{"idempotency_key":"wake","body":"wake up"}`)
	posted.Body.Close()

	select {
	case result := <-resultChannel:
		if result.err != nil {
			t.Fatalf("long poll: %v", result.err)
		}
		if result.response.StatusCode != http.StatusOK {
			t.Fatalf("status: got %d want 200", result.response.StatusCode)
		}
		var body struct {
			Messages []struct {
				Body string `json:"body"`
			} `json:"messages"`
		}
		decodeResponse(t, result.response, &body)
		if len(body.Messages) != 1 || body.Messages[0].Body != "wake up" {
			t.Errorf("unexpected long-poll body: %#v", body)
		}
	case <-time.After(time.Second):
		t.Fatal("long poll did not wake after post")
	}
}

func TestLongPollReturnsEmptyOnTimeout(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()
	started := time.Now()
	response := fixture.request(t, http.MethodGet, "/v1/discussions/"+fixture.discussionID+"/messages?after_sequence=0&wait=50ms", "goat", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", response.StatusCode)
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Errorf("long poll returned too early after %s", elapsed)
	}
	var body struct {
		Messages []any `json:"messages"`
	}
	decodeResponse(t, response, &body)
	if len(body.Messages) != 0 {
		t.Errorf("messages: got %d want 0", len(body.Messages))
	}
}

func TestLongPollDoesNotWaitPastDiscussionExpiry(t *testing.T) {
	store, err := discussion.NewStore(t.TempDir(), discussion.StoreOptions{})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	created, capabilities, err := store.Create(discussion.CreateInput{
		TTL: 60 * time.Millisecond,
		Participants: []discussion.ParticipantInput{
			{ID: "observer", Permissions: []discussion.Permission{discussion.PermissionRead}},
		},
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	handler, err := New(store, Options{InstanceID: "expiry-test"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/v1/discussions/"+created.ID+"/messages?wait=2s", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+capabilities["observer"])
	started := time.Now()
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Errorf("long poll waited past discussion expiry: %s", elapsed)
	}
	var body struct {
		Status string `json:"status"`
	}
	decodeResponse(t, response, &body)
	if body.Status != "expired" {
		t.Errorf("status: got %q want expired", body.Status)
	}
}

func TestLongPollHonorsCancellation(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fixture.server.URL+"/v1/discussions/"+fixture.discussionID+"/messages?wait=5s", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+fixture.capabilities["goat"])
	done := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(req)
		if response != nil {
			response.Body.Close()
		}
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled long poll returned no client error")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled long poll did not return")
	}
}

func TestEndWakesLongPollAndRejectsFuturePosts(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()
	path := "/v1/discussions/" + fixture.discussionID

	result := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodGet, fixture.server.URL+path+"/messages?wait=2s", nil)
		req.Header.Set("Authorization", "Bearer "+fixture.capabilities["goat"])
		response, _ := http.DefaultClient.Do(req)
		result <- response
	}()
	time.Sleep(50 * time.Millisecond)
	ended := fixture.request(t, http.MethodDelete, path, "interviewer", "")
	if ended.StatusCode != http.StatusOK {
		t.Fatalf("end status: got %d want 200", ended.StatusCode)
	}
	ended.Body.Close()

	select {
	case response := <-result:
		if response == nil {
			t.Fatal("long poll returned nil response")
		}
		var body struct {
			Status string `json:"status"`
		}
		decodeResponse(t, response, &body)
		if body.Status != "ended" {
			t.Errorf("status: got %q want ended", body.Status)
		}
	case <-time.After(time.Second):
		t.Fatal("end did not wake long poll")
	}

	post := fixture.request(t, http.MethodPost, path+"/messages", "goat", `{"idempotency_key":"late","body":"late"}`)
	if post.StatusCode != http.StatusConflict {
		t.Fatalf("late post status: got %d want 409", post.StatusCode)
	}
	var body struct {
		Error string `json:"error"`
	}
	decodeResponse(t, post, &body)
	if body.Error != "discussion_ended" {
		t.Errorf("late post error: got %q", body.Error)
	}
}

func TestResponsesAreJSONAndDisableSniffing(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()
	response := fixture.request(t, http.MethodGet, "/healthz", "", "")
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("content-type: got %q want application/json", got)
	}
	if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("x-content-type-options: got %q want nosniff", got)
	}

	authenticated := fixture.request(t, http.MethodGet, "/v1/discussions/"+fixture.discussionID, "goat", "")
	defer authenticated.Body.Close()
	if got := authenticated.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control: got %q want no-store", got)
	}
}

func TestNoPublicCreateRoute(t *testing.T) {
	fixture := newServerFixture(t)
	defer fixture.close()
	response := fixture.request(t, http.MethodPost, "/v1/discussions", "", bytes.NewBufferString(`{}`).String())
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", response.StatusCode)
	}
	response.Body.Close()
}
