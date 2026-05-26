package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackjackson/agent-community/internal/community"
)

func setupCommunity(t *testing.T) (communityRoot string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv(community.EnvVar, "")
	root, err := community.Init(community.InitOptions{Name: "testc"})
	if err != nil {
		t.Fatalf("init community: %v", err)
	}
	return root
}

func TestClaim_HappyPath(t *testing.T) {
	root := setupCommunity(t)
	ws := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ws, community.MarkerDir), 0o755); err != nil {
		t.Fatalf("mkdir marker: %v", err)
	}
	if err := ClaimName(ClaimOptions{Name: "brie", Workspace: ws, CommunityRoot: root}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	got, err := Whoami(ws)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if got != "brie" {
		t.Errorf("got %q want brie", got)
	}
}

func TestClaim_UniquenessRejected(t *testing.T) {
	root := setupCommunity(t)
	ws1 := t.TempDir()
	ws2 := t.TempDir()
	if err := ClaimName(ClaimOptions{Name: "brie", Workspace: ws1, CommunityRoot: root}); err != nil {
		t.Fatalf("claim ws1: %v", err)
	}
	err := ClaimName(ClaimOptions{Name: "brie", Workspace: ws2, CommunityRoot: root})
	if err == nil {
		t.Fatal("expected duplicate claim to fail")
	}
	if !contains(err.Error(), ws1) {
		t.Errorf("expected error to name the existing claimant workspace; got: %v", err)
	}
}

func TestClaim_ReclaimSameNameSameWorkspaceOK(t *testing.T) {
	root := setupCommunity(t)
	ws := t.TempDir()
	if err := ClaimName(ClaimOptions{Name: "brie", Workspace: ws, CommunityRoot: root}); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := ClaimName(ClaimOptions{Name: "brie", Workspace: ws, CommunityRoot: root}); err != nil {
		t.Errorf("re-claiming same name from same workspace should be allowed, got: %v", err)
	}
}

func TestClaim_MaxLength(t *testing.T) {
	root := setupCommunity(t)
	ws := t.TempDir()
	err := ClaimName(ClaimOptions{Name: "verylongname", Workspace: ws, CommunityRoot: root, MaxLength: 7})
	if err == nil {
		t.Fatal("expected MaxLength violation, got nil")
	}
}

func TestWhoami_NoIdentity(t *testing.T) {
	ws := t.TempDir()
	_, err := Whoami(ws)
	if err != ErrNoIdentity {
		t.Errorf("expected ErrNoIdentity, got %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
