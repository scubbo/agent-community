package community

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
)

const ConfigFile = "config.toml"

// Config is the mechanical configuration of a community. Cultural guidance
// lives in README.md alongside this file, not here.
type Config struct {
	Community CommunitySection `toml:"community"`
	Naming    NamingSection    `toml:"naming"`
}

type CommunitySection struct {
	Name    string `toml:"name"`
	Created string `toml:"created"`
}

type NamingSection struct {
	MaxLength int `toml:"max_length"`
}

func ReadConfig(communityRoot string) (*Config, error) {
	path := filepath.Join(communityRoot, ConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if _, err := toml.Decode(string(data), &c); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return &c, nil
}

func WriteConfig(communityRoot string, c *Config) error {
	if err := os.MkdirAll(communityRoot, 0o755); err != nil {
		return err
	}
	path := filepath.Join(communityRoot, ConfigFile)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

func NewConfig(name string) *Config {
	return &Config{
		Community: CommunitySection{
			Name:    name,
			Created: time.Now().UTC().Format(time.RFC3339),
		},
		Naming: NamingSection{
			MaxLength: 0, // 0 = no limit
		},
	}
}

// validateName checks that a community name is safe for use as a directory
// component and as a registry key.
func validateName(name string) error {
	if name == "" {
		return errors.New("community name must not be empty")
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return fmt.Errorf("community name %q contains invalid character %q (use letters, digits, - or _)", name, r)
		}
	}
	return nil
}
