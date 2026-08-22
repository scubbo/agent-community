package communityserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/scubbo/agent-community/internal/discussion"
)

var webhookTestNow = time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)

type staticResolver map[string][]net.IPAddr

func (r staticResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	addresses, exists := r[host]
	if !exists {
		return nil, errors.New("host not found")
	}
	return addresses, nil
}

func TestValidateWebhookURLRejectsUnsafeDestinations(t *testing.T) {
	public := net.IPAddr{IP: net.ParseIP("8.8.8.8")}
	private := net.IPAddr{IP: net.ParseIP("10.0.0.1")}
	resolver := staticResolver{
		"public.example":  {public},
		"private.example": {private},
		"mixed.example":   {public, private},
		"ipv6.example":    {{IP: net.ParseIP("fd00::1")}},
	}
	tests := []struct {
		name string
		url  string
		ok   bool
	}{
		{name: "public", url: "https://public.example/events", ok: true},
		{name: "http", url: "http://public.example/events"},
		{name: "credentials", url: "https://user:pass@public.example/events"},
		{name: "fragment", url: "https://public.example/events#secret"},
		{name: "loopback literal", url: "https://127.0.0.1/events"},
		{name: "private dns", url: "https://private.example/events"},
		{name: "mixed dns", url: "https://mixed.example/events"},
		{name: "private ipv6", url: "https://ipv6.example/events"},
		{name: "metadata", url: "https://169.254.169.254/events"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWebhookURL(context.Background(), tt.url, resolver)
			if tt.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestWebhookDeliverySignsExactBodyAndCompletes(t *testing.T) {
	store, err := discussion.NewStore(t.TempDir(), discussion.StoreOptions{Now: func() time.Time { return webhookTestNow }})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	created, capabilities, err := store.Create(discussion.CreateInput{
		TTL: time.Minute,
		Participants: []discussion.ParticipantInput{
			{ID: "interviewer", Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionPost}},
			{ID: "goat", Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionSubscribe}},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	secret := "secret-32-bytes-minimum-1234567890"
	received := make(chan struct{}, 1)
	callback := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		timestamp := r.Header.Get("X-Agent-Community-Timestamp")
		deliveryID := r.Header.Get("X-Agent-Community-Delivery")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(timestamp + "\n" + deliveryID + "\n"))
		_, _ = mac.Write(body)
		want := "v1=" + hex.EncodeToString(mac.Sum(nil))
		if !hmac.Equal([]byte(r.Header.Get("X-Agent-Community-Signature")), []byte(want)) {
			t.Errorf("bad signature: got %q want %q", r.Header.Get("X-Agent-Community-Signature"), want)
		}
		var envelope WebhookEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("decode envelope: %v", err)
		}
		if envelope.ID != deliveryID || envelope.Type != discussion.EventMessageCreated || envelope.Data.Message == nil || envelope.Data.Message.Body != "why?" {
			t.Errorf("unexpected envelope: %#v", envelope)
		}
		received <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer callback.Close()
	resolver := exposeTestServerAsPublic(t, callback)
	if _, err := store.Subscribe(created.ID, capabilities["goat"], discussion.SubscribeInput{
		CallbackURL: callback.URL, SigningSecret: secret, Events: []discussion.EventType{discussion.EventMessageCreated},
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, _, err := store.Post(created.ID, capabilities["interviewer"], discussion.PostInput{IdempotencyKey: "q1", Body: "why?"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	worker := NewWebhookWorker(store, WebhookWorkerOptions{
		Resolver: resolver,
		Client:   callback.Client(),
		Now:      func() time.Time { return webhookTestNow },
	})
	processed, err := worker.DeliverDue(context.Background(), 10)
	if err != nil || processed != 1 {
		t.Fatalf("deliver: processed=%d err=%v", processed, err)
	}
	select {
	case <-received:
	default:
		t.Fatal("callback was not received")
	}
	if due, _ := store.ListDueDeliveries(webhookTestNow.Add(time.Hour), 10); len(due) != 0 {
		t.Errorf("completed delivery remains due: %d", len(due))
	}
}

func TestWebhookDeliveryRetriesAndRejectsRedirect(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			store, delivery, callback, resolver := seededWebhookDelivery(t, status)
			defer callback.Close()
			worker := NewWebhookWorker(store, WebhookWorkerOptions{
				Resolver: resolver, Client: callback.Client(), Now: func() time.Time { return webhookTestNow },
			})
			processed, err := worker.DeliverDue(context.Background(), 1)
			if err != nil || processed != 1 {
				t.Fatalf("deliver: processed=%d err=%v", processed, err)
			}
			due, err := store.ListDueDeliveries(webhookTestNow.Add(2*time.Minute), 10)
			if err != nil || len(due) != 1 || due[0].ID != delivery.ID || due[0].Attempts != 1 {
				t.Fatalf("retry state: due=%#v err=%v", due, err)
			}
		})
	}
}

func TestWebhookDeliveryMarksTerminalClientErrorComplete(t *testing.T) {
	store, _, callback, resolver := seededWebhookDelivery(t, http.StatusBadRequest)
	defer callback.Close()
	worker := NewWebhookWorker(store, WebhookWorkerOptions{Resolver: resolver, Client: callback.Client(), Now: func() time.Time { return webhookTestNow }})
	if _, err := worker.DeliverDue(context.Background(), 1); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if due, _ := store.ListDueDeliveries(webhookTestNow.Add(time.Hour), 10); len(due) != 0 {
		t.Errorf("terminal client error remains due: %d", len(due))
	}
}

func TestWebhookDeliveryRevalidatesDNSBeforeRequest(t *testing.T) {
	store, _, callback, _ := seededWebhookDelivery(t, http.StatusNoContent)
	defer callback.Close()
	worker := NewWebhookWorker(store, WebhookWorkerOptions{
		Resolver: staticResolver{"callback.example": {{IP: net.ParseIP("10.0.0.1")}}},
		Client:   callback.Client(), Now: func() time.Time { return webhookTestNow },
	})
	if _, err := worker.DeliverDue(context.Background(), 1); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	due, _ := store.ListDueDeliveries(webhookTestNow.Add(2*time.Minute), 10)
	if len(due) != 1 || due[0].Attempts != 1 {
		t.Fatalf("unsafe DNS was not retried without request: %#v", due)
	}
}

func seededWebhookDelivery(t *testing.T, status int) (*discussion.Store, discussion.Delivery, *httptest.Server, staticResolver) {
	t.Helper()
	store, err := discussion.NewStore(t.TempDir(), discussion.StoreOptions{Now: func() time.Time { return webhookTestNow }})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	created, capabilities, err := store.Create(discussion.CreateInput{
		TTL: time.Hour,
		Participants: []discussion.ParticipantInput{
			{ID: "interviewer", Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionPost}},
			{ID: "goat", Permissions: []discussion.Permission{discussion.PermissionRead, discussion.PermissionSubscribe}},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	callback := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if status == http.StatusFound {
			w.Header().Set("Location", "https://private.example/steal")
		}
		w.WriteHeader(status)
	}))
	resolver := exposeTestServerAsPublic(t, callback)
	if _, err := store.Subscribe(created.ID, capabilities["goat"], discussion.SubscribeInput{
		CallbackURL: callback.URL, SigningSecret: "secret-32-bytes-minimum-1234567890", Events: []discussion.EventType{discussion.EventMessageCreated},
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, _, err := store.Post(created.ID, capabilities["interviewer"], discussion.PostInput{IdempotencyKey: "q1", Body: "why?"}); err != nil {
		t.Fatalf("post: %v", err)
	}
	due, err := store.ListDueDeliveries(webhookTestNow.Add(time.Second), 1)
	if err != nil || len(due) != 1 {
		t.Fatalf("due: %#v err=%v", due, err)
	}
	return store, due[0], callback, resolver
}

func exposeTestServerAsPublic(t *testing.T, server *httptest.Server) staticResolver {
	t.Helper()
	actualAddress := server.Listener.Addr().String()
	_, port, err := net.SplitHostPort(actualAddress)
	if err != nil {
		t.Fatalf("split test server address: %v", err)
	}
	client := server.Client()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected test server transport %T", client.Transport)
	}
	transport = transport.Clone()
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.InsecureSkipVerify = true // Test transport still uses TLS; hostname is synthetic.
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, actualAddress)
	}
	client.Transport = transport
	server.URL = "https://callback.example:" + port
	return staticResolver{"callback.example": {{IP: net.ParseIP("8.8.8.8")}}}
}
