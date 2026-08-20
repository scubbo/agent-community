package communityserver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackjackson/agent-community/internal/discussion"
)

const webhookResponseLimit = 64 * 1024

type IPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type WebhookEnvelope struct {
	ID           string               `json:"id"`
	Type         discussion.EventType `json:"type"`
	CreatedAt    time.Time            `json:"created_at"`
	DiscussionID string               `json:"discussion_id"`
	Data         WebhookData          `json:"data"`
}

type WebhookData struct {
	Message *discussion.Message         `json:"message,omitempty"`
	Ended   *discussion.DiscussionEnded `json:"ended,omitempty"`
}

type WebhookWorkerOptions struct {
	Resolver IPResolver
	Client   *http.Client
	Now      func() time.Time
}

type WebhookWorker struct {
	store        *discussion.Store
	resolver     IPResolver
	client       *http.Client
	customClient bool
	now          func() time.Time
}

func NewWebhookWorker(store *discussion.Store, opts WebhookWorkerOptions) *WebhookWorker {
	resolver := opts.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	var client *http.Client
	customClient := opts.Client != nil
	if customClient {
		copy := *opts.Client
		copy.Timeout = 10 * time.Second
		copy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		client = &copy
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &WebhookWorker{store: store, resolver: resolver, client: client, customClient: customClient, now: now}
}

func (w *WebhookWorker) DeliverDue(ctx context.Context, limit int) (int, error) {
	due, err := w.store.ListDueDeliveries(w.now().UTC(), limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, delivery := range due {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		processed++
		if err := w.deliver(ctx, delivery); err != nil {
			return processed, err
		}
	}
	return processed, nil
}

func (w *WebhookWorker) deliver(ctx context.Context, delivery discussion.Delivery) error {
	parsedURL, addresses, err := resolveWebhookURL(ctx, delivery.CallbackURL, w.resolver)
	if err != nil {
		return w.retry(delivery, err.Error(), 0)
	}
	envelope := WebhookEnvelope{
		ID: delivery.ID, Type: delivery.Event.Type, CreatedAt: delivery.Event.CreatedAt,
		DiscussionID: delivery.Event.DiscussionID,
		Data:         WebhookData{Message: delivery.Event.Message, Ended: delivery.Event.Ended},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	timestamp := strconv.FormatInt(w.now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(delivery.SigningSecret))
	_, _ = mac.Write([]byte(timestamp + "\n" + delivery.ID + "\n"))
	_, _ = mac.Write(body)
	signature := "v1=" + hex.EncodeToString(mac.Sum(nil))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.CallbackURL, bytes.NewReader(body))
	if err != nil {
		return w.retry(delivery, err.Error(), 0)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Agent-Community-Delivery", delivery.ID)
	request.Header.Set("X-Agent-Community-Timestamp", timestamp)
	request.Header.Set("X-Agent-Community-Signature", signature)
	client := w.client
	if !w.customClient {
		client = pinnedWebhookClient(parsedURL, addresses)
	}
	response, err := client.Do(request)
	if err != nil {
		return w.retry(delivery, err.Error(), 0)
	}
	defer response.Body.Close()
	_, readErr := io.Copy(io.Discard, io.LimitReader(response.Body, webhookResponseLimit+1))
	if readErr != nil {
		return w.retry(delivery, readErr.Error(), 0)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return w.store.CompleteDelivery(delivery.ID)
	}
	if retryableStatus(response.StatusCode) {
		return w.retry(delivery, fmt.Sprintf("HTTP %d", response.StatusCode), retryAfter(response, w.now().UTC()))
	}
	return w.store.CompleteDelivery(delivery.ID)
}

func (w *WebhookWorker) retry(delivery discussion.Delivery, reason string, retryAfterDuration time.Duration) error {
	delay := retryAfterDuration
	if delay <= 0 {
		delay = deliveryBackoff(delivery.Attempts)
	}
	return w.store.RetryDelivery(delivery.ID, w.now().UTC().Add(delay), truncate(reason, 500))
}

func ValidateWebhookURL(ctx context.Context, raw string, resolver IPResolver) error {
	_, _, err := resolveWebhookURL(ctx, raw, resolver)
	return err
}

func resolveWebhookURL(ctx context.Context, raw string, resolver IPResolver) (*url.URL, []netip.Addr, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, nil, errors.New("webhook URL must be absolute HTTPS")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return nil, nil, errors.New("webhook URL must not contain credentials or fragment")
	}
	host := parsed.Hostname()
	if literal, err := netip.ParseAddr(host); err == nil && !publicWebhookAddress(literal.Unmap()) {
		return nil, nil, fmt.Errorf("webhook host is a forbidden address: %s", literal)
	}
	addresses, err := resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, nil, fmt.Errorf("resolve webhook host %q: %w", host, err)
	}
	validated := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		ip, ok := netip.AddrFromSlice(address.IP)
		if !ok || !publicWebhookAddress(ip.Unmap()) {
			return nil, nil, fmt.Errorf("webhook host %q resolves to forbidden address %s", host, address.IP)
		}
		validated = append(validated, ip.Unmap())
	}
	return parsed, validated, nil
}

func pinnedWebhookClient(parsed *url.URL, addresses []netip.Addr) *http.Client {
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var lastErr error
		for _, address := range addresses {
			var dialer net.Dialer
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(address.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func publicWebhookAddress(address netip.Addr) bool {
	if !address.IsValid() || address.IsUnspecified() || address.IsLoopback() || address.IsMulticast() || address.IsPrivate() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
		return false
	}
	for _, prefix := range forbiddenDocumentationPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var forbiddenDocumentationPrefixes = []netip.Prefix{
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func retryableStatus(status int) bool {
	if status == http.StatusRequestTimeout || status == http.StatusConflict || status == http.StatusTooEarly || status == http.StatusTooManyRequests {
		return true
	}
	return status >= 500 || status >= 300 && status < 400
}

func deliveryBackoff(attempts int) time.Duration {
	backoff := []time.Duration{time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 30 * time.Second}
	if attempts < len(backoff) {
		return backoff[attempts]
	}
	return time.Minute
}

func retryAfter(response *http.Response, now time.Time) time.Duration {
	raw := strings.TrimSpace(response.Header.Get("Retry-After"))
	if raw == "" || response.StatusCode != http.StatusTooManyRequests && response.StatusCode != http.StatusServiceUnavailable {
		return 0
	}
	if seconds, err := strconv.Atoi(raw); err == nil {
		duration := time.Duration(seconds) * time.Second
		if duration > 0 && duration <= 5*time.Minute {
			return duration
		}
		return 0
	}
	if date, err := http.ParseTime(raw); err == nil {
		duration := date.Sub(now)
		if duration > 0 && duration <= 5*time.Minute {
			return duration
		}
	}
	return 0
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
