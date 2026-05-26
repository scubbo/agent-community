package community

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/jackjackson/agent-community/templates"
)

const (
	MessagesFile   = "messages.jsonl"
	IdentitiesFile = "identities.jsonl"
	ReadmeFile     = "README.md"
)

// InitOptions captures the parameters of `agent-community init`.
type InitOptions struct {
	Name  string
	Path  string // If empty, DefaultCommunityPath(name) is used.
	Theme string // If empty, "default".
}

// Init creates a new community on disk and registers it. Returns the absolute
// path of the created community root.
func Init(opts InitOptions) (string, error) {
	if err := validateName(opts.Name); err != nil {
		return "", err
	}
	if opts.Theme == "" {
		opts.Theme = "default"
	}

	root := opts.Path
	if root == "" {
		var err error
		root, err = DefaultCommunityPath(opts.Name)
		if err != nil {
			return "", err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	reg, err := LoadRegistry()
	if err != nil {
		return "", err
	}
	if _, exists := reg.Lookup(opts.Name); exists {
		return "", fmt.Errorf("community %q is already registered", opts.Name)
	}

	if _, statErr := os.Stat(root); statErr == nil {
		entries, _ := os.ReadDir(root)
		if len(entries) > 0 {
			return "", fmt.Errorf("path %s already exists and is not empty", root)
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}

	cfg := NewConfig(opts.Name)
	if err := WriteConfig(root, cfg); err != nil {
		return "", err
	}

	readme, err := renderReadme(opts.Theme, opts.Name)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(root, ReadmeFile), readme, 0o644); err != nil {
		return "", err
	}
	for _, f := range []string{MessagesFile, IdentitiesFile} {
		if err := os.WriteFile(filepath.Join(root, f), nil, 0o644); err != nil {
			return "", err
		}
	}

	if err := reg.Add(opts.Name, RegistryEntry{
		Path:    root,
		Created: cfg.Community.Created,
	}); err != nil {
		return "", err
	}
	if err := reg.Save(); err != nil {
		return "", err
	}
	return root, nil
}

func renderReadme(theme, name string) ([]byte, error) {
	tmplBytes, err := templates.ReadmeTemplate(theme)
	if err != nil {
		return nil, err
	}
	t, err := template.New("readme").Parse(string(tmplBytes))
	if err != nil {
		return nil, fmt.Errorf("parse readme template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, map[string]string{
		"CommunityName": name,
	}); err != nil {
		return nil, fmt.Errorf("execute readme template: %w", err)
	}
	out := buf.Bytes()
	if !bytes.HasSuffix(out, []byte("\n")) {
		out = append(out, '\n')
	}
	return out, nil
}

// AvailableThemes returns the names of bundled README themes.
func AvailableThemes() []string {
	all := templates.ReadmeThemes()
	out := make([]string, 0, len(all))
	for _, t := range all {
		out = append(out, strings.TrimSuffix(t, ".md.tmpl"))
	}
	return out
}
