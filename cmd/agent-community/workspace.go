package main

import (
	"os"
	"path/filepath"

	"github.com/scubbo/agent-community/internal/community"
)

// resolveWorkspace returns the directory considered "the workspace" for
// identity purposes. Walks up from start looking for a .agent-community
// marker. If found, returns that directory. Otherwise returns start as-is
// (the env-var-overrides-marker case where the user is operating from a
// directory with no marker of its own).
//
// Without this walk-up, running `agent-community claim` or `whoami` from a
// subdirectory of a joined workspace would look for the identity file in the
// wrong place.
func resolveWorkspace(start string) (string, error) {
	if start == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		start = cwd
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	marker, err := community.FindWorkspaceMarker(abs)
	if err != nil {
		return "", err
	}
	if marker != "" {
		return marker, nil
	}
	return abs, nil
}
