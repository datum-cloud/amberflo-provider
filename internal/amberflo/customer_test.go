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
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

func baseDesired() DesiredCustomer {
	return DesiredCustomer{
		ID:           "acct-1",
		Name:         "Acct One",
		Email:        "billing@example.com",
		CurrencyCode: "USD",
	}
}

func methodCounts(reqs []recordedRequest) map[string]int {
	out := map[string]int{}
	for _, r := range reqs {
		out[r.Method]++
	}
	return out
}

func TestEnsureCustomer_CreatesWhenAbsent(t *testing.T) {
	c, f := newTestClient(t)
	got, err := c.EnsureCustomer(context.Background(), baseDesired())
	if err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	if got.ID != "acct-1" {
		t.Errorf("ID=%q, want acct-1", got.ID)
	}
	if got.Name != "Acct One" {
		t.Errorf("Name=%q", got.Name)
	}
	if got.Traits[traitCurrencyCode] != "USD" {
		t.Errorf("currencyCode trait=%q, want USD", got.Traits[traitCurrencyCode])
	}
	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodGet] != 1 {
		t.Errorf("expected 1 GET, got %d", counts[http.MethodGet])
	}
	if counts[http.MethodPost] != 1 {
		t.Errorf("expected 1 POST (create), got %d (all: %v)", counts[http.MethodPost], counts)
	}
}

func TestEnsureCustomer_NoopWhenEqual(t *testing.T) {
	c, f := newTestClient(t)
	wc := buildWireCustomer(baseDesired())
	f.seed(storedWireCustomer{
		CustomerId:    wc.CustomerId,
		CustomerName:  wc.CustomerName,
		CustomerEmail: wc.CustomerEmail,
		Traits:        wc.Traits,
		Enabled:       true,
	})

	if _, err := c.EnsureCustomer(context.Background(), baseDesired()); err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}

	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodPost] != 0 || counts[http.MethodPut] != 0 {
		t.Errorf("expected no writes, got POST=%d PUT=%d", counts[http.MethodPost], counts[http.MethodPut])
	}
	if counts[http.MethodGet] != 1 {
		t.Errorf("expected 1 GET, got %d", counts[http.MethodGet])
	}
}

func TestEnsureCustomer_UpdatesOnTraitDiff(t *testing.T) {
	c, f := newTestClient(t)
	f.seed(storedWireCustomer{
		CustomerId:    "acct-1",
		CustomerName:  "Acct One",
		CustomerEmail: "old@example.com",
		Traits:        map[string]string{"currencyCode": "USD"},
		Enabled:       true,
	})

	d := baseDesired()
	if _, err := c.EnsureCustomer(context.Background(), d); err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}

	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodPut] != 1 {
		t.Errorf("expected 1 PUT for update, got %d (all: %v)", counts[http.MethodPut], counts)
	}
	got, err := c.GetCustomer(context.Background(), "acct-1")
	if err != nil {
		t.Fatalf("GetCustomer: %v", err)
	}
	if got.Email != d.Email {
		t.Errorf("email not updated: got %q", got.Email)
	}
}

