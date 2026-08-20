package discussion

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

type subscriptionRecord struct {
	ID            string      `json:"id"`
	ParticipantID string      `json:"participant_id"`
	CallbackURL   string      `json:"callback_url"`
	SigningSecret string      `json:"signing_secret"`
	Events        []EventType `json:"events"`
	IgnoreSelf    bool        `json:"ignore_self"`
	CreatedAt     time.Time   `json:"created_at"`
	RemovedAt     *time.Time  `json:"removed_at,omitempty"`
}

type deliveryRecord struct {
	ID             string    `json:"id"`
	DiscussionID   string    `json:"discussion_id"`
	SubscriptionID string    `json:"subscription_id"`
	Event          Event     `json:"event"`
	Status         string    `json:"status"`
	Attempts       int       `json:"attempts"`
	NextAttemptAt  time.Time `json:"next_attempt_at"`
	LastError      string    `json:"last_error,omitempty"`
}

func (s *Store) Subscribe(discussionID, token string, input SubscribeInput) (*Subscription, error) {
	if err := validateSubscribeInput(input); err != nil {
		return nil, err
	}
	lock := s.discussionLock(discussionID)
	lock.Lock()
	defer lock.Unlock()
	record, participant, err := s.authorize(discussionID, token)
	if err != nil {
		return nil, err
	}
	if !hasPermission(participant.Capability, PermissionSubscribe) {
		return nil, ErrForbidden
	}
	if err := s.refreshStatus(record); err != nil {
		return nil, err
	}
	if record.Status == StatusEnded {
		return nil, ErrDiscussionEnded
	}
	if record.Status == StatusExpired {
		return nil, ErrDiscussionExpired
	}
	subscriptions, err := s.readSubscriptions(record.ID)
	if err != nil {
		return nil, err
	}
	for _, subscription := range subscriptions {
		if subscription.ParticipantID == participant.ID && subscription.RemovedAt == nil {
			return nil, ErrSubscriptionExists
		}
	}
	now := s.now().UTC()
	id, err := ulid.New(ulid.Timestamp(now), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate subscription id: %w", err)
	}
	stored := subscriptionRecord{
		ID: id.String(), ParticipantID: participant.ID, CallbackURL: input.CallbackURL,
		SigningSecret: input.SigningSecret, Events: append([]EventType(nil), input.Events...),
		IgnoreSelf: input.IgnoreSelf, CreatedAt: now,
	}
	subscriptions = append(subscriptions, stored)
	if err := s.writeSubscriptions(record.ID, subscriptions); err != nil {
		return nil, err
	}
	return publicSubscription(stored), nil
}

func (s *Store) Unsubscribe(discussionID, token, subscriptionID string) error {
	lock := s.discussionLock(discussionID)
	lock.Lock()
	defer lock.Unlock()
	_, participant, err := s.authorize(discussionID, token)
	if err != nil {
		return err
	}
	if !hasPermission(participant.Capability, PermissionSubscribe) {
		return ErrForbidden
	}
	subscriptions, err := s.readSubscriptions(discussionID)
	if err != nil {
		return err
	}
	found := false
	changed := false
	now := s.now().UTC()
	for i := range subscriptions {
		if subscriptions[i].ID != subscriptionID {
			continue
		}
		found = true
		if subscriptions[i].ParticipantID != participant.ID {
			return ErrForbidden
		}
		if subscriptions[i].RemovedAt == nil {
			subscriptions[i].RemovedAt = &now
			changed = true
		}
	}
	if !found {
		return nil
	}
	if changed {
		return s.writeSubscriptions(discussionID, subscriptions)
	}
	return nil
}

func (s *Store) ListDueDeliveries(now time.Time, limit int) ([]Delivery, error) {
	if limit <= 0 {
		return nil, nil
	}
	root := filepath.Join(s.root, DiscussionsDir)
	discussions, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var due []Delivery
	for _, entry := range discussions {
		if !entry.IsDir() {
			continue
		}
		discussionID := entry.Name()
		lock := s.discussionLock(discussionID)
		lock.Lock()
		subscriptions, subErr := s.readSubscriptions(discussionID)
		if subErr != nil {
			lock.Unlock()
			return nil, subErr
		}
		byID := make(map[string]subscriptionRecord, len(subscriptions))
		for _, subscription := range subscriptions {
			byID[subscription.ID] = subscription
		}
		records, recordErr := s.readDeliveryRecords(discussionID)
		lock.Unlock()
		if recordErr != nil {
			return nil, recordErr
		}
		for _, record := range records {
			subscription, exists := byID[record.SubscriptionID]
			if !exists || subscription.RemovedAt != nil || record.Status != "pending" || record.NextAttemptAt.After(now) {
				continue
			}
			due = append(due, Delivery{
				ID: record.ID, SubscriptionID: record.SubscriptionID,
				CallbackURL: subscription.CallbackURL, SigningSecret: subscription.SigningSecret,
				Event: record.Event, Attempts: record.Attempts, NextAttemptAt: record.NextAttemptAt,
			})
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].NextAttemptAt.Equal(due[j].NextAttemptAt) {
			return due[i].ID < due[j].ID
		}
		return due[i].NextAttemptAt.Before(due[j].NextAttemptAt)
	})
	if len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}

