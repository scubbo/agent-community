// Package communityclient implements the remote discussion HTTP protocol for
// the CLI and stdio MCP server.
package communityclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackjackson/agent-community/internal/connection"
	"github.com/jackjackson/agent-community/internal/discussion"
)

const MaxResponseBodyBytes = discussion.MaxBodyBytes*2 + 32*1024

type Client struct {
	http *http.Client
}

type APIError struct {
	Status int
	Code   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("agent-community API returned %d: %s", e.Status, e.Code)
}

type PostInput struct {
	IdempotencyKey string
	Body           string
	ReplyTo        string
}

type ReadOptions struct {
	AfterSequence int
	Limit         int
	Wait          time.Duration
}

type ReadResult struct {
	Messages     []discussion.Message `json:"messages"`
	LastSequence int                  `json:"last_sequence"`
	Status       discussion.Status    `json:"status"`
}

type GetResult struct {
	Discussion *discussion.Discussion              `json:"discussion"`
	Self       discussion.AuthenticatedParticipant `json:"self"`
}

func New(client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 35 * time.Second}
	}
	return &Client{http: client}
}

func (c *Client) Post(ctx context.Context, target *connection.Connection, input PostInput) (*discussion.Message, bool, error) {
	payload := struct {
		IdempotencyKey string `json:"idempotency_key"`
		Body           string `json:"body"`
		ReplyTo        string `json:"reply_to,omitempty"`
	}{IdempotencyKey: input.IdempotencyKey, Body: input.Body, ReplyTo: input.ReplyTo}
	var response struct {
		OK      bool                `json:"ok"`
		Message *discussion.Message `json:"message"`
	}
	status, err := c.request(ctx, target, http.MethodPost, "/messages", nil, payload, &response)
	if err != nil {
		return nil, false, err
	}
	if !response.OK || response.Message == nil {
		return nil, false, errors.New("invalid post response")
	}
	return response.Message, status == http.StatusOK, nil
}

func (c *Client) Read(ctx context.Context, target *connection.Connection, opts ReadOptions) (*ReadResult, error) {
	query := make(url.Values)
	if opts.AfterSequence != 0 {
		query.Set("after_sequence", strconv.Itoa(opts.AfterSequence))
	}
	if opts.Limit != 0 {
		query.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.Wait != 0 {
		query.Set("wait", opts.Wait.String())
	}
	var response struct {
		OK bool `json:"ok"`
		ReadResult
	}
	if _, err := c.request(ctx, target, http.MethodGet, "/messages", query, nil, &response); err != nil {
		return nil, err
	}
	if !response.OK || response.Messages == nil {
		return nil, errors.New("invalid read response")
	}
	return &response.ReadResult, nil
}

func (c *Client) Get(ctx context.Context, target *connection.Connection) (*GetResult, error) {
	var response struct {
		OK         bool `json:"ok"`
		Discussion struct {
			*discussion.Discussion
			Self discussion.AuthenticatedParticipant `json:"self"`
		} `json:"discussion"`
	}
	if _, err := c.request(ctx, target, http.MethodGet, "", nil, nil, &response); err != nil {
		return nil, err
	}
	if !response.OK || response.Discussion.Discussion == nil || response.Discussion.Self.ID == "" {
		return nil, errors.New("invalid metadata response")
	}
	return &GetResult{Discussion: response.Discussion.Discussion, Self: response.Discussion.Self}, nil
}

func (c *Client) End(ctx context.Context, target *connection.Connection) (*discussion.Discussion, error) {
	var response struct {
		OK         bool                   `json:"ok"`
		Discussion *discussion.Discussion `json:"discussion"`
	}
	if _, err := c.request(ctx, target, http.MethodDelete, "", nil, nil, &response); err != nil {
		return nil, err
	}
	if !response.OK || response.Discussion == nil {
		return nil, errors.New("invalid end response")
	}
	return response.Discussion, nil
}

func (c *Client) request(ctx context.Context, target *connection.Connection, method, suffix string, query url.Values, payload, destination any) (int, error) {
	if target == nil || target.LocalURL == "" || target.DiscussionID == "" || target.Capability == "" {
		return 0, errors.New("invalid local discussion connection")
	}
	endpoint := strings.TrimSuffix(target.LocalURL, "/") + "/v1/discussions/" + url.PathEscape(target.DiscussionID) + suffix
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "Bearer "+target.Capability)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBodyBytes+1))
	if err != nil {
		return response.StatusCode, err
	}
	if len(data) > MaxResponseBodyBytes {
		return response.StatusCode, errors.New("agent-community API response exceeds limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiResponse struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := decodeStrict(data, &apiResponse); err != nil || apiResponse.Error == "" {
			return response.StatusCode, fmt.Errorf("agent-community API returned HTTP %d", response.StatusCode)
		}
		return response.StatusCode, &APIError{Status: response.StatusCode, Code: apiResponse.Error}
	}
	if err := decodeStrict(data, destination); err != nil {
		return response.StatusCode, fmt.Errorf("decode agent-community API response: %w", err)
	}
	return response.StatusCode, nil
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
