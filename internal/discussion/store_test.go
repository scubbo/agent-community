package discussion

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var testNow = time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	store, err := NewStore(root, StoreOptions{Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store, root
}

func createTestDiscussion(t *testing.T, store *Store) (*Discussion, map[string]string) {
	t.Helper()
	discussion, capabilities, err := store.Create(CreateInput{
		TTL: 30 * time.Minute,
		Participants: []ParticipantInput{
			{ID: "interviewer", Permissions: []Permission{PermissionRead, PermissionPost, PermissionManage}},
			{ID: "goat", Permissions: []Permission{PermissionRead, PermissionPost, PermissionSubscribe}},
		},
	})
	if err != nil {
		t.Fatalf("create discussion: %v", err)
	}
	return discussion, capabilities
}

func TestCreateStoresOnlyCapabilityDigests(t *testing.T) {
	store, root := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)

	if discussion.Status != StatusActive {
		t.Fatalf("status: got %q want %q", discussion.Status, StatusActive)
	}
	if !discussion.CreatedAt.Equal(testNow) {
		t.Errorf("created_at: got %s want %s", discussion.CreatedAt, testNow)
	}
	if !discussion.ExpiresAt.Equal(testNow.Add(30 * time.Minute)) {
		t.Errorf("expires_at: got %s", discussion.ExpiresAt)
	}
	if len(capabilities) != 2 {
		t.Fatalf("capabilities: got %d want 2", len(capabilities))
	}
	for participant, capability := range capabilities {
		if !strings.HasPrefix(capability, "acp_") || !strings.Contains(capability, ".") {
			t.Errorf("%s capability has unexpected format %q", participant, capability)
		}
	}

	dir := filepath.Join(root, DiscussionsDir, discussion.ID)
	metadata, err := os.ReadFile(filepath.Join(dir, DiscussionFile))
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	for participant, capability := range capabilities {
		if strings.Contains(string(metadata), capability) {
			t.Errorf("metadata contains plaintext %s capability", participant)
		}
	}
	if !strings.Contains(string(metadata), "token_digest") {
		t.Error("metadata does not contain capability digest")
	}

	assertMode(t, dir, 0o700)
	assertMode(t, filepath.Join(dir, DiscussionFile), 0o600)
	assertMode(t, filepath.Join(dir, MessagesFile), 0o600)
}