func TestEnsureCustomer_ProjectsEncoding(t *testing.T) {
	c, _ := newTestClient(t)
	d := baseDesired()
	d.Projects = []string{"a", "b"}
	got, err := c.EnsureCustomer(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	projects := got.Traits[traitProjects]
	if projects != `["a","b"]` {
		t.Errorf("projects trait=%q, want [\"a\",\"b\"]", projects)
	}
}

func TestEnsureCustomer_ProjectsDeterministic(t *testing.T) {
	c, _ := newTestClient(t)
	d := baseDesired()
	d.Projects = []string{"b", "a", "b"}
	got, err := c.EnsureCustomer(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	if got.Traits[traitProjects] != `["a","b"]` {
		t.Errorf("projects trait not normalized: %q", got.Traits[traitProjects])
	}
}

func TestEnsureCustomer_ProjectsEmptyOmitsTrait(t *testing.T) {
	c, _ := newTestClient(t)
	d := baseDesired()
	d.Projects = nil
	got, err := c.EnsureCustomer(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	if _, ok := got.Traits[traitProjects]; ok {
		t.Errorf("projects trait should be omitted when empty")
	}
}

func TestEnsureCustomer_PaymentTerms_Present(t *testing.T) {
	c, _ := newTestClient(t)
	d := baseDesired()
	d.PaymentTerms = &PaymentTerms{
		NetDays:           30,
		InvoiceFrequency:  "Monthly",
		InvoiceDayOfMonth: 1,
	}
	got, err := c.EnsureCustomer(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	if got.Traits[traitPaymentNetDays] != "30" {
		t.Errorf("netDays=%q", got.Traits[traitPaymentNetDays])
	}
	if got.Traits[traitPaymentInvoiceFreq] != "Monthly" {
		t.Errorf("invoiceFrequency=%q", got.Traits[traitPaymentInvoiceFreq])
	}
	if got.Traits[traitPaymentInvoiceDayOM] != "1" {
		t.Errorf("invoiceDayOfMonth=%q", got.Traits[traitPaymentInvoiceDayOM])
	}
}

func TestEnsureCustomer_PaymentTerms_Absent(t *testing.T) {
	c, _ := newTestClient(t)
	got, err := c.EnsureCustomer(context.Background(), baseDesired())
	if err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	for _, k := range []string{traitPaymentNetDays, traitPaymentInvoiceFreq, traitPaymentInvoiceDayOM} {
		if _, ok := got.Traits[k]; ok {
			t.Errorf("trait %q should be absent", k)
		}
	}
}

func TestEnsureCustomer_FallsBackNameToID(t *testing.T) {
	c, _ := newTestClient(t)
	d := baseDesired()
	d.Name = ""
	got, err := c.EnsureCustomer(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	if got.Name != d.ID {
		t.Errorf("Name=%q, want %q (fallback to ID)", got.Name, d.ID)
	}
}

func TestEnsureCustomer_ExtraTraitsPreserved(t *testing.T) {
	c, _ := newTestClient(t)
	d := baseDesired()
	d.ExtraTraits = map[string]string{"region": "us-east-1"}
	got, err := c.EnsureCustomer(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	if got.Traits["region"] != "us-east-1" {
		t.Errorf("extra trait lost: %v", got.Traits)
	}
}

func TestEnsureCustomer_ReservedKeysOverrideExtra(t *testing.T) {
	c, _ := newTestClient(t)
	d := baseDesired()
	d.ExtraTraits = map[string]string{traitCurrencyCode: "EUR"}
	got, err := c.EnsureCustomer(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	if got.Traits[traitCurrencyCode] != "USD" {
		t.Errorf("reserved key override failed: %q", got.Traits[traitCurrencyCode])
	}
}

func TestEnsureCustomer_EmptyIDIsPermanent(t *testing.T) {
	c, _ := newTestClient(t)
	d := baseDesired()
	d.ID = ""
	_, err := c.EnsureCustomer(context.Background(), d)
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	if !IsPermanent(err) {
		t.Errorf("expected PermanentError, got %T", err)
	}
}

func TestDisableCustomer_SoftDisables(t *testing.T) {
	c, f := newTestClient(t)
	f.seed(storedWireCustomer{
		CustomerId:   "acct-1",
		CustomerName: "Acct One",
		Traits:       map[string]string{"currencyCode": "USD"},
		Enabled:      true,
	})

	if err := c.DisableCustomer(context.Background(), "acct-1"); err != nil {
		t.Fatalf("DisableCustomer: %v", err)
	}

	got, err := c.GetCustomer(context.Background(), "acct-1")
	if err != nil {
		t.Fatalf("GetCustomer: %v", err)
	}
	if got.Enabled {
		t.Errorf("customer still enabled after DisableCustomer")
	}
	if got.Traits[traitEnabled] != "false" {
		t.Errorf("enabled trait=%q, want false", got.Traits[traitEnabled])
	}
	archivedAt := got.Traits[traitArchivedAt]
	if archivedAt == "" {
		t.Errorf("archived_at trait missing")
	}
	if _, err := time.Parse(time.RFC3339, archivedAt); err != nil {
		t.Errorf("archived_at not RFC3339: %q (%v)", archivedAt, err)
	}
}

func TestDisableCustomer_NotFoundIsNoop(t *testing.T) {
	c, f := newTestClient(t)
	if err := c.DisableCustomer(context.Background(), "does-not-exist"); err != nil {
		t.Fatalf("DisableCustomer on missing customer: want nil, got %v", err)
	}
	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodPut] != 0 || counts[http.MethodPost] != 0 {
		t.Errorf("no writes expected, got %v", counts)
	}
}

func TestDisableCustomer_EmptyIDIsPermanent(t *testing.T) {
	c, _ := newTestClient(t)
	err := c.DisableCustomer(context.Background(), "")
	if err == nil || !IsPermanent(err) {
		t.Fatalf("expected PermanentError, got %v", err)
	}
}

func TestEnsureCustomer_RetriesOnTransient(t *testing.T) {
	c, f := newTestClient(t)
	f.armFailures(503, 2, 0)

	if _, err := c.EnsureCustomer(context.Background(), baseDesired()); err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	reqs := f.requestsCopy()
	gets := 0
	for _, r := range reqs {
		if r.Method == http.MethodGet {
			gets++
		}
	}
	if gets != 3 {
		t.Errorf("expected 3 GET attempts (2 injected failures + 1 success), got %d", gets)
	}
}

func TestEnsureCustomer_PermanentOn4xx(t *testing.T) {
	c, f := newTestClient(t)
	f.armFailures(400, 1, 0)
	_, err := c.EnsureCustomer(context.Background(), baseDesired())
	if err == nil {
		t.Fatalf("expected permanent error")
	}
	if !IsPermanent(err) {
		t.Errorf("expected PermanentError, got %v", err)
	}
	if got := len(f.requestsCopy()); got != 1 {
		t.Errorf("expected exactly 1 request on permanent error, got %d", got)
	}
}

func TestEnsureCustomer_MissingAPIKey(t *testing.T) {
	_, err := NewClient(ClientOptions{APIKey: ""})
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "APIKey") {
		t.Errorf("error should reference APIKey: %v", err)
	}
}

func TestEnsureCustomer_ContextCanceled(t *testing.T) {
	c, f := newTestClient(t, func(co *ClientOptions) {
		co.sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
				return nil
			}
		}
	})
	f.armFailures(503, 100, 0)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	_, err := c.EnsureCustomer(ctx, baseDesired())
	if err == nil {
		t.Fatalf("expected context error")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestEnsureCustomer_RateLimited(t *testing.T) {
	var sleeps []time.Duration
	c, f := newTestClient(t, func(co *ClientOptions) {
		co.sleep = func(ctx context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return ctx.Err()
		}
	})
	f.armFailures(429, 1, 1)

	if _, err := c.EnsureCustomer(context.Background(), baseDesired()); err != nil {
		t.Fatalf("EnsureCustomer: %v", err)
	}
	if len(sleeps) == 0 {
		t.Fatal("expected sleep to be invoked")
	}
	if sleeps[0] < time.Second {
		t.Errorf("first sleep=%v, want >= 1s to honour Retry-After", sleeps[0])
	}
}

func TestBuildWireCustomer_DeterministicTraits(t *testing.T) {
	d1 := baseDesired()
	d1.Projects = []string{"p3", "p1", "p2"}

	d2 := baseDesired()
	d2.Projects = []string{"p1", "p2", "p3"}

	w1 := buildWireCustomer(d1)
	w2 := buildWireCustomer(d2)
	if w1.Traits[traitProjects] != w2.Traits[traitProjects] {
		t.Errorf("projects trait differs: %q vs %q", w1.Traits[traitProjects], w2.Traits[traitProjects])
	}
	var got []string
	if err := json.Unmarshal([]byte(w1.Traits[traitProjects]), &got); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("projects not sorted: %v", got)
	}
}

func TestGetCustomer_NotFoundSentinel(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.GetCustomer(context.Background(), "missing")
	if !errors.Is(err, ErrCustomerNotFound) {
		t.Errorf("expected ErrCustomerNotFound, got %v", err)
	}
}

func TestGetCustomer_EmptyIDIsPermanent(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.GetCustomer(context.Background(), "")
	if err == nil || !IsPermanent(err) {
		t.Fatalf("expected PermanentError, got %v", err)
	}
}
