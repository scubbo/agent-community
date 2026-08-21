package connection

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveLoadDeleteConnection(t *testing.T) {
	stateRoot := t.TempDir()
	connection := Connection{
		Name:          "interview-01k3",
		CommunityName: "test-community",
		LocalURL:      "http://127.0.0.1:7337",
		PublicURL:     "https://community.example",
		DiscussionID:  "01K3DUMMYDISCUSSION0000000",
		ParticipantID: "interviewer",
		Capability:    "acp_key.secret",
		ExpiresAt:     time.Date(2026, 8, 19, 12, 30, 0, 0, time.UTC),
	}
	if err := Save(stateRoot, connection); err != nil {
		t.Fatalf("save: %v", err)
	}
	path := Path(stateRoot, connection.CommunityName, connection.Name)
	assertConnectionMode(t, filepath.Dir(filepath.Dir(path)), 0o700)
	assertConnectionMode(t, filepath.Dir(path), 0o700)
	assertConnectionMode(t, path, 0o600)

	loaded, err := Load(stateRoot, connection.CommunityName, connection.Name)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if *loaded != connection {
		t.Errorf("loaded: got %#v want %#v", loaded, connection)
	}

	if err := Delete(stateRoot, connection.CommunityName, connection.Name); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := Load(stateRoot, connection.CommunityName, connection.Name); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load after delete: got %v want os.ErrNotExist", err)
	}
	if err := Delete(stateRoot, connection.CommunityName, connection.Name); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
}

func TestSaveRefusesOverwrite(t *testing.T) {
	stateRoot := t.TempDir()
	connection := validConnection()
	if err := Save(stateRoot, connection); err != nil {
		t.Fatalf("save: %v", err)
	}
	connection.Capability = "different"
	if err := Save(stateRoot, connection); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("got %v want ErrAlreadyExists", err)
	}
}

func TestSaveAndLoadValidateConnection(t *testing.T) {
	tests := []struct {
		name       string
		connection Connection
	}{
		{name: "bad name", connection: withConnection(func(c *Connection) { c.Name = "../escape" })},
		{name: "bad community", connection: withConnection(func(c *Connection) { c.CommunityName = "bad/community" })},
		{name: "bad local url", connection: withConnection(func(c *Connection) { c.LocalURL = "https://remote.example" })},
		{name: "bad public url", connection: withConnection(func(c *Connection) { c.PublicURL = "http://remote.example" })},
		{name: "missing capability", connection: withConnection(func(c *Connection) { c.Capability = "" })},
		{name: "missing expiry", connection: withConnection(func(c *Connection) { c.ExpiresAt = time.Time{} })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Save(t.TempDir(), tt.connection); !errors.Is(err, ErrInvalidConnection) {
				t.Fatalf("got %v want ErrInvalidConnection", err)
			}
		})
	}
}

func TestSaveAllowsLoopbackHTTPPublicURLForDevelopment(t *testing.T) {
	connection := validConnection()
	connection.PublicURL = "http://127.0.0.1:8080"
	if err := Save(t.TempDir(), connection); err != nil {
		t.Fatalf("save loopback HTTP connection: %v", err)
	}
}

func TestLoadFailsStrictlyOnUnknownFields(t *testing.T) {
	stateRoot := t.TempDir()
	path := Path(stateRoot, "test-community", "interview-01k3")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"name":"interview-01k3","unknown":true}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(stateRoot, "test-community", "interview-01k3")
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("got %v want strict path-specific error", err)
	}
}

func TestNewNameUsesDiscussionPrefix(t *testing.T) {
	name, err := NewName("01K3DUMMYDISCUSSION0000000")
	if err != nil {
		t.Fatalf("new name: %v", err)
	}
	if !strings.HasPrefix(name, "interview-") || len(name) <= len("interview-") {
		t.Errorf("unexpected name %q", name)
	}
}

func validConnection() Connection {
	return Connection{
		Name:          "interview-01k3",
		CommunityName: "test-community",
		LocalURL:      "http://127.0.0.1:7337",
		PublicURL:     "https://community.example",
		DiscussionID:  "01K3DUMMYDISCUSSION0000000",
		ParticipantID: "interviewer",
		Capability:    "acp_key.secret",
		ExpiresAt:     time.Date(2026, 8, 19, 12, 30, 0, 0, time.UTC),
	}
}

func withConnection(change func(*Connection)) Connection {
	connection := validConnection()
	change(&connection)
	return connection
}

func assertConnectionMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("mode %s: got %#o want %#o", path, got, want)
	}
}
