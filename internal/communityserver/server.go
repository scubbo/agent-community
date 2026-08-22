package communityserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scubbo/agent-community/internal/discussion"
)

const (
	MaxRequestBodyBytes = discussion.MaxBodyBytes + 8*1024
	maxLongPollWait     = 25 * time.Second
)

type Options struct {
	InstanceID string
	Resolver   IPResolver
}

type Server struct {
	store      *discussion.Store
	instanceID string
	resolver   IPResolver

	notificationsMu sync.Mutex
	notifications   map[string]chan struct{}
}

func New(store *discussion.Store, opts Options) (*Server, error) {
	if store == nil {
		return nil, errors.New("discussion store is required")
	}
	if opts.InstanceID == "" {
		return nil, errors.New("instance id is required")
	}
	resolver := opts.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	return &Server{
		store:         store,
		instanceID:    opts.InstanceID,
		resolver:      resolver,
		notifications: make(map[string]chan struct{}),
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if r.URL.Path == "/healthz" {
		if r.Method != http.MethodGet {
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "instance_id": s.instanceID})
		return
	}

	discussionID, resource, childID, ok := parseDiscussionPath(r.URL.Path)
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	w.Header().Set("Cache-Control", "no-store")

	switch resource {
	case "":
		s.handleDiscussion(w, r, discussionID, token)
	case "messages":
		s.handleMessages(w, r, discussionID, token)
	case "subscriptions":
		s.handleSubscriptions(w, r, discussionID, childID, token)
	default:
		s.writeError(w, http.StatusNotFound, "not_found")
	}
}

func (s *Server) handleSubscriptions(w http.ResponseWriter, r *http.Request, discussionID, subscriptionID, token string) {
	if subscriptionID == "" {
		if r.Method != http.MethodPost {
			s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		data, err := readBoundedBody(w, r)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_body")
			return
		}
		var input struct {
			CallbackURL   string                 `json:"callback_url"`
			SigningSecret string                 `json:"signing_secret"`
			Events        []discussion.EventType `json:"events"`
			IgnoreSelf    bool                   `json:"ignore_self"`
		}
		if err := decodeStrict(data, &input); err != nil {
			s.writeError(w, http.StatusBadRequest, "invalid_body")
			return
		}
		if err := ValidateWebhookURL(r.Context(), input.CallbackURL, s.resolver); err != nil {
			s.writeError(w, http.StatusBadRequest, "unsafe_callback_url")
			return
		}
		subscription, err := s.store.Subscribe(discussionID, token, discussion.SubscribeInput{
			CallbackURL: input.CallbackURL, SigningSecret: input.SigningSecret, Events: input.Events, IgnoreSelf: input.IgnoreSelf,
		})
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "subscription": subscription})
		return
	}
	if r.Method != http.MethodDelete {
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if err := s.store.Unsubscribe(discussionID, token, subscriptionID); err != nil {
		s.writeStoreError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleDiscussion(w http.ResponseWriter, r *http.Request, discussionID, token string) {
	switch r.Method {
	case http.MethodGet:
		metadata, self, err := s.store.Get(discussionID, token)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"discussion": struct {
				*discussion.Discussion
				Self *discussion.AuthenticatedParticipant `json:"self"`
			}{Discussion: metadata, Self: self},
		})
	case http.MethodDelete:
		metadata, err := s.store.End(discussionID, token)
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		s.notify(discussionID)
		s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "discussion": metadata})
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request, discussionID, token string) {
	switch r.Method {
	case http.MethodGet:
		s.handleReadMessages(w, r, discussionID, token)
	case http.MethodPost:
		s.handlePostMessage(w, r, discussionID, token)
	default:
		s.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (s *Server) handlePostMessage(w http.ResponseWriter, r *http.Request, discussionID, token string) {
	data, err := readBoundedBody(w, r)
	if err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	var input struct {
		IdempotencyKey string `json:"idempotency_key"`
		Body           string `json:"body"`
		ReplyTo        string `json:"reply_to"`
	}
	if err := decodeStrict(data, &input); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_body")
		return
	}
	if len([]byte(input.Body)) > discussion.MaxBodyBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, "message_too_large")
		return
	}
	message, replayed, err := s.store.Post(discussionID, token, discussion.PostInput{
		IdempotencyKey: input.IdempotencyKey,
		Body:           input.Body,
		ReplyTo:        input.ReplyTo,
	})
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	if !replayed {
		s.notify(discussionID)
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
	}
	s.writeJSON(w, status, map[string]any{"ok": true, "message": message})
}

