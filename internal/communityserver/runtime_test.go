package communityserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackjackson/agent-community/internal/discussion"
)

func newRuntimeOptions(t *testing.T) RuntimeOptions {
	t.Helper()
	root := t.TempDir()
	stateRoot := t.TempDir()
	return RuntimeOptions{
		CommunityName: "test-community",
		CommunityRoot: root,
		StateRoot:     stateRoot,
		ListenAddress: "127.0.0.1:0",
		PublicURL:     "https://community.example",
		SocketPath:    filepath.Join(stateRoot, "control", "community.sock"),
	}
}

func TestRuntimePublishesRegistrationAndCreatesThroughLocalControl(t *testing.T) {
	opts := newRuntimeOptions(t)
	runtime, err := Start(opts)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer runtime.Close(context.Background())

	registration, err := LoadRegistration(opts.StateRoot, opts.CommunityName)
	if err != nil {
		t.Fatalf("load registration: %v", err)
	}
	if registration.InstanceID == "" || registration.ControlSecret == "" {
		t.Errorf("registration missing instance or control secret: %#v", registration)
	}
	if registration.PublicURL != opts.PublicURL {
		t.Errorf("public URL: got %q want %q", registration.PublicURL, opts.PublicURL)
	}
	if registration.LocalURL != runtime.LocalURL() {
		t.Errorf("local URL: got %q want %q", registration.LocalURL, runtime.LocalURL())
	}
	if registration.ControlSocket != opts.SocketPath {
		t.Errorf("control socket: got %q want %q", registration.ControlSocket, opts.SocketPath)
	}
	assertRuntimeMode(t, RegistrationPath(opts.StateRoot, opts.CommunityName), 0o600)
	assertRuntimeMode(t, filepath.Dir(opts.SocketPath), 0o700)
	assertRuntimeMode(t, opts.SocketPath, 0o600)

	created, err := CreateThroughControl(context.Background(), registration, discussion.CreateInput{
		TTL: 10 * time.Minute,
		Participants: []discussion.ParticipantInput{
			{ID: "interviewer", Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionPost, discussion.PermissionManage}},
			{ID: "goat", Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionPost}},
		},
	})
	if err != nil {
		t.Fatalf("create through control: %v", err)
	}
	if created.Discussion.ID == "" || len(created.Capabilities) != 2 {
		t.Errorf("unexpected create result: %#v", created)
	}
	if strings.Contains(created.Capabilities["goat"], " ") {
		t.Errorf("capability contains whitespace: %q", created.Capabilities["goat"])
	}

	req, err := http.NewRequest(http.MethodGet, runtime.LocalURL()+"/v1/discussions/"+created.Discussion.ID, nil)
	if err != nil {
		t.Fatalf("new metadata request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+created.Capabilities["goat"])
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("metadata request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("metadata status: got %d want 200 body=%s", response.StatusCode, data)
	}
}

func TestRuntimeKeepsCreateOffPublicTCP(t *testing.T) {
	opts := newRuntimeOptions(t)
	runtime, err := Start(opts)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer runtime.Close(context.Background())

	response, err := http.Post(runtime.LocalURL()+"/v1/discussions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", response.StatusCode)
	}
}

func TestRuntimeRejectsInvalidControlAuthentication(t *testing.T) {
	opts := newRuntimeOptions(t)
	runtime, err := Start(opts)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer runtime.Close(context.Background())

	registration, err := LoadRegistration(opts.StateRoot, opts.CommunityName)
	if err != nil {
		t.Fatalf("load registration: %v", err)
	}
	registration.ControlSecret = "wrong"
	_, err = CreateThroughControl(context.Background(), registration, discussion.CreateInput{
		TTL:          time.Minute,
		Participants: []discussion.ParticipantInput{{ID: "goat", Permissions: []discussion.Permission{discussion.PermissionRead}}},
	})
	if !errors.Is(err, ErrControlUnauthorized) {
		t.Fatalf("got %v want ErrControlUnauthorized", err)
	}
}

func TestRuntimeRejectsSecondWriter(t *testing.T) {
	opts := newRuntimeOptions(t)
	first, err := Start(opts)
	if err != nil {
		t.Fatalf("start first: %v", err)
	}
	defer first.Close(context.Background())

	secondOpts := opts
	secondOpts.ListenAddress = "127.0.0.1:0"
	secondOpts.SocketPath = filepath.Join(opts.StateRoot, "control", "second.sock")
	second, err := Start(secondOpts)
	if second != nil {
		_ = second.Close(context.Background())
	}
	if !errors.Is(err, ErrAlreadyServing) {
		t.Fatalf("got %v want ErrAlreadyServing", err)
	}
}

func TestRuntimeCloseRemovesRegistrationAndSocket(t *testing.T) {
	opts := newRuntimeOptions(t)
	runtime, err := Start(opts)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	for _, path := range []string{RegistrationPath(opts.StateRoot, opts.CommunityName), opts.SocketPath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("path still exists after close %s: %v", path, err)
		}
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestRuntimeReplacesStaleSocketAndRegistration(t *testing.T) {
	opts := newRuntimeOptions(t)
	if err := os.MkdirAll(filepath.Dir(opts.SocketPath), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	if err := os.WriteFile(opts.SocketPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale socket: %v", err)
	}
	registrationPath := RegistrationPath(opts.StateRoot, opts.CommunityName)
	if err := os.MkdirAll(filepath.Dir(registrationPath), 0o700); err != nil {
		t.Fatalf("mkdir registration dir: %v", err)
	}
	if err := os.WriteFile(registrationPath, []byte(`{"instance_id":"stale"}`), 0o600); err != nil {
		t.Fatalf("write stale registration: %v", err)
	}

	runtime, err := Start(opts)
	if err != nil {
		t.Fatalf("start with stale state: %v", err)
	}
	defer runtime.Close(context.Background())
	if _, err := LoadRegistration(opts.StateRoot, opts.CommunityName); err != nil {
		t.Fatalf("load replacement registration: %v", err)
	}
}

func TestRuntimeValidatesPublicURL(t *testing.T) {
	tests := []struct {
		name      string
		publicURL string
		allowHTTP bool
		wantError bool
	}{
		{name: "https", publicURL: "https://community.example"},
		{name: "http rejected", publicURL: "http://community.example", wantError: true},
		{name: "loopback http dev", publicURL: "http://127.0.0.1:8080", allowHTTP: true},
		{name: "non-loopback http dev rejected", publicURL: "http://community.example", allowHTTP: true, wantError: true},
		{name: "credentials rejected", publicURL: "https://user:pass@community.example", wantError: true},
		{name: "path rejected", publicURL: "https://community.example/base", wantError: true},
		{name: "query rejected", publicURL: "https://community.example?x=1", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := newRuntimeOptions(t)
			opts.PublicURL = tt.publicURL
			opts.AllowLoopbackHTTP = tt.allowHTTP
			runtime, err := Start(opts)
			if runtime != nil {
				_ = runtime.Close(context.Background())
			}
			if tt.wantError && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadRegistrationRejectsUnknownFields(t *testing.T) {
	stateRoot := t.TempDir()
	path := RegistrationPath(stateRoot, "test-community")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := map[string]any{"instance_id": "x", "unknown": true}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadRegistration(stateRoot, "test-community"); err == nil {
		t.Fatal("expected strict registration error")
	}
}

func assertRuntimeMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("mode %s: got %#o want %#o", path, got, want)
	}
}
