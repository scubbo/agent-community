package community

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempHome redirects XDG_DATA_HOME and XDG_STATE_HOME into the test's
// scratch directory so tests don't pollute the user's real state.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv(EnvVar, "")
	return dir
}

func TestResolve_EnvVarOverride(t *testing.T) {
	withTempHome(t)
	if _, err := Init(InitOptions{Name: "alpha"}); err != nil {
		t.Fatalf("init alpha: %v", err)
	}
	if _, err := Init(InitOptions{Name: "beta"}); err != nil {
		t.Fatalf("init beta: %v", err)
	}

	// Workspace joined to alpha.
	ws := t.TempDir()
	if _, err := Join(JoinOptions{Name: "alpha", Workspace: ws}); err != nil {
		t.Fatalf("join: %v", err)
	}

	// Env var overrides to beta.
	t.Setenv(EnvVar, "beta")
	got, err := Resolve(ws)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Name != "beta" {
		t.Errorf("expected env var override to win, got %q", got.Name)
	}
	if got.Source != SourceEnv {
		t.Errorf("expected SourceEnv, got %v", got.Source)
	}
}

func TestResolve_MarkerWalkUp(t *testing.T) {
	withTempHome(t)
	if _, err := Init(InitOptions{Name: "alpha"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	root := t.TempDir()
	if _, err := Join(JoinOptions{Name: "alpha", Workspace: root}); err != nil {
		t.Fatalf("join: %v", err)
	}
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir deep: %v", err)
	}
	got, err := Resolve(deep)
	if err != nil {
		t.Fatalf("resolve from deep: %v", err)
	}
	if got.Name != "alpha" {
		t.Errorf("expected name alpha, got %q", got.Name)
	}
	if got.Source != SourceMarker {
		t.Errorf("expected SourceMarker, got %v", got.Source)
	}
	absRoot, _ := filepath.Abs(root)
	if got.Workspace != absRoot {
		t.Errorf("expected workspace %s, got %s", absRoot, got.Workspace)
	}
}

func TestResolve_NoCommunity(t *testing.T) {
	withTempHome(t)
	dir := t.TempDir()
	_, err := Resolve(dir)
	if err == nil {
		t.Fatal("expected ErrNoCommunity, got nil")
	}
	// We can't use errors.Is because lookupRoot may wrap differently;
	// check both the canonical sentinel and the registry-miss message.
	// In this case there's no marker at all, so ErrNoCommunity is returned.
	if err.Error() != ErrNoCommunity.Error() {
		t.Errorf("expected ErrNoCommunity, got: %v", err)
	}
}

func TestResolve_EnvVarUnknownCommunity(t *testing.T) {
	withTempHome(t)
	t.Setenv(EnvVar, "ghost")
	dir := t.TempDir()
	_, err := Resolve(dir)
	if err == nil {
		t.Fatal("expected error for unknown community, got nil")
	}
}

func TestInit_DuplicateRejected(t *testing.T) {
	withTempHome(t)
	if _, err := Init(InitOptions{Name: "alpha"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	if _, err := Init(InitOptions{Name: "alpha"}); err == nil {
		t.Fatal("expected duplicate init to fail")
	}
}

func TestJoin_UnknownCommunityRejected(t *testing.T) {
	withTempHome(t)
	if _, err := Join(JoinOptions{Name: "nope", Workspace: t.TempDir()}); err == nil {
		t.Fatal("expected join of unknown community to fail")
	}
}

func TestJoin_GitignoreUpdated(t *testing.T) {
	withTempHome(t)
	if _, err := Init(InitOptions{Name: "alpha"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	ws := t.TempDir()
	if err := os.Mkdir(filepath.Join(ws, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if _, err := Join(JoinOptions{Name: "alpha", Workspace: ws}); err != nil {
		t.Fatalf("join: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(ws, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	want := MarkerDir + "/" + IdentityFile
	if !contains(string(data), want) {
		t.Errorf(".gitignore missing %q; got %q", want, string(data))
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > 0 && (indexOf(s, sub) >= 0)))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
