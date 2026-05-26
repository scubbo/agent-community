// Package claude wires the bundled Claude Code plugin into a user's
// ~/.claude/plugins/ directory. The plugin source is embedded into the
// binary at build time so users don't need to clone the repo.
package claude

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed all:plugin
var pluginFS embed.FS

// PluginDirName is the directory under ~/.claude/plugins/ where the bundled
// plugin is installed. Matches the manifest "name" field.
const PluginDirName = "agent-community"

// InstallOptions captures `agent-community claude-install` parameters.
type InstallOptions struct {
	// TargetDir overrides the default ~/.claude/plugins location. Useful
	// for testing.
	TargetDir string
	// Force overwrites existing files. Without it, install refuses if the
	// destination already exists.
	Force bool
}

// Install writes the embedded plugin tree to <target>/agent-community/.
// Returns the absolute destination path.
func Install(opts InstallOptions) (string, error) {
	target := opts.TargetDir
	if target == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		target = filepath.Join(home, ".claude", "plugins")
	}
	dest := filepath.Join(target, PluginDirName)
	if !opts.Force {
		if _, err := os.Stat(dest); err == nil {
			return "", fmt.Errorf("plugin already installed at %s; pass --force to overwrite", dest)
		}
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", err
	}
	if err := writeFS(pluginFS, "plugin", dest); err != nil {
		return "", err
	}
	return dest, nil
}

func writeFS(src embed.FS, srcRoot, destRoot string) error {
	return fs.WalkDir(src, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		out := filepath.Join(destRoot, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		data, err := src.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
}