func (s *Server) handleReadMessages(w http.ResponseWriter, r *http.Request, discussionID, token string) {
	afterSequence, limit, wait, err := parseReadQuery(r)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}

	for {
		// Register before reading. A post that races this read either appears in
		// the durable read or closes this channel, so no accepted message is lost.
		notification := s.notification(discussionID)
		messages, metadata, err := s.store.Read(discussionID, token, discussion.ReadOptions{
			AfterSequence: afterSequence,
			Limit:         limit,
		})
		if err != nil {
			s.writeStoreError(w, err)
			return
		}
		if len(messages) > 0 || metadata.Status != discussion.StatusActive || wait == 0 {
			s.writeMessageRead(w, messages, metadata)
			return
		}

		waitDuration := wait
		if untilExpiry := time.Until(metadata.ExpiresAt); untilExpiry < waitDuration {
			waitDuration = untilExpiry
		}
		if waitDuration <= 0 {
			wait = 0
			continue
		}
		timer := time.NewTimer(waitDuration)
		select {
		case <-notification:
			if !timer.Stop() {
				<-timer.C
			}
			// Use a non-blocking read after a notification; a later request can
			// wait again if another consumer used a newer cursor.
			wait = 0
		case <-timer.C:
			wait = 0
		case <-r.Context().Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		}
	}
}

func (s *Server) writeMessageRead(w http.ResponseWriter, messages []discussion.Message, metadata *discussion.Discussion) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"messages":      messages,
		"last_sequence": metadata.LastSequence,
		"status":        metadata.Status,
	})
}

func (s *Server) notification(discussionID string) <-chan struct{} {
	s.notificationsMu.Lock()
	defer s.notificationsMu.Unlock()
	channel := s.notifications[discussionID]
	if channel == nil {
		channel = make(chan struct{})
		s.notifications[discussionID] = channel
	}
	return channel
}

func (s *Server) notify(discussionID string) {
	s.notificationsMu.Lock()
	defer s.notificationsMu.Unlock()
	if channel := s.notifications[discussionID]; channel != nil {
		close(channel)
	}
	s.notifications[discussionID] = make(chan struct{})
}

func (s *Server) writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, discussion.ErrUnauthorized):
		s.writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, discussion.ErrForbidden):
		s.writeError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, discussion.ErrInvalidInput):
		s.writeError(w, http.StatusBadRequest, "invalid_input")
	case errors.Is(err, discussion.ErrIdempotencyConflict):
		s.writeError(w, http.StatusConflict, "idempotency_conflict")
	case errors.Is(err, discussion.ErrDiscussionEnded):
		s.writeError(w, http.StatusConflict, "discussion_ended")
	case errors.Is(err, discussion.ErrDiscussionExpired):
		s.writeError(w, http.StatusConflict, "discussion_expired")
	case errors.Is(err, discussion.ErrMessageLimit):
		s.writeError(w, http.StatusConflict, "message_limit_reached")
	case errors.Is(err, discussion.ErrSubscriptionExists):
		s.writeError(w, http.StatusConflict, "subscription_exists")
	case errors.Is(err, discussion.ErrSubscriptionNotFound):
		s.writeError(w, http.StatusNotFound, "subscription_not_found")
	default:
		s.writeError(w, http.StatusInternalServerError, "internal_error")
	}
}

func (s *Server) writeError(w http.ResponseWriter, status int, code string) {
	s.writeJSON(w, status, map[string]any{"ok": false, "error": code})
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func parseDiscussionPath(path string) (string, string, string, bool) {
	const prefix = "/v1/discussions/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", "", false
	}
	remainder := strings.TrimPrefix(path, prefix)
	if remainder == "" || strings.HasSuffix(remainder, "/") {
		return "", "", "", false
	}
	parts := strings.Split(remainder, "/")
	if len(parts) == 1 {
		return parts[0], "", "", true
	}
	if len(parts) == 2 {
		return parts[0], parts[1], "", true
	}
	if len(parts) == 3 && parts[1] == "subscriptions" {
		return parts[0], parts[1], parts[2], true
	}
	return "", "", "", false
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	token := strings.TrimPrefix(header, prefix)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", false
	}
	return token, true
}

func parseReadQuery(r *http.Request) (int, int, time.Duration, error) {
	query := r.URL.Query()
	for key, values := range query {
		if key != "after_sequence" && key != "limit" && key != "wait" {
			return 0, 0, 0, fmt.Errorf("unknown query parameter %q", key)
		}
		if len(values) != 1 {
			return 0, 0, 0, fmt.Errorf("query parameter %q must appear once", key)
		}
	}
	afterSequence := 0
	if raw := query.Get("after_sequence"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			return 0, 0, 0, errors.New("invalid after_sequence")
		}
		afterSequence = value
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > discussion.MaxReadLimit {
			return 0, 0, 0, errors.New("invalid limit")
		}
		limit = value
	}
	var wait time.Duration
	if raw := query.Get("wait"); raw != "" {
		value, err := time.ParseDuration(raw)
		if err != nil || value <= 0 || value > maxLongPollWait {
			return 0, 0, 0, errors.New("invalid wait")
		}
		wait = value
	}
	return afterSequence, limit, wait, nil
}

func readBoundedBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body := http.MaxBytesReader(w, r.Body, MaxRequestBodyBytes)
	defer body.Close()
	return io.ReadAll(body)
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
