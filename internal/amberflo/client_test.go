/*
Copyright 2026 Datum Technology Inc.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, version 3.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.
*/

package amberflo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer is a package-local, minimal Amberflo stand-in used across
// this package's tests. Tests configure it inline per case rather than
// through an admin API — closures over `fail*` fields let them inject
// 503/429/400 responses for the next N requests.
type fakeServer struct {
	srv    *httptest.Server
	apiKey string

	mu sync.Mutex

	customers map[string]*storedWireCustomer
	requests  []recordedRequest

	// failure injection
	failStatus     int
	failCount      int
	failRetryAfter int
}

// storedWireCustomer mirrors the wireCustomer shape for tests.
type storedWireCustomer struct {
	CustomerId    string            `json:"customerId"`
	CustomerName  string            `json:"customerName"`
	CustomerEmail string            `json:"customerEmail,omitempty"`
	Traits        map[string]string `json:"traits,omitempty"`
	Enabled       bool              `json:"enabled"`
	UpdateTime    int64             `json:"updateTime,omitempty"`
	CreateTime    int64             `json:"createTime,omitempty"`
}

type recordedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

func newFake(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{
		apiKey:    "unit-test-key",
		customers: map[string]*storedWireCustomer{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeServer) URL() string    { return f.srv.URL }
func (f *fakeServer) APIKey() string { return f.apiKey }

// seed installs a customer as if it had already been created. Tests call
// this to cover the "customer already exists" branch of EnsureCustomer.
func (f *fakeServer) seed(c storedWireCustomer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := c
	if cp.Traits != nil {
		cp.Traits = cloneStringMap(cp.Traits)
	}
	f.customers[c.CustomerId] = &cp
}

// armFailures queues the next `count` requests to return `status`. When
// retryAfter > 0 it is sent as a Retry-After header.
func (f *fakeServer) armFailures(status, count, retryAfter int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failStatus = status
	f.failCount = count
	f.failRetryAfter = retryAfter
}

// requestsCopy returns a snapshot of recorded requests.
func (f *fakeServer) requestsCopy() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()

	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: r.Header.Clone(),
		Body:    append([]byte(nil), body...),
	})
	if f.failCount > 0 {
		status := f.failStatus
		retryAfter := f.failRetryAfter
		f.failCount--
		f.mu.Unlock()
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		}
		http.Error(w, "armed failure", status)
		return
	}
	f.mu.Unlock()

	if r.Header.Get("X-API-KEY") != f.apiKey {
		http.Error(w, "forbidden", http.StatusUnauthorized)
		return
	}

	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/customers/"):
		id := strings.TrimPrefix(r.URL.Path, "/customers/")
		f.mu.Lock()
		c, ok := f.customers[id]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, c)

	case (r.Method == http.MethodPost || r.Method == http.MethodPut) &&
		strings.HasPrefix(r.URL.Path, "/customers"):
		var in storedWireCustomer
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if in.CustomerId == "" {
			http.Error(w, "customerId required", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		stored := &storedWireCustomer{
			CustomerId:    in.CustomerId,
			CustomerName:  in.CustomerName,
			CustomerEmail: in.CustomerEmail,
			Traits:        cloneStringMap(in.Traits),
			Enabled:       in.Enabled,
			UpdateTime:    time.Now().UnixMilli(),
		}
		f.customers[in.CustomerId] = stored
		out := *stored
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, out)

	default:
		http.Error(w, "not implemented", http.StatusNotImplemented)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// newTestClient returns a Client pointed at a fresh fake. Retries use an
// instant-sleep to keep tests deterministic.
func newTestClient(t *testing.T, opts ...func(*ClientOptions)) (Client, *fakeServer) {
	t.Helper()
	f := newFake(t)

	co := ClientOptions{
		BaseURL:         f.URL(),
		APIKey:          f.APIKey(),
		RateLimitPerSec: 1000,
		RetryAttempts:   4,
		sleep: func(ctx context.Context, _ time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return nil
		},
		jitter: func(d time.Duration) time.Duration { return d },
	}
	for _, o := range opts {
		o(&co)
	}
	c, err := NewClient(co)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c, f
}

func TestNewClient_RejectsEmptyAPIKey(t *testing.T) {
	_, err := NewClient(ClientOptions{BaseURL: "https://example.com", APIKey: ""})
	if err == nil {
		t.Fatalf("expected error for empty APIKey")
	}
	if !strings.Contains(err.Error(), "APIKey") {
		t.Errorf("error should mention APIKey: %v", err)
	}
}

func TestNewClient_RejectsInvalidBaseURL(t *testing.T) {
	_, err := NewClient(ClientOptions{BaseURL: "not-a-url", APIKey: "x"})
	if err == nil {
		t.Fatalf("expected error for invalid BaseURL")
	}
}

func TestNewClient_DefaultsBaseURL(t *testing.T) {
	c, err := NewClient(ClientOptions{APIKey: "x"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestClient_AuthHeaderPresentOnEveryCall(t *testing.T) {
	c, f := newTestClient(t)
	ctx := context.Background()
	f.seed(storedWireCustomer{CustomerId: "acct-1", CustomerName: "acct-1", Enabled: true})
	if _, err := c.GetCustomer(ctx, "acct-1"); err != nil {
		t.Fatalf("GetCustomer: %v", err)
	}
	reqs := f.requestsCopy()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 recorded request, got %d", len(reqs))
	}
	got := reqs[0].Headers.Get("X-Api-Key")
	if got != f.APIKey() {
		t.Errorf("X-API-KEY header missing/wrong: %v", reqs[0].Headers)
	}
}

func TestClient_RejectsMissingAPIKeyAtServer(t *testing.T) {
	f := newFake(t)
	c, err := NewClient(ClientOptions{
		BaseURL:       f.URL(),
		APIKey:        "wrong-key",
		RetryAttempts: 1,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.GetCustomer(context.Background(), "any")
	if err == nil {
		t.Fatalf("expected error for bad API key")
	}
	if !IsPermanent(err) {
		t.Errorf("expected permanent error for 401, got %v", err)
	}
}

func TestClient_RespectsRetryAfter(t *testing.T) {
	var sleeps []time.Duration
	c, f := newTestClient(t, func(co *ClientOptions) {
		co.sleep = func(ctx context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return ctx.Err()
		}
	})
	f.armFailures(429, 1, 1)

	if _, err := c.GetCustomer(context.Background(), "anything"); err != nil && !errors.Is(err, ErrCustomerNotFound) {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sleeps) == 0 {
		t.Fatalf("expected at least one sleep")
	}
	if sleeps[0] < time.Second {
		t.Errorf("expected first retry delay >= 1s for Retry-After, got %v", sleeps[0])
	}
}

func TestClient_NetworkErrorIsTransient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	c, err := NewClient(ClientOptions{
		BaseURL:       url,
		APIKey:        "k",
		RetryAttempts: 2,
		sleep:         func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = c.GetCustomer(context.Background(), "x")
	if err == nil {
		t.Fatalf("expected network error")
	}
	if !IsTransient(err) {
		t.Errorf("expected TransientError, got %T: %v", err, err)
	}
}
