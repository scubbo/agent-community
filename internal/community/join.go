package community

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// JoinOptions captures `agent-community join` parameters.
type JoinOptions struct {
	Name      string
	Workspace string // If empty, current working directory is used.
}

// Join marks the workspace as belonging to the named community. The community
// must already exist (be in the registry).
//
// Writes <workspace>/.agent-community/community with the community name and
// updates <workspace>/.gitignore (if one exists) so the per-developer
// identity file is not accidentally committed.
func Join(opts JoinOptions) (workspace string, err error) {
	if err := validateName(opts.Name); err != nil {
		return "", err
	}
	reg, err := LoadRegistry()
	if err != nil {
		return "", err
	}
	if _, ok := reg.Lookup(opts.Name); !ok {
		return "", fmt.Errorf("community %q is not registered on this machine; run `agent-community init %s` first", opts.Name, opts.Name)
	}

	if opts.Workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		opts.Workspace = cwd
	}
	workspace, err = filepath.Abs(opts.Workspace)
	if err != nil {
		return "", err
	}

	markerDir := filepath.Join(workspace, MarkerDir)
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		return "", err
	}

	existing, _ := os.ReadFile(filepath.Join(markerDir, CommunityFile))
	if existing != nil && strings.TrimSpace(string(existing)) != "" && strings.TrimSpace(string(existing)) != opts.Name {
		return "", fmt.Errorf("workspace %s is already joined to community %q; remove %s to re-join", workspace, strings.TrimSpace(string(existing)), filepath.Join(markerDir, CommunityFile))
	}

	if err := os.WriteFile(
		filepath.Join(markerDir, CommunityFile),
		[]byte(opts.Name+"\n"),
		0o644,
	); err != nil {
		return "", err
	}

	if err := ensureGitignored(workspace, MarkerDir+"/"+IdentityFile); err != nil {
		return "", err
	}

	return workspace, nil
}

// ensureGitignored appends `pattern` to <workspace>/.gitignore if the
// workspace is a git repo and the pattern isn't already there. Silent if the
// workspace isn't a git repo at all.
func ensureGitignored(workspace, pattern string) error {
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		return nil
	}
	path := filepath.Join(workspace, ".gitignore")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("open .gitignore: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == pattern {
			return nil
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	prefix := ""
	if info.Size() > 0 {
		buf := make([]byte, 1)
		if _, err := f.ReadAt(buf, info.Size()-1); err == nil && buf[0] != '\n' {
			prefix = "\n"
		}
	}
	if _, err := f.WriteString(prefix + pattern + "\n"); err != nil {
		return err
	}
	return nil
}
