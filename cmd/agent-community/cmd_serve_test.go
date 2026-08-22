package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scubbo/agent-community/internal/community"
	"github.com/scubbo/agent-community/internal/communityserver"
)

func TestRunServeStartsAndStopsResolvedCommunity(t *testing.T) {
	base := t.TempDir()
	dataHome := filepath.Join(base, "data")
	stateHome := filepath.Join(base, "state")
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv(community.EnvVar, "")
	root, err := community.Init(community.InitOptions{Name: "remote-test"})
	if err != nil {
		t.Fatalf("init community: %v", err)
	}
	workspace := t.TempDir()
	if _, err := community.Join(community.JoinOptions{Name: "remote-test", Workspace: workspace}); err != nil {
		t.Fatalf("join community: %v", err)
	}
	socketRoot, err := os.MkdirTemp("/tmp", "agent-community-cli-")
	if err != nil {
		t.Fatalf("create socket root: %v", err)
	}
	defer os.RemoveAll(socketRoot)
	socketPath := filepath.Join(socketRoot, "control.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runServe(ctx, []string{
			"--workspace", workspace,
			"--listen", "127.0.0.1:0",
			"--public-url", "http://127.0.0.1:7337",
			"--allow-loopback-http",
			"--control-socket", socketPath,
		}, &output)
	}()

	registrationPath := communityserver.RegistrationPath(filepath.Join(stateHome, "agent-community"), "remote-test")
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(registrationPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("registration not created at %s", registrationPath)
		}
		time.Sleep(10 * time.Millisecond)
	}
	registration, err := communityserver.LoadRegistration(filepath.Join(stateHome, "agent-community"), "remote-test")
	if err != nil {
		t.Fatalf("load registration: %v", err)
	}
	if registration.CommunityRoot != root {
		t.Errorf("community root: got %q want %q", registration.CommunityRoot, root)
	}
	if registration.ControlSecret == "" {
		t.Fatal("registration missing control secret")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run serve: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run serve did not stop after cancellation")
	}
	if _, err := os.Stat(registrationPath); !os.IsNotExist(err) {
		t.Errorf("registration remains after shutdown: %v", err)
	}
	text := output.String()
	if !strings.Contains(text, "Serving community \"remote-test\"") || !strings.Contains(text, registration.LocalURL) || !strings.Contains(text, registration.PublicURL) {
		t.Errorf("unexpected output: %q", text)
	}
	if strings.Contains(text, registration.ControlSecret) {
		t.Error("output contains control secret")
	}
}

func TestRunServeRequiresPublicURL(t *testing.T) {
	var output bytes.Buffer
	err := runServe(context.Background(), nil, &output)
	if err == nil || !strings.Contains(err.Error(), "public-url") {
		t.Fatalf("got %v want public-url error", err)
	}
}
