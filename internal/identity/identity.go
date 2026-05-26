// Package identity manages per-workspace agent names within a community.
//
// One workspace claims one name within one community. The choice is
// persisted to <workspace>/.agent-community/identity, and an audit record is
// appended to the community's identities.jsonl.
package identity

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackjackson/agent-community/internal/community"
	"github.com/jackjackson/agent-community/internal/message"
)

// ErrNoIdentity is returned when the workspace has joined a community but
// hasn't yet claimed a name.
var ErrNoIdentity = errors.New("no identity claimed for this workspace; run `agent-community claim <name>`")

// Claim is one entry in identities.jsonl.
type Claim struct {
	Name      string    `json:"name"`
	Workspace string    `json:"workspace"`
	Timestamp time.Time `json:"ts"`
}

const identitiesFile = "identities.jsonl"

// Whoami returns the name claimed for the given workspace. Returns
// ErrNoIdentity if no identity file exists.
func Whoami(workspace string) (string, error) {
	path := filepath.Join(workspace, community.MarkerDir, community.IdentityFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNoIdentity
	}
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(data))
	if name == "" {
		return "", ErrNoIdentity
	}
	return name, nil
}

// ClaimOptions captures the parameters of `agent-community claim`.
type ClaimOptions struct {
	Name          string
	Workspace     string // Workspace where .agent-community/identity will be written.
	CommunityRoot string // Community root where identities.jsonl lives.
	MaxLength     int    // From config; 0 = no limit.
}

// ClaimName performs uniqueness checks and writes the workspace identity file
// + appends to identities.jsonl. Returns the existing claimant's workspace
// path if the name is taken.
func ClaimName(opts ClaimOptions) error {
	if err := validateAgentName(opts.Name, opts.MaxLength); err != nil {
		return err
	}

	workspace, err := filepath.Abs(opts.Workspace)
	if err != nil {
		return err
	}

	existing, err := findClaim(opts.CommunityRoot, opts.Name)
	if err != nil {
		return err
	}
	if existing != nil && existing.Workspace != workspace {
		return fmt.Errorf("name %q is already claimed by workspace %s (since %s)", opts.Name, existing.Workspace, existing.Timestamp.Format(time.RFC3339))
	}

	identityPath := filepath.Join(workspace, community.MarkerDir, community.IdentityFile)
	if err := os.MkdirAll(filepath.Dir(identityPath), 0o755); err != nil {
		return err
	}
	if existingBytes, _ := os.ReadFile(identityPath); existingBytes != nil {
		curr := strings.TrimSpace(string(existingBytes))
		if curr != "" && curr != opts.Name {
			return fmt.Errorf("workspace %s already claimed name %q; remove %s to re-claim", workspace, curr, identityPath)
		}
	}
	if err := os.WriteFile(identityPath, []byte(opts.Name+"\n"), 0o644); err != nil {
		return err
	}

	if existing == nil {
		return appendClaim(opts.CommunityRoot, Claim{
			Name:      opts.Name,
			Workspace: workspace,
			Timestamp: time.Now().UTC(),
		})
	}
	return nil
}

// findClaim returns the first identities.jsonl entry matching the given
// name, or nil if no such claim exists. Also checks messages.jsonl for an
// author of that name (as a backstop -- old logs predating identities.jsonl).
func findClaim(communityRoot, name string) (*Claim, error) {
	path := filepath.Join(communityRoot, identitiesFile)
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var c Claim
			if err := json.Unmarshal(line, &c); err != nil {
				continue
			}
			if c.Name == name {
				return &c, nil
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	msgs, err := message.ReadAll(communityRoot, message.ReadOptions{})
	if err != nil {
		return nil, err
	}
	for _, m := range msgs {
		if m.Author == name {
			return &Claim{Name: name, Workspace: "(historical; pre-identities.jsonl)", Timestamp: m.Timestamp}, nil
		}
	}
	return nil, nil
}

func appendClaim(communityRoot string, c Claim) error {
	path := filepath.Join(communityRoot, identitiesFile)
	if err := os.MkdirAll(communityRoot, 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(c)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

func validateAgentName(name string, maxLength int) error {
	if name == "" {
		return errors.New("name must not be empty")
	}
	if strings.ContainsAny(name, " \t\r\n|") {
		return fmt.Errorf("name %q contains whitespace or pipe (reserved)", name)
	}
	if maxLength > 0 && len(name) > maxLength {
		return fmt.Errorf("name %q exceeds community max length %d", name, maxLength)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("name %q contains control character", name)
		}
	}
	return nil
}
