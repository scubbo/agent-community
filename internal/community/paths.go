package community

import (
	"os"
	"path/filepath"
)

// MarkerDir is the directory placed inside a workspace to mark it as a member
// of a community. Mirrors how .git marks a git repository.
const MarkerDir = ".agent-community"

// CommunityFile, inside MarkerDir, holds the community name the workspace
// belongs to. One line, no trailing whitespace.
const CommunityFile = "community"

// IdentityFile, inside MarkerDir, holds the chosen agent name for this
// workspace within that community. One line.
const IdentityFile = "identity"

// EnvVar overrides the workspace-based community resolution.
const EnvVar = "AGENT_COMMUNITY"

// DataHome returns the base directory under which communities are stored by
// default. Honors XDG_DATA_HOME; falls back to ~/.local/share.
func DataHome() (string, error) {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "agent-community"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "agent-community"), nil
}

// StateHome returns the base directory for state (registry, etc.). Honors
// XDG_STATE_HOME; falls back to ~/.local/state.
func StateHome() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "agent-community"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "agent-community"), nil
}

// DefaultCommunityPath returns the default on-disk location for a community
// of the given name. Used when --path is not supplied to `init`.
func DefaultCommunityPath(name string) (string, error) {
	root, err := DataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// RegistryPath is where the community-name -> path map lives.
func RegistryPath() (string, error) {
	root, err := StateHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "registry.toml"), nil
}
