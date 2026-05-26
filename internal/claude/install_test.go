package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestInstall_ExtractsBundle(t *testing.T) {
	dir := t.TempDir()
	dest, err := Install(InstallOptions{TargetDir: dir})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if dest != filepath.Join(dir, PluginDirName) {
		t.Errorf("unexpected dest: %s", dest)
	}

	want := []string{
		".claude-plugin/plugin.json",
		"skills/post/SKILL.md",
		"skills/read/SKILL.md",
		"skills/claim/SKILL.md",
		"monitors/monitors.json",
		"hooks/hooks.json",
	}
	for _, rel := range want {
		path := filepath.Join(dest, rel)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("missing %s after install: %v", rel, err)
		}
	}

	// plugin.json must be valid JSON
	data, err := os.ReadFile(filepath.Join(dest, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatalf("read plugin.json: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Errorf("plugin.json invalid JSON: %v", err)
	}
	if manifest["name"] != PluginDirName {
		t.Errorf("manifest name mismatch: %v vs %s", manifest["name"], PluginDirName)
	}
}

func TestInstall_RefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	if _, err := Install(InstallOptions{TargetDir: dir}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if _, err := Install(InstallOptions{TargetDir: dir}); err == nil {
		t.Fatal("expected second install without --force to fail")
	}
	if _, err := Install(InstallOptions{TargetDir: dir, Force: true}); err != nil {
		t.Errorf("force install failed: %v", err)
	}
}
