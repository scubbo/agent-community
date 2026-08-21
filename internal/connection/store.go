// Package connection stores participant credentials for local CLI and MCP
// clients. Server-side discussion metadata stores only capability digests.
package connection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const connectionsDir = "connections"

var (
	ErrAlreadyExists     = errors.New("connection already exists")
	ErrInvalidConnection = errors.New("invalid connection")
)

type Connection struct {
	Name          string    `json:"name"`
	CommunityName string    `json:"community_name"`
	LocalURL      string    `json:"local_url"`
	PublicURL     string    `json:"public_url"`
	DiscussionID  string    `json:"discussion_id"`
	ParticipantID string    `json:"participant_id"`
	Capability    string    `json:"capability"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func Save(stateRoot string, connection Connection) error {
	if err := validate(connection); err != nil {
		return err
	}
	path := Path(stateRoot, connection.CommunityName, connection.Name)
	if err := secureDirectory(filepath.Dir(filepath.Dir(path))); err != nil {
		return err
	}
	if err := secureDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(connection, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return ErrAlreadyExists
	}
	if err != nil {
		return err
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	removeOnError = false
	return syncDirectory(filepath.Dir(path))
}

func Load(stateRoot, communityName, name string) (*Connection, error) {
	if !validName(communityName) || !validName(name) {
		return nil, ErrInvalidConnection
	}
	path := Path(stateRoot, communityName, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var connection Connection
	if err := decodeStrict(data, &connection); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := validate(connection); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	if connection.CommunityName != communityName || connection.Name != name {
		return nil, fmt.Errorf("validate %s: connection identity does not match path", path)
	}
	return &connection, nil
}

func Delete(stateRoot, communityName, name string) error {
	if !validName(communityName) || !validName(name) {
		return ErrInvalidConnection
	}
	err := os.Remove(Path(stateRoot, communityName, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func Path(stateRoot, communityName, name string) string {
	return filepath.Join(stateRoot, connectionsDir, communityName, name+".json")
}

func NewName(discussionID string) (string, error) {
	if len(discussionID) < 8 {
		return "", fmt.Errorf("%w: invalid discussion id", ErrInvalidConnection)
	}
	return "interview-" + strings.ToLower(discussionID[len(discussionID)-8:]), nil
}

func validate(connection Connection) error {
	if !validName(connection.Name) || !validName(connection.CommunityName) {
		return fmt.Errorf("%w: invalid name", ErrInvalidConnection)
	}
	if connection.DiscussionID == "" || !validName(connection.DiscussionID) || !validName(connection.ParticipantID) || connection.Capability == "" || connection.ExpiresAt.IsZero() {
		return fmt.Errorf("%w: required field is missing or invalid", ErrInvalidConnection)
	}
	localURL, err := url.Parse(connection.LocalURL)
	if err != nil || localURL.Scheme != "http" || !isLoopback(localURL.Hostname()) || localURL.User != nil || localURL.RawQuery != "" || localURL.Fragment != "" || (localURL.Path != "" && localURL.Path != "/") {
		return fmt.Errorf("%w: local URL must be loopback HTTP", ErrInvalidConnection)
	}
	publicURL, err := url.Parse(connection.PublicURL)
	validPublicScheme := err == nil && (publicURL.Scheme == "https" || publicURL.Scheme == "http" && isLoopback(publicURL.Hostname()))
	if !validPublicScheme || publicURL.Host == "" || publicURL.User != nil || publicURL.RawQuery != "" || publicURL.Fragment != "" || (publicURL.Path != "" && publicURL.Path != "/") {
		return fmt.Errorf("%w: public URL must be HTTPS without credentials, path, query, or fragment", ErrInvalidConnection)
	}
	return nil
}

func validName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
