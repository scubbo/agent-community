package message

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAppendAndReadAll_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{"first", "second", "third"} {
		m, err := New("brie", body, "")
		if err != nil {
			t.Fatalf("new %q: %v", body, err)
		}
		if err := Append(dir, m); err != nil {
			t.Fatalf("append %q: %v", body, err)
		}
	}

	got, err := ReadAll(dir, ReadOptions{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	wantBodies := []string{"first", "second", "third"}
	for i, m := range got {
		if m.Body != wantBodies[i] {
			t.Errorf("message %d body: got %q want %q", i, m.Body, wantBodies[i])
		}
		if m.Author != "brie" {
			t.Errorf("message %d author: got %q", i, m.Author)
		}
		if m.ID == "" {
			t.Errorf("message %d missing id", i)
		}
	}
}

func TestNew_RejectsNewlines(t *testing.T) {
	if _, err := New("brie", "line1\nline2", ""); err == nil {
		t.Error("expected error for newline in body")
	}
	if _, err := New("brie", "ok", "line1\nline2"); err == nil {
		t.Error("expected error for newline in context")
	}
}

func TestNew_RejectsEmpty(t *testing.T) {
	if _, err := New("", "body", ""); err == nil {
		t.Error("expected error for empty author")
	}
	if _, err := New("brie", "", ""); err == nil {
		t.Error("expected error for empty body")
	}
}

func TestReadAll_LimitReturnsMostRecent(t *testing.T) {
	dir := t.TempDir()
	for i, body := range []string{"a", "b", "c", "d", "e"} {
		_ = i
		m, _ := New("x", body, "")
		if err := Append(dir, m); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	got, err := ReadAll(dir, ReadOptions{Limit: 2})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 with limit, got %d", len(got))
	}
	if got[0].Body != "d" || got[1].Body != "e" {
		t.Errorf("limit returned wrong messages: %q %q", got[0].Body, got[1].Body)
	}
}

// TestAppend_ConcurrentWrites verifies that multiple concurrent Append calls
// each produce exactly one well-formed line. With O_APPEND on macOS/Linux,
// writes shorter than PIPE_BUF (4KB) are atomic; this test exercises that.
func TestAppend_ConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			body := strings.Repeat("x", 100) + "-" + itoa(i)
			m, _ := New("a"+itoa(i%5), body, "")
			if err := Append(dir, m); err != nil {
				t.Errorf("append %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	f, err := os.Open(filepath.Join(dir, MessagesFile))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 65536), 1<<20)
	lines := 0
	for scanner.Scan() {
		lines++
		line := scanner.Bytes()
		if _, err := Decode(line); err != nil {
			t.Errorf("line %d not valid JSONL: %v\n%s", lines, err, string(line))
		}
	}
	if lines != N {
		t.Errorf("expected %d lines, got %d", N, lines)
	}
}

// itoa avoids the strconv import to keep this test file self-contained for
// the message-format invariants it's exercising.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
