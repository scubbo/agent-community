package watch

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/scubbo/agent-community/internal/message"
)

// syncBuffer is a thread-safe buffer with a "wait for content" primitive.
// The watch test posts one message and asserts that the watcher emits it
// promptly; using a plain bytes.Buffer would race with the writer goroutine.
type syncBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	cond *sync.Cond
}

func newSyncBuffer() *syncBuffer {
	sb := &syncBuffer{}
	sb.cond = sync.NewCond(&sb.mu)
	return sb
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.buf.Write(p)
	s.cond.Broadcast()
	return n, err
}

func (s *syncBuffer) waitFor(substr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	s.mu.Lock()
	defer s.mu.Unlock()
	for !strings.Contains(s.buf.String(), substr) {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		done := make(chan struct{})
		go func() {
			time.Sleep(remaining)
			s.cond.Broadcast()
			close(done)
		}()
		s.cond.Wait()
		select {
		case <-done:
			return strings.Contains(s.buf.String(), substr)
		default:
		}
	}
	return true
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, s.buf.Len())
	copy(out, s.buf.Bytes())
	return out
}

var _ io.Writer = (*syncBuffer)(nil)

// TestRun_NoPenultimateLag is the critical regression test. The prototype's
// tail-pipe approach surfaced message N only when message N+1 arrived; this
// must not happen here. We post exactly one message after watch starts and
// expect to see it within a short timeout.
func TestRun_NoPenultimateLag(t *testing.T) {
	dir := t.TempDir()
	out := newSyncBuffer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{CommunityRoot: dir}, out)
	}()

	time.Sleep(100 * time.Millisecond) // let the watcher seek-to-end + arm fsnotify

	m, err := message.New("brioche", "are you there?", "")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := message.Append(dir, m); err != nil {
		t.Fatalf("append: %v", err)
	}

	if !out.waitFor("are you there?", 2*time.Second) {
		t.Fatalf("watcher did not emit within 2s; output so far: %q", out.String())
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Run did not return after cancel")
	}
}

func TestRun_ExcludesSelfAuthor(t *testing.T) {
	dir := t.TempDir()
	out := newSyncBuffer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = Run(ctx, Options{CommunityRoot: dir, ExcludeAuthor: "brie"}, out)
	}()
	time.Sleep(100 * time.Millisecond)

	mine, _ := message.New("brie", "from me", "")
	theirs, _ := message.New("brioche", "from them", "")
	if err := message.Append(dir, mine); err != nil {
		t.Fatal(err)
	}
	if err := message.Append(dir, theirs); err != nil {
		t.Fatal(err)
	}

	if !out.waitFor("from them", 2*time.Second) {
		t.Fatalf("did not see other-author message: %q", out.String())
	}
	if strings.Contains(out.String(), "from me") {
		t.Errorf("own message leaked through filter: %q", out.String())
	}
}

func TestRun_JSONLineFormat(t *testing.T) {
	dir := t.TempDir()
	out := newSyncBuffer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = Run(ctx, Options{CommunityRoot: dir, Format: JSONLine}, out)
	}()
	time.Sleep(100 * time.Millisecond)

	m, _ := message.New("brie", "json line", "")
	if err := message.Append(dir, m); err != nil {
		t.Fatal(err)
	}

	if !out.waitFor(`"body":"json line"`, 2*time.Second) {
		t.Fatalf("did not emit JSONL: %q", out.String())
	}
}

func TestRun_HandlesPartialLines(t *testing.T) {
	// Append a partial line (no trailing newline), then complete it. The
	// watcher must wait for the newline before decoding, not emit the
	// partial as a malformed message.
	dir := t.TempDir()
	out := newSyncBuffer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Run(ctx, Options{CommunityRoot: dir}, out)
	}()
	time.Sleep(100 * time.Millisecond)

	m, _ := message.New("brioche", "split-write", "")
	enc, _ := m.Encode()
	enc = append(enc, '\n')

	path := message.MessagesPath(dir)
	if err := appendBytes(path, enc[:len(enc)/2]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if strings.Contains(out.String(), "split-write") {
		t.Errorf("emitted partial line: %q", out.String())
	}
	if err := appendBytes(path, enc[len(enc)/2:]); err != nil {
		t.Fatal(err)
	}
	if !out.waitFor("split-write", 2*time.Second) {
		t.Fatalf("did not emit completed message: %q", out.String())
	}
}

func appendBytes(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(b)
	return err
}