func TestCreateValidatesParticipantsAndTTL(t *testing.T) {
	store, _ := newTestStore(t)
	tests := []struct {
		name  string
		input CreateInput
	}{
		{name: "no participants", input: CreateInput{TTL: time.Minute}},
		{name: "duplicate participant", input: CreateInput{TTL: time.Minute, Participants: []ParticipantInput{{ID: "goat", Permissions: []Permission{PermissionRead}}, {ID: "goat", Permissions: []Permission{PermissionPost}}}}},
		{name: "invalid participant", input: CreateInput{TTL: time.Minute, Participants: []ParticipantInput{{ID: "goat farm", Permissions: []Permission{PermissionRead}}}}},
		{name: "unknown permission", input: CreateInput{TTL: time.Minute, Participants: []ParticipantInput{{ID: "goat", Permissions: []Permission{"own_everything"}}}}},
		{name: "zero ttl", input: CreateInput{Participants: []ParticipantInput{{ID: "goat", Permissions: []Permission{PermissionRead}}}}},
		{name: "ttl too long", input: CreateInput{TTL: 2*time.Hour + time.Second, Participants: []ParticipantInput{{ID: "goat", Permissions: []Permission{PermissionRead}}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := store.Create(tt.input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("got %v want ErrInvalidInput", err)
			}
		})
	}
}

func TestPostDerivesAuthorFromCapabilityAndSupportsMultiline(t *testing.T) {
	store, _ := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)

	message, replayed, err := store.Post(discussion.ID, capabilities["interviewer"], PostInput{
		IdempotencyKey: "question-1",
		Body:           "Why this design?\nPlease explain the tradeoff.",
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if replayed {
		t.Fatal("first post reported as replay")
	}
	if message.Author != "interviewer" {
		t.Errorf("author: got %q want interviewer", message.Author)
	}
	if message.Sequence != 1 {
		t.Errorf("sequence: got %d want 1", message.Sequence)
	}
}

func TestPostRejectsUnauthorizedAndCrossDiscussionCapabilities(t *testing.T) {
	store, _ := newTestStore(t)
	first, firstCapabilities := createTestDiscussion(t, store)
	second, secondCapabilities := createTestDiscussion(t, store)

	_, _, err := store.Post(first.ID, "not-a-token", PostInput{IdempotencyKey: "bad", Body: "bad"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("invalid token: got %v want ErrUnauthorized", err)
	}
	_, _, err = store.Post(first.ID, secondCapabilities["goat"], PostInput{IdempotencyKey: "cross", Body: "bad"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("cross-discussion token: got %v want ErrUnauthorized", err)
	}

	if _, _, err := store.Post(second.ID, firstCapabilities["interviewer"], PostInput{IdempotencyKey: "wrong", Body: "bad"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("wrong discussion: got %v want ErrUnauthorized", err)
	}
}

func TestPostRejectsCapabilityWithoutPostPermission(t *testing.T) {
	store, _ := newTestStore(t)
	discussion, capabilities, err := store.Create(CreateInput{
		TTL:          time.Minute,
		Participants: []ParticipantInput{{ID: "observer", Permissions: []Permission{PermissionRead}}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _, err = store.Post(discussion.ID, capabilities["observer"], PostInput{IdempotencyKey: "denied", Body: "no"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v want ErrForbidden", err)
	}
}

func TestPostIsIdempotentPerParticipant(t *testing.T) {
	store, _ := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	input := PostInput{IdempotencyKey: "question-1", Body: "Why?"}

	first, replayed, err := store.Post(discussion.ID, capabilities["interviewer"], input)
	if err != nil || replayed {
		t.Fatalf("first post: replayed=%v err=%v", replayed, err)
	}
	second, replayed, err := store.Post(discussion.ID, capabilities["interviewer"], input)
	if err != nil || !replayed {
		t.Fatalf("replay: replayed=%v err=%v", replayed, err)
	}
	if second.ID != first.ID || second.Sequence != first.Sequence {
		t.Errorf("replay returned different message: first=%+v second=%+v", first, second)
	}

	_, _, err = store.Post(discussion.ID, capabilities["interviewer"], PostInput{IdempotencyKey: input.IdempotencyKey, Body: "Different"})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting replay: got %v want ErrIdempotencyConflict", err)
	}

	goat, replayed, err := store.Post(discussion.ID, capabilities["goat"], input)
	if err != nil || replayed {
		t.Fatalf("same key from other participant: replayed=%v err=%v", replayed, err)
	}
	if goat.Sequence != 2 {
		t.Errorf("goat sequence: got %d want 2", goat.Sequence)
	}
}

func TestPostValidatesBodyKeyReplyAndMessageLimit(t *testing.T) {
	store, _ := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	token := capabilities["interviewer"]

	tests := []struct {
		name  string
		input PostInput
		err   error
	}{
		{name: "empty body", input: PostInput{IdempotencyKey: "empty"}, err: ErrInvalidInput},
		{name: "empty key", input: PostInput{Body: "body"}, err: ErrInvalidInput},
		{name: "control in key", input: PostInput{IdempotencyKey: "bad\nkey", Body: "body"}, err: ErrInvalidInput},
		{name: "body too large", input: PostInput{IdempotencyKey: "large", Body: strings.Repeat("x", MaxBodyBytes+1)}, err: ErrInvalidInput},
		{name: "unknown reply", input: PostInput{IdempotencyKey: "reply", Body: "body", ReplyTo: "01KNOTREAL"}, err: ErrInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, err := store.Post(discussion.ID, token, tt.input); !errors.Is(err, tt.err) {
				t.Fatalf("got %v want %v", err, tt.err)
			}
		})
	}

	first, _, err := store.Post(discussion.ID, token, PostInput{IdempotencyKey: "first", Body: "first"})
	if err != nil {
		t.Fatalf("post first: %v", err)
	}
	reply, _, err := store.Post(discussion.ID, token, PostInput{IdempotencyKey: "reply-ok", Body: "reply", ReplyTo: first.ID})
	if err != nil {
		t.Fatalf("post reply: %v", err)
	}
	if reply.ReplyTo != first.ID {
		t.Errorf("reply_to: got %q want %q", reply.ReplyTo, first.ID)
	}
}

func TestConcurrentPostsHaveContiguousUniqueSequences(t *testing.T) {
	store, _ := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	const count = 50

	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := store.Post(discussion.ID, capabilities["interviewer"], PostInput{
				IdempotencyKey: fmt.Sprintf("question-%d", i),
				Body:           fmt.Sprintf("body %d", i),
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent post: %v", err)
		}
	}

	messages, _, err := store.Read(discussion.ID, capabilities["interviewer"], ReadOptions{Limit: count})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(messages) != count {
		t.Fatalf("messages: got %d want %d", len(messages), count)
	}
	sequences := make([]int, 0, count)
	for _, message := range messages {
		sequences = append(sequences, message.Sequence)
	}
	sort.Ints(sequences)
	for i, sequence := range sequences {
		if sequence != i+1 {
			t.Fatalf("sequence %d: got %d want %d", i, sequence, i+1)
		}
	}
}

func TestReadUsesSequenceCursorAndRequiresReadPermission(t *testing.T) {
	store, _ := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	for i := 1; i <= 3; i++ {
		_, _, err := store.Post(discussion.ID, capabilities["interviewer"], PostInput{
			IdempotencyKey: fmt.Sprintf("message-%d", i),
			Body:           fmt.Sprintf("body %d", i),
		})
		if err != nil {
			t.Fatalf("post %d: %v", i, err)
		}
	}

	messages, metadata, err := store.Read(discussion.ID, capabilities["goat"], ReadOptions{AfterSequence: 1, Limit: 1})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(messages) != 1 || messages[0].Sequence != 2 {
		t.Fatalf("messages: got %+v want sequence 2", messages)
	}
	if metadata.LastSequence != 3 {
		t.Errorf("last_sequence: got %d want 3", metadata.LastSequence)
	}

	postOnly, postOnlyCapabilities, err := store.Create(CreateInput{
		TTL:          time.Minute,
		Participants: []ParticipantInput{{ID: "sender", Permissions: []Permission{PermissionPost}}},
	})
	if err != nil {
		t.Fatalf("create post-only: %v", err)
	}
	if _, _, err := store.Read(postOnly.ID, postOnlyCapabilities["sender"], ReadOptions{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("post-only read: got %v want ErrForbidden", err)
	}
}

func TestEndAndExpiryPreserveReadButRejectWrites(t *testing.T) {
	store, _ := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	if _, _, err := store.Post(discussion.ID, capabilities["interviewer"], PostInput{IdempotencyKey: "before", Body: "before"}); err != nil {
		t.Fatalf("post before end: %v", err)
	}

	ended, err := store.End(discussion.ID, capabilities["interviewer"])
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if ended.Status != StatusEnded {
		t.Fatalf("status: got %q want %q", ended.Status, StatusEnded)
	}
	if _, _, err := store.Post(discussion.ID, capabilities["goat"], PostInput{IdempotencyKey: "after", Body: "after"}); !errors.Is(err, ErrDiscussionEnded) {
		t.Fatalf("post after end: got %v want ErrDiscussionEnded", err)
	}
	if _, _, err := store.Read(discussion.ID, capabilities["goat"], ReadOptions{}); err != nil {
		t.Fatalf("read after end: %v", err)
	}
	if _, err := store.End(discussion.ID, capabilities["interviewer"]); err != nil {
		t.Fatalf("idempotent end: %v", err)
	}

	expiring, expiringCapabilities, err := store.Create(CreateInput{
		TTL: time.Minute,
		Participants: []ParticipantInput{
			{ID: "interviewer", Permissions: []Permission{PermissionRead, PermissionPost}},
		},
	})
	if err != nil {
		t.Fatalf("create expiring: %v", err)
	}
	store.now = func() time.Time { return testNow.Add(2 * time.Minute) }
	if _, _, err := store.Post(expiring.ID, expiringCapabilities["interviewer"], PostInput{IdempotencyKey: "late", Body: "late"}); !errors.Is(err, ErrDiscussionExpired) {
		t.Fatalf("post after expiry: got %v want ErrDiscussionExpired", err)
	}
	_, metadata, err := store.Read(expiring.ID, expiringCapabilities["interviewer"], ReadOptions{})
	if err != nil {
		t.Fatalf("read after expiry: %v", err)
	}
	if metadata.Status != StatusExpired {
		t.Errorf("expired status: got %q want %q", metadata.Status, StatusExpired)
	}
}

func TestEndRequiresManagePermission(t *testing.T) {
	store, _ := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	if _, err := store.End(discussion.ID, capabilities["goat"]); !errors.Is(err, ErrForbidden) {
		t.Fatalf("got %v want ErrForbidden", err)
	}
}

func TestReadFailsLoudlyOnMalformedAcceptedData(t *testing.T) {
	store, root := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	path := filepath.Join(root, DiscussionsDir, discussion.ID, MessagesFile)
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatalf("corrupt messages: %v", err)
	}

	_, _, err := store.Read(discussion.ID, capabilities["interviewer"], ReadOptions{})
	if err == nil {
		t.Fatal("expected malformed log error")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("error does not identify corrupt path and line: %v", err)
	}
}

func TestReadRejectsNonContiguousSequence(t *testing.T) {
	store, root := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	path := filepath.Join(root, DiscussionsDir, discussion.ID, MessagesFile)
	record := map[string]any{
		"id":              "01K3DUMMYMESSAGE0000000000",
		"discussion_id":   discussion.ID,
		"sequence":        2,
		"created_at":      testNow,
		"author":          "interviewer",
		"body":            "gap",
		"idempotency_key": "gap",
	}
	line, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatalf("write messages: %v", err)
	}

	if _, _, err := store.Read(discussion.ID, capabilities["interviewer"], ReadOptions{}); err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("got %v want sequence validation error", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("mode %s: got %#o want %#o", path, got, want)
	}
}
