package community

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// Registry maps community names to on-disk root paths. Stored at
// $XDG_STATE_HOME/agent-community/registry.toml (or fallback).
//
// Multiple communities can coexist on one machine. The registry is the
// authoritative answer to "given the name, where does it live?"
type Registry struct {
	Communities map[string]RegistryEntry `toml:"communities"`
}

type RegistryEntry struct {
	Path    string `toml:"path"`
	Created string `toml:"created"`
}

func LoadRegistry() (*Registry, error) {
	path, err := RegistryPath()
	if err != nil {
		return nil, err
	}
	r := &Registry{Communities: map[string]RegistryEntry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), r); err != nil {
		return nil, fmt.Errorf("decode registry: %w", err)
	}
	if r.Communities == nil {
		r.Communities = map[string]RegistryEntry{}
	}
	return r, nil
}

func (r *Registry) Save() error {
	path, err := RegistryPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "registry-*.toml")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()

	if err := toml.NewEncoder(f).Encode(r); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.Communities))
	for n := range r.Communities {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (r *Registry) Lookup(name string) (RegistryEntry, bool) {
	e, ok := r.Communities[name]
	return e, ok
}

func (r *Registry) Add(name string, entry RegistryEntry) error {
	if _, exists := r.Communities[name]; exists {
		return fmt.Errorf("community %q already registered (path %s)", name, r.Communities[name].Path)
	}
	r.Communities[name] = entry
	return nil
}
