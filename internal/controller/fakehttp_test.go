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

package controller

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// recordingFakeServer is a minimal Amberflo stand-in used by the envtest
// controller suite. It implements just enough of the /customers surface
// for the reconciler to drive end-to-end tests without reaching a real
// API. It is intentionally self-contained — the earlier fake sub-package
// is gone; tests build inline fakes like this one.
type recordingFakeServer struct {
	srv    *httptest.Server
	apiKey string

	mu         sync.Mutex
	customers  map[string]*storedCustomer
	requests   []recordedRequest
	failStatus int
	failCount  int
}

// storedCustomer mirrors the Amberflo customer wire shape the client uses.
type storedCustomer struct {
	CustomerId    string            `json:"customerId"`
	CustomerName  string            `json:"customerName"`
	CustomerEmail string            `json:"customerEmail,omitempty"`
	Enabled       bool              `json:"enabled"`
	Traits        map[string]string `json:"traits,omitempty"`
}

// recordedRequest captures a request for post-hoc inspection by tests.
type recordedRequest struct {
	Method string
	Path   string
	Body   []byte
}

// newRecordingFakeServer returns a started fake.
func newRecordingFakeServer() *recordingFakeServer {
	f := &recordingFakeServer{
		apiKey:    "envtest-api-key",
		customers: map[string]*storedCustomer{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// URL returns the base URL of the fake server.
func (f *recordingFakeServer) URL() string { return f.srv.URL }

// APIKey returns the accepted API key value for X-API-KEY.
func (f *recordingFakeServer) APIKey() string { return f.apiKey }

// Close tears down the fake server.
func (f *recordingFakeServer) Close() { f.srv.Close() }

// Reset clears state between tests.
func (f *recordingFakeServer) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.customers = map[string]*storedCustomer{}
	f.requests = nil
	f.failStatus = 0
	f.failCount = 0
}

// ArmFailures causes the next `count` API requests to return `status`.
func (f *recordingFakeServer) ArmFailures(status, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failStatus = status
	f.failCount = count
}

// FetchCustomer returns a deep copy of a stored customer or false.
func (f *recordingFakeServer) FetchCustomer(id string) (storedCustomer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.customers[id]
	if !ok {
		return storedCustomer{}, false
	}
	return *c, true
}

// Requests returns a snapshot of all requests the fake has served.
func (f *recordingFakeServer) Requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *recordingFakeServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()

	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Body:   append([]byte(nil), body...),
	})
	// Apply armed failure count first so auth/normal paths don't mask it.
	if f.failCount > 0 {
		status := f.failStatus
		f.failCount--
		f.mu.Unlock()
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
		(r.URL.Path == "/customers"):
		var in storedCustomer
		if err := json.Unmarshal(body, &in); err != nil {
			http.Error(w, fmt.Sprintf("bad json: %v", err), http.StatusBadRequest)
			return
		}
		if in.CustomerId == "" {
			http.Error(w, "customerId required", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.customers[in.CustomerId] = &storedCustomer{
			CustomerId:    in.CustomerId,
			CustomerName:  in.CustomerName,
			CustomerEmail: in.CustomerEmail,
			Enabled:       in.Enabled,
			Traits:        cloneMap(in.Traits),
		}
		out := *f.customers[in.CustomerId]
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

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}
