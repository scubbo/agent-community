package community

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNoCommunity is returned when the resolver cannot determine which
// community a caller belongs to. CLI commands translate this into exit code 4
// with a helpful suggestion.
var ErrNoCommunity = errors.New("no community resolved; set " + EnvVar + " or run `agent-community join <name>` in this workspace")

// Resolved is the outcome of community resolution for the current call. It
// includes both the name (from env var or marker file) and the on-disk root
// path (from the registry).
type Resolved struct {
	Name      string
	Root      string
	Workspace string // The workspace where the marker was found; empty if env var resolved.
	Source    Source
}

type Source int

const (
	SourceEnv Source = iota + 1
	SourceMarker
)

// Resolve finds the active community for a given workspace.
//
// Order:
//  1. $AGENT_COMMUNITY environment variable
//  2. Walk up from `workspace` (or CWD if empty) looking for .agent-community/community
//
// The returned Root is looked up in the registry. If a name is set but the
// registry has no entry, an error is returned -- this means the user named a
// community that doesn't exist on this machine.
func Resolve(workspace string) (*Resolved, error) {
	if workspace == "" {
		w, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		workspace = w
	}

	if name := strings.TrimSpace(os.Getenv(EnvVar)); name != "" {
		root, err := lookupRoot(name)
		if err != nil {
			return nil, err
		}
		return &Resolved{Name: name, Root: root, Source: SourceEnv}, nil
	}

	markerWorkspace, name, err := findMarker(workspace)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, ErrNoCommunity
	}
	root, err := lookupRoot(name)
	if err != nil {
		return nil, err
	}
	return &Resolved{
		Name:      name,
		Root:      root,
		Workspace: markerWorkspace,
		Source:    SourceMarker,
	}, nil
}

// findMarker walks up from `start` looking for a .agent-community/community
// marker. Returns the workspace path (where the marker was found), the
// community name from the file, and any error.
//
// Returns empty strings (and nil error) if no marker is found.
func findMarker(start string) (workspace, name string, err error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return "", "", err
	}
	for {
		marker := filepath.Join(dir, MarkerDir, CommunityFile)
		data, readErr := os.ReadFile(marker)
		if readErr == nil {
			return dir, strings.TrimSpace(string(data)), nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", "", readErr
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", nil
		}
		dir = parent
	}
}

func lookupRoot(name string) (string, error) {
	reg, err := LoadRegistry()
	if err != nil {
		return "", err
	}
	entry, ok := reg.Lookup(name)
	if !ok {
		return "", fmt.Errorf("community %q is not registered on this machine; run `agent-community init %s` or `agent-community list` to see available", name, name)
	}
	return entry.Path, nil
}

// FindWorkspaceMarker returns the workspace directory containing a marker for
// the given starting path, or empty string if none found. Used by `join` and
// `claim` to locate the right workspace to write to.
func FindWorkspaceMarker(start string) (string, error) {
	w, _, err := findMarker(start)
	return w, err
}
