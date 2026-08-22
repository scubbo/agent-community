package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestModuleUsesCanonicalRepositoryPath(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	if !strings.HasPrefix(string(data), "module github.com/scubbo/agent-community\n") {
		t.Fatalf("unexpected module declaration: %s", strings.SplitN(string(data), "\n", 2)[0])
	}
}