func (s *Store) ReconcileDeliveries() error {
	root := filepath.Join(s.root, DiscussionsDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		discussionID := entry.Name()
		lock := s.discussionLock(discussionID)
		lock.Lock()
		record, readErr := s.readRecord(discussionID)
		if readErr == nil {
			messages, messagesErr := s.readMessages(*record)
			if messagesErr != nil {
				readErr = messagesErr
			} else {
				for _, message := range messages {
					if err := s.reconcileMessageDeliveries(*record, message.Message); err != nil {
						readErr = err
						break
					}
				}
			}
			if readErr == nil && record.Status == StatusEnded {
				readErr = s.reconcileEndDeliveries(*record, EndReasonExplicit)
			}
			if readErr == nil && record.Status == StatusExpired {
				readErr = s.reconcileEndDeliveries(*record, EndReasonExpired)
			}
		}
		lock.Unlock()
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

func (s *Store) ExpireDue() ([]string, error) {
	root := filepath.Join(s.root, DiscussionsDir)
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var expired []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		discussionID := entry.Name()
		lock := s.discussionLock(discussionID)
		lock.Lock()
		record, readErr := s.readRecord(discussionID)
		if readErr == nil && record.Status == StatusActive && !s.now().UTC().Before(record.ExpiresAt) {
			record.Status = StatusExpired
			readErr = s.writeRecord(*record)
			if readErr == nil {
				readErr = s.reconcileEndDeliveries(*record, EndReasonExpired)
			}
			if readErr == nil {
				expired = append(expired, discussionID)
			}
		}
		lock.Unlock()
		if readErr != nil {
			return nil, readErr
		}
	}
	return expired, nil
}

func (s *Store) RetryDelivery(deliveryID string, nextAttemptAt time.Time, lastError string) error {
	return s.updateDelivery(deliveryID, func(record *deliveryRecord) {
		record.Attempts++
		record.NextAttemptAt = nextAttemptAt.UTC()
		record.LastError = lastError
	})
}

func (s *Store) CompleteDelivery(deliveryID string) error {
	return s.updateDelivery(deliveryID, func(record *deliveryRecord) {
		record.Status = "completed"
		record.LastError = ""
	})
}

func (s *Store) reconcileMessageDeliveries(record discussionRecord, message Message) error {
	return s.reconcileDeliveries(record.ID, Event{
		Type: EventMessageCreated, CreatedAt: message.CreatedAt, DiscussionID: record.ID, Message: &message,
	})
}

func (s *Store) reconcileEndDeliveries(record discussionRecord, reason EndReason) error {
	return s.reconcileDeliveries(record.ID, Event{
		Type: EventDiscussionEnded, CreatedAt: s.now().UTC(), DiscussionID: record.ID,
		Ended: &DiscussionEnded{Status: record.Status, Reason: reason},
	})
}

func (s *Store) reconcileDeliveries(discussionID string, event Event) error {
	subscriptions, err := s.readSubscriptions(discussionID)
	if err != nil {
		return err
	}
	existing, err := s.readDeliveryRecords(discussionID)
	if err != nil {
		return err
	}
	for _, subscription := range subscriptions {
		if subscription.RemovedAt != nil || !subscriptionAccepts(subscription, event) || deliveryExists(existing, subscription.ID, event) {
			continue
		}
		id, err := ulid.New(ulid.Timestamp(s.now().UTC()), rand.Reader)
		if err != nil {
			return fmt.Errorf("generate delivery id: %w", err)
		}
		delivery := deliveryRecord{
			ID: id.String(), DiscussionID: discussionID, SubscriptionID: subscription.ID,
			Event: event, Status: "pending", NextAttemptAt: s.now().UTC(),
		}
		if err := writeJSONAtomic(filepath.Join(s.discussionDir(discussionID), OutboxDir, delivery.ID+".json"), &delivery); err != nil {
			return err
		}
		existing = append(existing, delivery)
	}
	return nil
}

func (s *Store) readSubscriptions(discussionID string) ([]subscriptionRecord, error) {
	path := filepath.Join(s.discussionDir(discussionID), SubscriptionsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var subscriptions []subscriptionRecord
	if err := decodeStrict(data, &subscriptions); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	seen := map[string]struct{}{}
	for _, subscription := range subscriptions {
		if subscription.ID == "" || subscription.ParticipantID == "" || subscription.CallbackURL == "" || subscription.SigningSecret == "" || subscription.CreatedAt.IsZero() {
			return nil, fmt.Errorf("validate %s: invalid subscription", path)
		}
		if _, exists := seen[subscription.ID]; exists {
			return nil, fmt.Errorf("validate %s: duplicate subscription %q", path, subscription.ID)
		}
		seen[subscription.ID] = struct{}{}
	}
	return subscriptions, nil
}

func (s *Store) writeSubscriptions(discussionID string, subscriptions []subscriptionRecord) error {
	return writeJSONAtomic(filepath.Join(s.discussionDir(discussionID), SubscriptionsFile), subscriptions)
}

func (s *Store) readDeliveryRecords(discussionID string) ([]deliveryRecord, error) {
	dir := filepath.Join(s.discussionDir(discussionID), OutboxDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	records := make([]deliveryRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var record deliveryRecord
		if err := decodeStrict(data, &record); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		if record.ID == "" || record.DiscussionID != discussionID || record.SubscriptionID == "" || (record.Status != "pending" && record.Status != "completed" && record.Status != "failed") {
			return nil, fmt.Errorf("validate %s: invalid delivery", path)
		}
		records = append(records, record)
	}
	return records, nil
}

func (s *Store) updateDelivery(deliveryID string, update func(*deliveryRecord)) error {
	root := filepath.Join(s.root, DiscussionsDir)
	discussions, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range discussions {
		if !entry.IsDir() {
			continue
		}
		discussionID := entry.Name()
		path := filepath.Join(s.discussionDir(discussionID), OutboxDir, deliveryID+".json")
		lock := s.discussionLock(discussionID)
		lock.Lock()
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			lock.Unlock()
			continue
		}
		if err != nil {
			lock.Unlock()
			return err
		}
		var record deliveryRecord
		if err := decodeStrict(data, &record); err != nil {
			lock.Unlock()
			return err
		}
		update(&record)
		err = writeJSONAtomic(path, &record)
		lock.Unlock()
		return err
	}
	return os.ErrNotExist
}

func validateSubscribeInput(input SubscribeInput) error {
	parsed, err := url.Parse(input.CallbackURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("%w: callback URL must be HTTPS", ErrInvalidInput)
	}
	if len(input.SigningSecret) < 32 {
		return fmt.Errorf("%w: signing secret must contain at least 32 characters", ErrInvalidInput)
	}
	if len(input.Events) == 0 {
		return fmt.Errorf("%w: at least one event is required", ErrInvalidInput)
	}
	seen := map[EventType]struct{}{}
	for _, event := range input.Events {
		if event != EventMessageCreated && event != EventDiscussionEnded {
			return fmt.Errorf("%w: unknown event %q", ErrInvalidInput, event)
		}
		if _, exists := seen[event]; exists {
			return fmt.Errorf("%w: duplicate event %q", ErrInvalidInput, event)
		}
		seen[event] = struct{}{}
	}
	return nil
}

func subscriptionAccepts(subscription subscriptionRecord, event Event) bool {
	if event.Message != nil && subscription.IgnoreSelf && event.Message.Author == subscription.ParticipantID {
		return false
	}
	for _, candidate := range subscription.Events {
		if candidate == event.Type {
			return true
		}
	}
	return false
}

func deliveryExists(records []deliveryRecord, subscriptionID string, event Event) bool {
	for _, record := range records {
		if record.SubscriptionID != subscriptionID || record.Event.Type != event.Type {
			continue
		}
		if event.Message != nil && record.Event.Message != nil && record.Event.Message.ID == event.Message.ID {
			return true
		}
		if event.Ended != nil && record.Event.Ended != nil {
			return true
		}
	}
	return false
}

func publicSubscription(record subscriptionRecord) *Subscription {
	return &Subscription{
		ID: record.ID, ParticipantID: record.ParticipantID, Events: append([]EventType(nil), record.Events...),
		IgnoreSelf: record.IgnoreSelf, CreatedAt: record.CreatedAt,
	}
}
