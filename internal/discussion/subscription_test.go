package discussion

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSubscribePersistsSecretPrivatelyAndReconcilesDeliveries(t *testing.T) {
	store, root := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	subscription, err := store.Subscribe(discussion.ID, capabilities["goat"], SubscribeInput{
		CallbackURL:   "https://farm.example/interviews/events",
		SigningSecret: "secret-32-bytes-minimum-1234567890",
		Events:        []EventType{EventMessageCreated, EventDiscussionEnded},
		IgnoreSelf:    true,
	})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if subscription.ID == "" || subscription.ParticipantID != "goat" {
		t.Errorf("unexpected subscription: %#v", subscription)
	}
	path := filepath.Join(root, DiscussionsDir, discussion.ID, SubscriptionsFile)
	assertMode(t, path, 0o600)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read subscriptions: %v", err)
	}
	if !containsBytes(data, []byte("secret-32-bytes-minimum-1234567890")) {
		t.Fatal("subscription secret was not persisted for webhook signing")
	}

	if _, _, err := store.Post(discussion.ID, capabilities["interviewer"], PostInput{IdempotencyKey: "q1", Body: "why?"}); err != nil {
		t.Fatalf("post interviewer: %v", err)
	}
	if _, _, err := store.Post(discussion.ID, capabilities["goat"], PostInput{IdempotencyKey: "a1", Body: "because"}); err != nil {
		t.Fatalf("post goat: %v", err)
	}
	due, err := store.ListDueDeliveries(testNow.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("deliveries: got %d want 1 (ignore_self suppresses goat answer)", len(due))
	}
	if due[0].Event.Type != EventMessageCreated || due[0].Event.Message == nil || due[0].Event.Message.Author != "interviewer" {
		t.Errorf("unexpected delivery: %#v", due[0])
	}
	if due[0].CallbackURL != "https://farm.example/interviews/events" || due[0].SigningSecret == "" {
		t.Errorf("delivery missing endpoint credentials: %#v", due[0])
	}
}

func TestSubscribeRequiresPermissionAndOneActiveSubscription(t *testing.T) {
	store, _ := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	input := SubscribeInput{CallbackURL: "https://farm.example/events", SigningSecret: "secret-32-bytes-minimum-1234567890", Events: []EventType{EventMessageCreated}}
	if _, err := store.Subscribe(discussion.ID, capabilities["interviewer"], input); !errors.Is(err, ErrForbidden) {
		t.Fatalf("interviewer subscribe: got %v want ErrForbidden", err)
	}
	first, err := store.Subscribe(discussion.ID, capabilities["goat"], input)
	if err != nil {
		t.Fatalf("first subscribe: %v", err)
	}
	if _, err := store.Subscribe(discussion.ID, capabilities["goat"], input); !errors.Is(err, ErrSubscriptionExists) {
		t.Fatalf("second subscribe: got %v want ErrSubscriptionExists", err)
	}
	if err := store.Unsubscribe(discussion.ID, capabilities["interviewer"], first.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("other participant unsubscribe: got %v want ErrForbidden", err)
	}
	if err := store.Unsubscribe(discussion.ID, capabilities["goat"], first.ID); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	if err := store.Unsubscribe(discussion.ID, capabilities["goat"], first.ID); err != nil {
		t.Fatalf("idempotent unsubscribe: %v", err)
	}
}

func TestEndCreatesTerminalDeliveryAndDeliveryStateTransitions(t *testing.T) {
	store, _ := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	if _, err := store.Subscribe(discussion.ID, capabilities["goat"], SubscribeInput{
		CallbackURL: "https://farm.example/events", SigningSecret: "secret-32-bytes-minimum-1234567890", Events: []EventType{EventDiscussionEnded},
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := store.End(discussion.ID, capabilities["interviewer"]); err != nil {
		t.Fatalf("end: %v", err)
	}
	due, err := store.ListDueDeliveries(testNow.Add(time.Second), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("due after end: len=%d err=%v", len(due), err)
	}
	if due[0].Event.Type != EventDiscussionEnded || due[0].Event.Ended == nil || due[0].Event.Ended.Reason != EndReasonExplicit {
		t.Errorf("unexpected end event: %#v", due[0].Event)
	}
	if err := store.RetryDelivery(due[0].ID, testNow.Add(time.Minute), "temporary"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if next, _ := store.ListDueDeliveries(testNow.Add(30*time.Second), 10); len(next) != 0 {
		t.Errorf("retry became due too early: %d", len(next))
	}
	if next, _ := store.ListDueDeliveries(testNow.Add(2*time.Minute), 10); len(next) != 1 || next[0].Attempts != 1 {
		t.Errorf("retry state not persisted: %#v", next)
	}
	if err := store.CompleteDelivery(due[0].ID); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if next, _ := store.ListDueDeliveries(testNow.Add(2*time.Minute), 10); len(next) != 0 {
		t.Errorf("completed delivery remains due: %d", len(next))
	}
}

func TestPostReplayReconcilesMissingOutbox(t *testing.T) {
	store, root := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	if _, err := store.Subscribe(discussion.ID, capabilities["goat"], SubscribeInput{
		CallbackURL: "https://farm.example/events", SigningSecret: "secret-32-bytes-minimum-1234567890", Events: []EventType{EventMessageCreated},
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	input := PostInput{IdempotencyKey: "q1", Body: "why?"}
	message, _, err := store.Post(discussion.ID, capabilities["interviewer"], input)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	outbox := filepath.Join(root, DiscussionsDir, discussion.ID, OutboxDir)
	entries, err := os.ReadDir(outbox)
	if err != nil || len(entries) != 1 {
		t.Fatalf("read outbox: entries=%d err=%v", len(entries), err)
	}
	if err := os.Remove(filepath.Join(outbox, entries[0].Name())); err != nil {
		t.Fatalf("remove delivery: %v", err)
	}
	replayed, wasReplay, err := store.Post(discussion.ID, capabilities["interviewer"], input)
	if err != nil || !wasReplay || replayed.ID != message.ID {
		t.Fatalf("replay: message=%#v replay=%v err=%v", replayed, wasReplay, err)
	}
	if due, err := store.ListDueDeliveries(testNow.Add(time.Second), 10); err != nil || len(due) != 1 {
		t.Fatalf("reconciled due: len=%d err=%v", len(due), err)
	}
}

func TestReconcileDeliveriesRepairsMissingOutboxWithoutPostReplay(t *testing.T) {
	store, root := newTestStore(t)
	discussion, capabilities := createTestDiscussion(t, store)
	if _, err := store.Subscribe(discussion.ID, capabilities["goat"], SubscribeInput{
		CallbackURL: "https://farm.example/events", SigningSecret: "secret-32-bytes-minimum-1234567890", Events: []EventType{EventMessageCreated, EventDiscussionEnded},
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, _, err := store.Post(discussion.ID, capabilities["interviewer"], PostInput{IdempotencyKey: "q1", Body: "why?"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	if _, err := store.End(discussion.ID, capabilities["interviewer"]); err != nil {
		t.Fatalf("end: %v", err)
	}
	outbox := filepath.Join(root, DiscussionsDir, discussion.ID, OutboxDir)
	entries, err := os.ReadDir(outbox)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(outbox, entry.Name())); err != nil {
			t.Fatalf("remove delivery: %v", err)
		}
	}

	if err := store.ReconcileDeliveries(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	due, err := store.ListDueDeliveries(testNow.Add(time.Second), 10)
	if err != nil {
		t.Fatalf("list due: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("deliveries: got %d want message + end", len(due))
	}
}

func TestExpireDueTransitionsAndCreatesEndDeliveryWithoutAccess(t *testing.T) {
	current := testNow
	store, _ := NewStore(t.TempDir(), StoreOptions{Now: func() time.Time { return current }})
	discussion, capabilities := createTestDiscussion(t, store)
	if _, err := store.Subscribe(discussion.ID, capabilities["goat"], SubscribeInput{
		CallbackURL: "https://farm.example/events", SigningSecret: "secret-32-bytes-minimum-1234567890", Events: []EventType{EventDiscussionEnded},
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	current = discussion.ExpiresAt.Add(time.Second)

	expired, err := store.ExpireDue()
	if err != nil {
		t.Fatalf("expire due: %v", err)
	}
	if len(expired) != 1 || expired[0] != discussion.ID {
		t.Fatalf("expired: got %v want [%s]", expired, discussion.ID)
	}
	due, err := store.ListDueDeliveries(current, 10)
	if err != nil || len(due) != 1 || due[0].Event.Ended == nil || due[0].Event.Ended.Reason != EndReasonExpired {
		t.Fatalf("expiry delivery: %#v err=%v", due, err)
	}
}

func containsBytes(data, target []byte) bool {
	for i := 0; i+len(target) <= len(data); i++ {
		match := true
		for j := range target {
			if data[i+j] != target[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
