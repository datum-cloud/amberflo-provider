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
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// baseDesiredMeter returns a DesiredMeter with the fields every test
// cares about populated to non-empty defaults. Individual specs mutate
// one field at a time to isolate the behaviour they are exercising.
//
// Default shape mirrors what the controller produces for a Sum-aggregated
// MeterDefinition: meterType=sum_of_all_usage and an empty
// AggregationDimensions slice. UniqueCount-style fixtures should override
// MeterType + AggregationDimensions explicitly.
func baseDesiredMeter() DesiredMeter {
	return DesiredMeter{
		APIName:               "uid-cpu-1",
		Label:                 "CPU Seconds",
		MeterType:             "sum_of_all_usage",
		AggregationDimensions: nil,
		Unit:                  "s",
		Dimensions:            []string{"region", "tier"},
	}
}

// activeUsersDesiredMeter returns a DesiredMeter shaped like the
// controller's output for a UniqueCount aggregation.
func activeUsersDesiredMeter() DesiredMeter {
	return DesiredMeter{
		APIName:               "uid-active-1",
		Label:                 "Active Projects",
		MeterType:             "active_users",
		AggregationDimensions: []string{"project_id"},
		Unit:                  "{project}",
		Dimensions:            []string{"project_id"},
	}
}

// storedFromDesired renders a DesiredMeter into the storedWireMeter
// shape used by the fake's seed helper. Used so "meter already exists"
// tests can prime the fake with a record that byte-matches the wire
// that buildWireMeter would produce.
func storedFromDesired(d DesiredMeter) storedWireMeter {
	w := buildWireMeter(d)
	return storedWireMeter{
		ID:                    "fake-id-" + d.APIName,
		Label:                 w.Label,
		MeterAPIName:          w.MeterAPIName,
		MeterType:             w.MeterType,
		AggregationDimensions: append([]string(nil), w.AggregationDimensions...),
		Unit:                  w.Unit,
		Dimensions:            append([]string(nil), w.Dimensions...),
		UseInBilling:          w.UseInBilling,
		LockingStatus:         w.LockingStatus,
	}
}

func TestEnsureMeter_CreatesWhenAbsent(t *testing.T) {
	c, f := newTestClient(t)
	got, err := c.EnsureMeter(context.Background(), baseDesiredMeter())
	if err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	if got.APIName != "uid-cpu-1" {
		t.Errorf("APIName=%q, want uid-cpu-1", got.APIName)
	}
	if got.Label != "CPU Seconds" {
		t.Errorf("Label=%q", got.Label)
	}
	if got.MeterType != "sum_of_all_usage" {
		t.Errorf("MeterType=%q, want sum_of_all_usage", got.MeterType)
	}
	if got.LockingStatus != lockingStatusActive {
		t.Errorf("LockingStatus=%q, want %q", got.LockingStatus, lockingStatusActive)
	}
	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodGet] != 1 {
		t.Errorf("expected 1 GET, got %d", counts[http.MethodGet])
	}
	if counts[http.MethodPost] != 1 {
		t.Errorf("expected 1 POST (create), got %d (all: %v)", counts[http.MethodPost], counts)
	}
	if counts[http.MethodPut] != 0 {
		t.Errorf("expected 0 PUT on create, got %d", counts[http.MethodPut])
	}

	// Regression: the POST body MUST carry `lockingStatus:
	// "close_to_changes"`. Without it Amberflo defaults the meter to
	// `open` (UI: "draft"), which is exactly the bug that motivated
	// this change.
	var post recordedRequest
	for _, r := range f.requestsCopy() {
		if r.Method == http.MethodPost && r.Path == "/meters" {
			post = r
		}
	}
	if post.Method == "" {
		t.Fatalf("no POST /meters observed")
	}
	if !strings.Contains(string(post.Body), `"lockingStatus":"close_to_changes"`) {
		t.Errorf("POST body missing lockingStatus:close_to_changes: %s", string(post.Body))
	}
}

func TestEnsureMeter_ActiveUsers_RoundTripsAggregationDimensions(t *testing.T) {
	c, f := newTestClient(t)
	got, err := c.EnsureMeter(context.Background(), activeUsersDesiredMeter())
	if err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	if got.MeterType != "active_users" {
		t.Errorf("MeterType=%q", got.MeterType)
	}
	if !slices.Equal(got.AggregationDimensions, []string{"project_id"}) {
		t.Errorf("AggregationDimensions=%v", got.AggregationDimensions)
	}

	// Confirm what we POSTed actually carried aggregationDimensions on
	// the wire — this is the regression we are guarding against.
	reqs := f.requestsCopy()
	var post recordedRequest
	for _, r := range reqs {
		if r.Method == http.MethodPost && r.Path == "/meters" {
			post = r
		}
	}
	if post.Method == "" {
		t.Fatalf("no POST /meters observed")
	}
	bodyStr := string(post.Body)
	if !strings.Contains(bodyStr, `"aggregationDimensions":["project_id"]`) {
		t.Errorf("POST body missing aggregationDimensions: %s", bodyStr)
	}
	if strings.Contains(bodyStr, `"aggregation":`) {
		t.Errorf("POST body must not include client-side aggregation: %s", bodyStr)
	}
}

func TestEnsureMeter_NoopWhenEqual(t *testing.T) {
	c, f := newTestClient(t)
	f.seedMeter(storedFromDesired(baseDesiredMeter()))

	if _, err := c.EnsureMeter(context.Background(), baseDesiredMeter()); err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}

	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodPost] != 0 || counts[http.MethodPut] != 0 {
		t.Errorf("expected no writes, got POST=%d PUT=%d", counts[http.MethodPost], counts[http.MethodPut])
	}
	// GET /meters plus GET product-items/list so a leftover item can
	// be retargeted after a previous create-without-billing.
	if counts[http.MethodGet] != 2 {
		t.Errorf("expected 2 GETs, got %d", counts[http.MethodGet])
	}
}

func TestEnsureMeter_UpdatesOnLabelDrift(t *testing.T) {
	c, f := newTestClient(t)
	seed := storedFromDesired(baseDesiredMeter())
	seed.Label = "Old Label"
	f.seedMeter(seed)

	if _, err := c.EnsureMeter(context.Background(), baseDesiredMeter()); err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}

	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodPut] != 1 {
		t.Errorf("expected 1 PUT for label drift, got %d (all: %v)", counts[http.MethodPut], counts)
	}
	stored, ok := f.fetchMeter("uid-cpu-1")
	if !ok {
		t.Fatalf("meter missing from fake after update")
	}
	if stored.Label != "CPU Seconds" {
		t.Errorf("label not updated: %q", stored.Label)
	}

	// Regression guard: the PUT body MUST include the server id. The
	// live API 400s with "already exists" when id is missing, so an
	// implementation that forgets to populate it would only show up in
	// e2e — this assertion catches it in unit tests.
	var put recordedRequest
	for _, r := range f.requestsCopy() {
		if r.Method == http.MethodPut && r.Path == "/meters" {
			put = r
		}
	}
	if put.Method == "" {
		t.Fatalf("no PUT /meters observed")
	}
	if !strings.Contains(string(put.Body), `"id":"fake-id-uid-cpu-1"`) {
		t.Errorf("PUT body missing server id: %s", string(put.Body))
	}
	if !strings.Contains(string(put.Body), `"lockingStatus":"close_to_changes"`) {
		t.Errorf("PUT body missing lockingStatus:close_to_changes: %s", string(put.Body))
	}
}

func TestEnsureMeter_UpdatesOnDimensionAdd(t *testing.T) {
	c, f := newTestClient(t)
	seed := storedFromDesired(baseDesiredMeter())
	// Seed a meter with only one dimension; the desired state adds a
	// second. Order-preserving drift detection must flag this.
	seed.Dimensions = []string{"region"}
	f.seedMeter(seed)

	if _, err := c.EnsureMeter(context.Background(), baseDesiredMeter()); err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}

	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodPut] != 1 {
		t.Errorf("expected 1 PUT for dimension add, got %d (all: %v)", counts[http.MethodPut], counts)
	}
	stored, _ := f.fetchMeter("uid-cpu-1")
	if !reflect.DeepEqual(stored.Dimensions, []string{"region", "tier"}) {
		t.Errorf("dimensions not updated: %v", stored.Dimensions)
	}
}

func TestEnsureMeter_UpdatesOnUnitChange(t *testing.T) {
	c, f := newTestClient(t)
	seed := storedFromDesired(baseDesiredMeter())
	seed.Unit = "ms"
	f.seedMeter(seed)

	if _, err := c.EnsureMeter(context.Background(), baseDesiredMeter()); err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}

	if got := methodCounts(f.requestsCopy())[http.MethodPut]; got != 1 {
		t.Errorf("expected 1 PUT for unit change, got %d", got)
	}
	stored, _ := f.fetchMeter("uid-cpu-1")
	if stored.Unit != "s" {
		t.Errorf("unit not updated: %q", stored.Unit)
	}
}

func TestEnsureMeter_UpdatesOnAggregationDimensionsDrift(t *testing.T) {
	c, f := newTestClient(t)
	seed := storedFromDesired(activeUsersDesiredMeter())
	// Wipe the aggregationDimensions on the stored record; the desired
	// state still wants ["project_id"], so a PUT should issue.
	seed.AggregationDimensions = nil
	f.seedMeter(seed)

	if _, err := c.EnsureMeter(context.Background(), activeUsersDesiredMeter()); err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	if got := methodCounts(f.requestsCopy())[http.MethodPut]; got != 1 {
		t.Errorf("expected 1 PUT for aggregationDimensions drift, got %d", got)
	}
	stored, _ := f.fetchMeter("uid-active-1")
	if !slices.Equal(stored.AggregationDimensions, []string{"project_id"}) {
		t.Errorf("aggregationDimensions not updated: %v", stored.AggregationDimensions)
	}
}

func TestEnsureMeter_EmptyAPINameIsPermanent(t *testing.T) {
	c, _ := newTestClient(t)
	d := baseDesiredMeter()
	d.APIName = ""
	_, err := c.EnsureMeter(context.Background(), d)
	if err == nil || !IsPermanent(err) {
		t.Fatalf("expected PermanentError, got %v", err)
	}
}

func TestEnsureMeter_EmptyMeterTypeIsPermanent(t *testing.T) {
	c, _ := newTestClient(t)
	d := baseDesiredMeter()
	d.MeterType = ""
	_, err := c.EnsureMeter(context.Background(), d)
	if err == nil || !IsPermanent(err) {
		t.Fatalf("expected PermanentError, got %v", err)
	}
}

func TestEnsureMeter_AdoptsOnLabelCollision(t *testing.T) {
	c, f := newTestClient(t)
	legacy := storedFromDesired(baseDesiredMeter())
	legacy.MeterAPIName = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	legacy.ID = "fake-id-" + legacy.MeterAPIName
	legacy.Label = "CPU Seconds"
	f.seedMeter(legacy)

	d := baseDesiredMeter()
	d.APIName = "cpu-seconds"
	got, err := c.EnsureMeter(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	if got.APIName != "cpu-seconds" {
		t.Errorf("APIName=%q, want cpu-seconds", got.APIName)
	}
	if got.ID == legacy.ID {
		t.Errorf("expected a new Amberflo server id after delete+create, still %q", got.ID)
	}
	if _, ok := f.fetchMeter(legacy.MeterAPIName); ok {
		t.Error("expected leftover UID-keyed meter to be deleted")
	}
	if stored, ok := f.fetchMeter("cpu-seconds"); !ok || stored.Label != "CPU Seconds (cpu-seconds)" {
		t.Errorf("replacement meter missing under unique label: ok=%v stored=%+v", ok, stored)
	}

	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodPost] != 2 {
		t.Errorf("expected 2 POSTs (rejected create, then recreate), got %d", counts[http.MethodPost])
	}
	if counts[http.MethodDelete] != 1 {
		t.Errorf("expected 1 DELETE of leftover meter, got %d", counts[http.MethodDelete])
	}
}

func TestEnsureMeter_RejectsForeignLabelCollision(t *testing.T) {
	c, f := newTestClient(t)
	other := storedFromDesired(baseDesiredMeter())
	other.MeterAPIName = "someone-elses-meter"
	other.Label = "CPU Seconds"
	f.seedMeter(other)

	d := baseDesiredMeter()
	d.APIName = "cpu-seconds"
	_, err := c.EnsureMeter(context.Background(), d)
	if err == nil || !IsPermanent(err) {
		t.Fatalf("expected PermanentError, got %v", err)
	}
	if _, ok := f.fetchMeter("someone-elses-meter"); !ok {
		t.Error("foreign meter should be left in place")
	}
}

func TestEnsureMeter_RetargetsOrphanDeprecatedProductItem(t *testing.T) {
	c, f := newTestClient(t)
	f.seedProductItem(wireProductItem{
		ID:              "item-alb-requests",
		ProductItemName: "CPU Seconds",
		MeterAPIName:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		ProductID:       "1",
		LockingStatus:   lockingStatusDeprecated,
	})
	f.seedProductPlan(wireProductPlan{
		ID:              "payg-v1",
		ProductPlanName: "Pay As You Go",
		ProductItemPriceIdsMap: map[string]string{
			"item-alb-requests": "payg-v1--alb-requests",
		},
	})

	d := baseDesiredMeter()
	d.APIName = "cpu-seconds"
	got, err := c.EnsureMeter(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	if got.APIName != "cpu-seconds" {
		t.Errorf("APIName=%q, want cpu-seconds", got.APIName)
	}
	item, ok := f.fetchProductItem("item-alb-requests")
	if !ok {
		t.Fatal("expected leftover product item to be kept")
	}
	if item.MeterAPIName != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("immutable leftover meterApiName=%q, want original UID", item.MeterAPIName)
	}
	if item.LockingStatus != lockingStatusDeprecated {
		t.Errorf("leftover lockingStatus=%q, want deprecated", item.LockingStatus)
	}
	stored, ok := f.fetchMeter("cpu-seconds")
	if !ok || stored.Label != "CPU Seconds (cpu-seconds)" {
		t.Errorf("replacement meter missing under unique label: ok=%v stored=%+v", ok, stored)
	}
	if stored.UseInBilling {
		t.Error("expected POST without useInBilling so Amberflo does not mint a second product item")
	}

	var postedWithoutBilling bool
	for _, req := range f.requestsCopy() {
		if req.Method == http.MethodPost && req.Path == "/meters" &&
			strings.Contains(string(req.Body), `"useInBilling":false`) {
			postedWithoutBilling = true
		}
		if req.Method == http.MethodDelete && strings.Contains(req.Path, "product-items") {
			t.Fatal("must not DELETE a plan-attached product item")
		}
	}
	if !postedWithoutBilling {
		t.Error("expected a meter POST with useInBilling=false")
	}
}

func TestEnsureMeter_RetargetsOrphanOpenProductItem(t *testing.T) {
	c, f := newTestClient(t)
	f.seedProductItem(wireProductItem{
		ID:              "item-uptime",
		ProductItemName: "CPU Seconds",
		MeterAPIName:    "c031ea42-b516-4f2c-bbb4-b0dee4299f35",
		ProductID:       "1",
		LockingStatus:   lockingStatusActive,
	})

	d := baseDesiredMeter()
	d.APIName = "cpu-seconds"
	if _, err := c.EnsureMeter(context.Background(), d); err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	item, ok := f.fetchProductItem("item-uptime")
	if !ok {
		t.Fatal("expected leftover product item to be kept")
	}
	if item.MeterAPIName != "cpu-seconds" {
		t.Errorf("product item meterApiName=%q, want cpu-seconds", item.MeterAPIName)
	}
}

func TestEnsureMeter_AdoptsOrphanProductItemWhenMeterAlreadyExists(t *testing.T) {
	c, f := newTestClient(t)
	existing := storedFromDesired(baseDesiredMeter())
	existing.MeterAPIName = "cpu-seconds"
	existing.ID = "fake-id-cpu-seconds"
	f.seedMeter(existing)
	f.seedProductItem(wireProductItem{
		ID:              "item-alb-requests",
		ProductItemName: "CPU Seconds",
		MeterAPIName:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		ProductID:       "1",
		LockingStatus:   lockingStatusActive,
	})

	d := baseDesiredMeter()
	d.APIName = "cpu-seconds"
	if _, err := c.EnsureMeter(context.Background(), d); err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	item, ok := f.fetchProductItem("item-alb-requests")
	if !ok {
		t.Fatal("expected leftover product item to be kept")
	}
	if item.MeterAPIName != "cpu-seconds" {
		t.Errorf("product item meterApiName=%q, want cpu-seconds", item.MeterAPIName)
	}
}

func TestEnsureMeter_RejectsProductItemWithLiveMeter(t *testing.T) {
	c, f := newTestClient(t)
	live := storedFromDesired(baseDesiredMeter())
	live.MeterAPIName = "someone-elses-meter"
	live.Label = "Other Label"
	live.ID = "fake-id-" + live.MeterAPIName
	f.seedMeter(live)
	f.seedProductItem(wireProductItem{
		ID:              "item-foreign",
		ProductItemName: "CPU Seconds",
		MeterAPIName:    live.MeterAPIName,
		ProductID:       "1",
		LockingStatus:   lockingStatusActive,
	})

	d := baseDesiredMeter()
	d.APIName = "cpu-seconds"
	_, err := c.EnsureMeter(context.Background(), d)
	if err == nil || !IsPermanent(err) {
		t.Fatalf("expected PermanentError, got %v", err)
	}
	if _, ok := f.fetchProductItem("item-foreign"); !ok {
		t.Error("foreign product item should be left in place")
	}
	if _, ok := f.fetchMeter(live.MeterAPIName); !ok {
		t.Error("live meter should be left in place")
	}
}

func TestEnsureMeter_PostsUniqueLabelWhenGETByLabelMisses(t *testing.T) {
	c, f := newTestClient(t)
	f.occupyLabel("CPU Seconds")

	d := baseDesiredMeter()
	d.APIName = "cpu-seconds"
	got, err := c.EnsureMeter(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	if got.APIName != "cpu-seconds" {
		t.Errorf("APIName=%q, want cpu-seconds", got.APIName)
	}
	if got.Label != "CPU Seconds (cpu-seconds)" {
		t.Errorf("Label=%q, want disambiguated form", got.Label)
	}
	stored, ok := f.fetchMeter("cpu-seconds")
	if !ok {
		t.Fatal("expected meter under stable meterApiName")
	}
	if stored.UseInBilling {
		t.Error("expected POST without useInBilling")
	}

	var originalLabelPosts, uniqueLabelPosts int
	for _, req := range f.requestsCopy() {
		if req.Method != http.MethodPost || req.Path != "/meters" {
			continue
		}
		body := string(req.Body)
		switch {
		case strings.Contains(body, `"label":"CPU Seconds (cpu-seconds)"`):
			uniqueLabelPosts++
		case strings.Contains(body, `"label":"CPU Seconds"`):
			originalLabelPosts++
		}
	}
	if originalLabelPosts != 1 {
		t.Errorf("expected 1 initial POST with original label, got %d", originalLabelPosts)
	}
	if uniqueLabelPosts != 1 {
		t.Errorf("expected 1 POST with unique label after GET miss, got %d", uniqueLabelPosts)
	}
}

func TestEnsureMeter_DoesNotRewriteDisambiguatedLabel(t *testing.T) {
	c, f := newTestClient(t)
	existing := storedFromDesired(baseDesiredMeter())
	existing.MeterAPIName = "cpu-seconds"
	existing.ID = "fake-id-cpu-seconds"
	existing.Label = "CPU Seconds (cpu-seconds)"
	f.seedMeter(existing)
	f.occupyLabel("CPU Seconds")

	d := baseDesiredMeter()
	d.APIName = "cpu-seconds"
	if _, err := c.EnsureMeter(context.Background(), d); err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	if got := methodCounts(f.requestsCopy())[http.MethodPut]; got != 0 {
		t.Errorf("expected no PUT rewriting unique label back to occupied original, got %d", got)
	}
	stored, _ := f.fetchMeter("cpu-seconds")
	if stored.Label != "CPU Seconds (cpu-seconds)" {
		t.Errorf("Label=%q", stored.Label)
	}
}

func TestEnsureMeter_PreservesDisambiguatedLabelOnOtherDrift(t *testing.T) {
	c, f := newTestClient(t)
	existing := storedFromDesired(baseDesiredMeter())
	existing.MeterAPIName = "cpu-seconds"
	existing.ID = "fake-id-cpu-seconds"
	existing.Label = "CPU Seconds (cpu-seconds)"
	existing.Unit = "ms"
	f.seedMeter(existing)
	f.occupyLabel("CPU Seconds")

	d := baseDesiredMeter()
	d.APIName = "cpu-seconds"
	if _, err := c.EnsureMeter(context.Background(), d); err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	stored, _ := f.fetchMeter("cpu-seconds")
	if stored.Label != "CPU Seconds (cpu-seconds)" {
		t.Errorf("Label=%q, PUT must not restore the occupied original", stored.Label)
	}
	if stored.Unit != "s" {
		t.Errorf("Unit=%q, want s", stored.Unit)
	}
}

func TestEnsureMeter_CreatesWhenLeftoverHasInvalidLongMeterAPIName(t *testing.T) {
	c, f := newTestClient(t)
	longName := "assistant-miloapis-com-conversation-cache-read-tokens"
	f.seedProductItem(wireProductItem{
		ID:              longName,
		ProductItemName: "Cached prompt tokens",
		MeterAPIName:    longName,
		ProductID:       "1",
		LockingStatus:   "open",
	})

	d := baseDesiredMeter()
	d.APIName = longName
	d.Label = "Cached prompt tokens"
	got, err := c.EnsureMeter(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	want := MeterAPIName(longName)
	if got.APIName != want {
		t.Errorf("APIName=%q, want hashed %q", got.APIName, want)
	}
}

func TestEnsureMeter_HashesAPINameWhenTooLong(t *testing.T) {
	c, f := newTestClient(t)
	d := baseDesiredMeter()
	d.APIName = strings.Repeat("a", maxMeterAPINameLen+1)
	got, err := c.EnsureMeter(context.Background(), d)
	if err != nil {
		t.Fatalf("EnsureMeter: %v", err)
	}
	want := MeterAPIName(d.APIName)
	if got.APIName != want {
		t.Errorf("APIName=%q, want hashed %q", got.APIName, want)
	}
	if len(got.APIName) > maxMeterAPINameLen {
		t.Errorf("hashed APIName length %d exceeds %d", len(got.APIName), maxMeterAPINameLen)
	}
	if _, ok := f.fetchMeter(want); !ok {
		t.Error("expected meter stored under hashed meterApiName")
	}
}

func TestMeterAPIName_PassThroughAndHash(t *testing.T) {
	short := "networking-datumapis-com-gateway-requests"
	if got := MeterAPIName(short); got != short {
		t.Errorf("short name: got %q want %q", got, short)
	}
	long := "networking-datumapis-com-gateway-connection-seconds"
	if len(long) <= maxMeterAPINameLen {
		t.Fatalf("fixture %q is not longer than %d", long, maxMeterAPINameLen)
	}
	got := MeterAPIName(long)
	if len(got) != 40 {
		t.Errorf("hashed length=%d, want 40", len(got))
	}
	if got == long {
		t.Error("expected long name to be hashed")
	}
	if MeterAPIName(long) != got {
		t.Error("hash must be stable")
	}
	other := MeterAPIName("assistant-miloapis-com-conversation-cache-read-tokens")
	if other == got {
		t.Error("distinct long names must hash differently")
	}
}

func TestDisambiguatedMeterLabel_FitsCap(t *testing.T) {
	got := disambiguatedMeterLabel("ALB Requests", "networking-datumapis-com-gateway-requests")
	if got != "ALB Requests (networking-datumapis-com-gateway-requests)" {
		t.Errorf("got %q", got)
	}
	longLabel := strings.Repeat("x", maxMeterLabelLen)
	got = disambiguatedMeterLabel(longLabel, "cpu-seconds")
	if len(got) > maxMeterLabelLen {
		t.Errorf("length %d exceeds %d: %q", len(got), maxMeterLabelLen, got)
	}
	if !strings.HasSuffix(got, " (cpu-seconds)") {
		t.Errorf("expected truncated label to keep apiName suffix, got %q", got)
	}
}

func TestGetMeterByLabel_ExactMatch(t *testing.T) {
	c, f := newTestClient(t)
	f.seedMeter(storedFromDesired(baseDesiredMeter()))
	got, err := c.GetMeterByLabel(context.Background(), "CPU Seconds")
	if err != nil {
		t.Fatalf("GetMeterByLabel: %v", err)
	}
	if got.APIName != "uid-cpu-1" {
		t.Errorf("APIName=%q", got.APIName)
	}

	reqs := f.requestsCopy()
	if len(reqs) != 1 {
		t.Fatalf("expected 1 GET, got %d", len(reqs))
	}
	if reqs[0].Method != http.MethodGet || reqs[0].Path != "/meters" {
		t.Errorf("got %s %s", reqs[0].Method, reqs[0].Path)
	}
	q, err := url.ParseQuery(reqs[0].Query)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got := q.Get("label"); got != "CPU Seconds" {
		t.Errorf("label query=%q, want CPU Seconds", got)
	}
}

func TestGetMeterByLabel_NotFound(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.GetMeterByLabel(context.Background(), "missing")
	if !errors.Is(err, ErrMeterNotFound) {
		t.Errorf("expected ErrMeterNotFound, got %v", err)
	}
}

func TestGetMeterByLabel_MultipleMatchesArePermanent(t *testing.T) {
	c, f := newTestClient(t)
	a := storedFromDesired(baseDesiredMeter())
	a.MeterAPIName = "meter-a"
	a.Label = "Shared"
	b := storedFromDesired(baseDesiredMeter())
	b.MeterAPIName = "meter-b"
	b.Label = "Shared"
	f.seedMeter(a)
	f.seedMeter(b)

	_, err := c.GetMeterByLabel(context.Background(), "Shared")
	if err == nil || !IsPermanent(err) {
		t.Fatalf("expected PermanentError, got %v", err)
	}
}

func TestDeleteMeter_FallsBackToLegacyUIDByLabel(t *testing.T) {
	c, f := newTestClient(t)
	legacy := storedFromDesired(baseDesiredMeter())
	legacy.MeterAPIName = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	legacy.ID = "fake-id-" + legacy.MeterAPIName
	legacy.Label = "CPU Seconds"
	f.seedMeter(legacy)

	if err := c.DeleteMeter(context.Background(), "cpu-seconds", "CPU Seconds"); err != nil {
		t.Fatalf("DeleteMeter: %v", err)
	}
	if _, ok := f.fetchMeter(legacy.MeterAPIName); ok {
		t.Error("expected leftover UID-keyed meter to be deleted")
	}
}

func TestDeleteMeter_DoesNotDeleteForeignLabelMatch(t *testing.T) {
	c, f := newTestClient(t)
	other := storedFromDesired(baseDesiredMeter())
	other.MeterAPIName = "someone-elses-meter"
	other.Label = "CPU Seconds"
	f.seedMeter(other)

	if err := c.DeleteMeter(context.Background(), "cpu-seconds", "CPU Seconds"); err != nil {
		t.Fatalf("DeleteMeter: %v", err)
	}
	if _, ok := f.fetchMeter("someone-elses-meter"); !ok {
		t.Error("foreign meter should be left in place")
	}
}

func TestGetMeter_HappyPath(t *testing.T) {
	c, f := newTestClient(t)
	f.seedMeter(storedFromDesired(baseDesiredMeter()))

	got, err := c.GetMeter(context.Background(), "uid-cpu-1")
	if err != nil {
		t.Fatalf("GetMeter: %v", err)
	}
	if got.APIName != "uid-cpu-1" {
		t.Errorf("APIName=%q", got.APIName)
	}
	if got.Label != "CPU Seconds" {
		t.Errorf("Label=%q", got.Label)
	}
	if got.Unit != "s" {
		t.Errorf("Unit=%q", got.Unit)
	}
	if !reflect.DeepEqual(got.Dimensions, []string{"region", "tier"}) {
		t.Errorf("Dimensions=%v", got.Dimensions)
	}
	if got.MeterType != "sum_of_all_usage" {
		t.Errorf("MeterType=%q", got.MeterType)
	}
}

func TestGetMeter_NotFoundSentinel(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.GetMeter(context.Background(), "missing")
	if !errors.Is(err, ErrMeterNotFound) {
		t.Errorf("expected ErrMeterNotFound, got %v", err)
	}
}

func TestGetMeter_EmptyAPINameIsPermanent(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.GetMeter(context.Background(), "")
	if err == nil || !IsPermanent(err) {
		t.Fatalf("expected PermanentError, got %v", err)
	}
}

func TestGetMeter_PermanentOn4xx(t *testing.T) {
	c, f := newTestClient(t)
	// 400 is unambiguously permanent (404 is translated to
	// ErrMeterNotFound, so we inject a different 4xx here).
	f.armFailures(400, 1, 0)
	_, err := c.GetMeter(context.Background(), "any")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !IsPermanent(err) {
		t.Errorf("expected PermanentError, got %T: %v", err, err)
	}
}

func TestGetMeter_TransientOn5xx(t *testing.T) {
	c, f := newTestClient(t)
	// Exhaust all retries with 503s so the final outcome is transient.
	f.armFailures(503, 10, 0)
	_, err := c.GetMeter(context.Background(), "any")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !IsTransient(err) {
		t.Errorf("expected TransientError, got %T: %v", err, err)
	}
}

func TestDeleteMeter_HappyPath_DeprecatesThenDeletesByServerID(t *testing.T) {
	c, f := newTestClient(t)
	f.seedMeter(storedFromDesired(baseDesiredMeter()))

	if err := c.DeleteMeter(context.Background(), "uid-cpu-1", ""); err != nil {
		t.Fatalf("DeleteMeter: %v", err)
	}
	if _, ok := f.fetchMeter("uid-cpu-1"); ok {
		t.Errorf("meter still present after DeleteMeter")
	}

	// Sequence the requests the client emitted. We expect the
	// three-step flow: GET /meters?… → PUT /meters (with deprecated)
	// → DELETE /meters/<server id>. Any deviation breaks the live-API
	// contract (POST-to-deprecate is not a thing; DELETE on a
	// non-deprecated meter 400s).
	reqs := f.requestsCopy()
	if len(reqs) < 3 {
		t.Fatalf("expected >=3 requests (GET, PUT, DELETE), got %d: %v", len(reqs), reqs)
	}
	if reqs[0].Method != http.MethodGet || !strings.HasPrefix(reqs[0].Path, "/meters") {
		t.Errorf("step 1 should be GET /meters?…; got %s %s", reqs[0].Method, reqs[0].Path)
	}
	if reqs[1].Method != http.MethodPut || reqs[1].Path != "/meters" {
		t.Errorf("step 2 should be PUT /meters; got %s %s", reqs[1].Method, reqs[1].Path)
	}
	if !strings.Contains(string(reqs[1].Body), `"lockingStatus":"deprecated"`) {
		t.Errorf("step 2 PUT body must include lockingStatus:deprecated: %s", string(reqs[1].Body))
	}
	if !strings.Contains(string(reqs[1].Body), `"id":"fake-id-uid-cpu-1"`) {
		t.Errorf("step 2 PUT body must include server id: %s", string(reqs[1].Body))
	}
	if reqs[2].Method != http.MethodDelete || reqs[2].Path != "/meters/fake-id-uid-cpu-1" {
		t.Errorf("step 3 should be DELETE /meters/<server id>; got %s %s",
			reqs[2].Method, reqs[2].Path)
	}
}

func TestDeleteMeter_SkipsDeprecateWhenAlreadyDeprecated(t *testing.T) {
	c, f := newTestClient(t)
	seed := storedFromDesired(baseDesiredMeter())
	seed.LockingStatus = "deprecated"
	f.seedMeter(seed)

	if err := c.DeleteMeter(context.Background(), "uid-cpu-1", ""); err != nil {
		t.Fatalf("DeleteMeter: %v", err)
	}
	// Only GET + DELETE. No PUT — the guard saves a round-trip when
	// the meter is already in the right state.
	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodPut] != 0 {
		t.Errorf("expected 0 PUTs when already deprecated, got %d", counts[http.MethodPut])
	}
	if counts[http.MethodDelete] != 1 {
		t.Errorf("expected 1 DELETE, got %d", counts[http.MethodDelete])
	}
}

func TestDeleteMeter_FakeRejectsDeleteWhileActive(t *testing.T) {
	// Sanity check that the fake actually enforces the lockingStatus
	// precondition — i.e. an implementation that forgot the
	// deprecate step would fail this test. We hit the DELETE endpoint
	// directly here via a raw request rather than c.DeleteMeter so we
	// observe the 400.
	f := newFake(t)
	seed := storedFromDesired(baseDesiredMeter())
	// Seed in the live-state we want to probe.
	seed.LockingStatus = lockingStatusActive
	f.seedMeter(seed)

	req, err := http.NewRequest(http.MethodDelete, f.URL()+"/meters/fake-id-uid-cpu-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-KEY", f.APIKey())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("raw DELETE: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 deleting active meter, got %d", resp.StatusCode)
	}
}

func TestDeleteMeter_NotFoundTolerated(t *testing.T) {
	c, f := newTestClient(t)
	// Nothing seeded — GET returns []; DeleteMeter must treat that as
	// success so the reconciler's finalizer release path is idempotent.
	if err := c.DeleteMeter(context.Background(), "does-not-exist", ""); err != nil {
		t.Fatalf("DeleteMeter on missing meter: want nil, got %v", err)
	}
	// Only the GET probe runs — no DELETE since we never resolved an id.
	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodDelete] != 0 {
		t.Errorf("expected 0 DELETEs, got %d", counts[http.MethodDelete])
	}
	if counts[http.MethodGet] != 1 {
		t.Errorf("expected 1 GET probe, got %d", counts[http.MethodGet])
	}
}

func TestDeleteMeter_DoesNotUseMeterAPINameAsDeletePathKey(t *testing.T) {
	// Regression: an earlier iteration DELETEd /meters/{meterApiName},
	// which the live API 200s on silently without deleting. Guard
	// against regression by asserting the DELETE path contains the
	// server id segment ("fake-id-…") and NOT the meterApiName.
	c, f := newTestClient(t)
	f.seedMeter(storedFromDesired(baseDesiredMeter()))

	if err := c.DeleteMeter(context.Background(), "uid-cpu-1", ""); err != nil {
		t.Fatalf("DeleteMeter: %v", err)
	}
	for _, r := range f.requestsCopy() {
		if r.Method != http.MethodDelete {
			continue
		}
		if r.Path == "/meters/uid-cpu-1" {
			t.Errorf("DELETE used meterApiName in path; live API would no-op: %s", r.Path)
		}
	}
}

func TestDeleteMeter_TransientOn5xx(t *testing.T) {
	c, f := newTestClient(t)
	f.armFailures(503, 10, 0)
	err := c.DeleteMeter(context.Background(), "uid-cpu-1", "")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !IsTransient(err) {
		t.Errorf("expected TransientError, got %T: %v", err, err)
	}
}

func TestDeleteMeter_EmptyAPINameIsPermanent(t *testing.T) {
	c, _ := newTestClient(t)
	err := c.DeleteMeter(context.Background(), "", "")
	if err == nil || !IsPermanent(err) {
		t.Fatalf("expected PermanentError, got %v", err)
	}
}

func TestMeterNeedsUpdate_FieldMatrix(t *testing.T) {
	base := Meter{
		APIName:               "uid-x",
		Label:                 "L",
		MeterType:             "sum_of_all_usage",
		AggregationDimensions: []string{},
		Unit:                  "s",
		Dimensions:            []string{"a", "b"},
		LockingStatus:         lockingStatusActive,
	}
	want := wireMeter{
		Label:                 "L",
		MeterAPIName:          "uid-x",
		MeterType:             "sum_of_all_usage",
		AggregationDimensions: []string{},
		Unit:                  "s",
		Dimensions:            []string{"a", "b"},
		UseInBilling:          true,
		LockingStatus:         lockingStatusActive,
	}

	// Equal shapes are a no-op.
	if meterNeedsUpdate(base, want) {
		t.Errorf("equal shapes should not need update")
	}

	disambiguated := base
	disambiguated.Label = disambiguatedMeterLabel(want.Label, want.MeterAPIName)
	if meterNeedsUpdate(disambiguated, want) {
		t.Errorf("disambiguated label should not need update")
	}

	// Each field that should trigger an update.
	cases := []struct {
		name string
		mut  func(*Meter, *wireMeter)
	}{
		{"label differs", func(m *Meter, _ *wireMeter) { m.Label = "other" }},
		{"meterType differs", func(m *Meter, _ *wireMeter) { m.MeterType = "active_users" }},
		{"unit differs", func(m *Meter, _ *wireMeter) { m.Unit = "ms" }},
		{"apiName differs", func(m *Meter, _ *wireMeter) { m.APIName = "uid-y" }},
		{"dimension added", func(m *Meter, _ *wireMeter) { m.Dimensions = []string{"a"} }},
		{"dimension reordered", func(m *Meter, _ *wireMeter) { m.Dimensions = []string{"b", "a"} }},
		{"aggregationDimensions added", func(m *Meter, _ *wireMeter) {
			m.AggregationDimensions = []string{"x"}
		}},
		{"lockingStatus differs", func(m *Meter, _ *wireMeter) {
			// Defensive: if an operator manually flipped the state
			// back to "open" in the UI we want to re-sync.
			m.LockingStatus = "open"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			existing := base
			existing.Dimensions = append([]string(nil), base.Dimensions...)
			existing.AggregationDimensions = append([]string(nil), base.AggregationDimensions...)
			w := want
			w.Dimensions = append([]string(nil), want.Dimensions...)
			w.AggregationDimensions = append([]string(nil), want.AggregationDimensions...)
			tc.mut(&existing, &w)
			if !meterNeedsUpdate(existing, w) {
				t.Errorf("%s: expected update to be needed", tc.name)
			}
		})
	}
}

func TestMeterNeedsUpdate_IgnoresAggregationField(t *testing.T) {
	// `aggregation` is server-controlled (derived from meterType). The
	// drift comparison must not look at it, or every PUT after the
	// server's first echo would re-trigger a no-op write loop.
	base := Meter{
		APIName:    "uid-x",
		Label:      "L",
		MeterType:  "sum_of_all_usage",
		Unit:       "s",
		Dimensions: []string{"a"},
	}
	want := wireMeter{
		Label:        "L",
		MeterAPIName: "uid-x",
		MeterType:    "sum_of_all_usage",
		Unit:         "s",
		Dimensions:   []string{"a"},
		UseInBilling: true,
	}
	if meterNeedsUpdate(base, want) {
		t.Errorf("drift comparison should ignore the server-controlled aggregation field")
	}
}

func TestBuildWireMeter_LabelFallback(t *testing.T) {
	d := baseDesiredMeter()
	d.Label = ""
	w := buildWireMeter(d)
	if w.Label != d.APIName {
		t.Errorf("Label=%q, want fallback to APIName %q", w.Label, d.APIName)
	}
	if w.MeterType != "sum_of_all_usage" {
		t.Errorf("MeterType=%q, want sum_of_all_usage", w.MeterType)
	}
	if !w.UseInBilling {
		t.Errorf("UseInBilling should always be true on outbound writes")
	}
	if w.LockingStatus != lockingStatusActive {
		t.Errorf("LockingStatus=%q, want %q on outbound writes",
			w.LockingStatus, lockingStatusActive)
	}
}

func TestBuildWireMeter_NilDimensionsSerializeAsEmptyArray(t *testing.T) {
	d := baseDesiredMeter()
	d.Dimensions = nil
	d.AggregationDimensions = nil
	w := buildWireMeter(d)
	if w.Dimensions == nil {
		t.Errorf("Dimensions must be non-nil so JSON renders as [] not null")
	}
	if w.AggregationDimensions == nil {
		t.Errorf("AggregationDimensions must be non-nil so JSON renders as [] not null")
	}

	// Confirm the encoded JSON shape matches what Amberflo expects.
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)
	if !strings.Contains(s, `"dimensions":[]`) {
		t.Errorf("expected dimensions:[] in body: %s", s)
	}
	if !strings.Contains(s, `"aggregationDimensions":[]`) {
		t.Errorf("expected aggregationDimensions:[] in body: %s", s)
	}
}

func TestBuildWireMeter_DimensionsDefensiveCopy(t *testing.T) {
	d := baseDesiredMeter()
	d.Dimensions = []string{"a", "b"}
	w := buildWireMeter(d)
	// Mutate the caller's slice after building — the wire payload must
	// not observe the mutation.
	d.Dimensions[0] = "mutated"
	if w.Dimensions[0] != "a" {
		t.Errorf("wireMeter.Dimensions aliased caller's slice: %v", w.Dimensions)
	}
}

func TestBuildWireMeter_OmitsClientAggregation(t *testing.T) {
	// Regression: the wire shape must NOT include an `aggregation` key.
	// Amberflo silently ignores it, but we want to keep request bodies
	// honest about which fields the provider actually owns.
	w := buildWireMeter(baseDesiredMeter())
	raw, err := json.Marshal(w)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"aggregation":`) {
		t.Errorf("wire body must not include aggregation: %s", string(raw))
	}
}

func TestMeterFromWire_PopulatesFieldsAndRaw(t *testing.T) {
	payload := `{"id":"m1","label":"L","meterApiName":"uid-x","meterType":"sum_of_all_usage","aggregationDimensions":["d1"],"unit":"s","dimensions":["r","t"],"useInBilling":true}`
	var wm wireMeter
	if err := json.Unmarshal([]byte(payload), &wm); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := meterFromWire(wm, []byte(payload))
	if got.ID != "m1" || got.APIName != "uid-x" || got.MeterType != "sum_of_all_usage" {
		t.Errorf("unexpected conversion: %+v", got)
	}
	if !reflect.DeepEqual(got.Dimensions, []string{"r", "t"}) {
		t.Errorf("Dimensions=%v", got.Dimensions)
	}
	if !reflect.DeepEqual(got.AggregationDimensions, []string{"d1"}) {
		t.Errorf("AggregationDimensions=%v", got.AggregationDimensions)
	}
	if string(got.Raw) != payload {
		t.Errorf("Raw body not captured verbatim: %q", string(got.Raw))
	}
}

func TestMeterFromWire_NilDimensionsAndEmptyRaw(t *testing.T) {
	wm := wireMeter{MeterAPIName: "uid-x", MeterType: "sum_of_all_usage"}
	got := meterFromWire(wm, nil)
	if len(got.Dimensions) != 0 {
		t.Errorf("Dimensions=%v, want empty", got.Dimensions)
	}
	if len(got.AggregationDimensions) != 0 {
		t.Errorf("AggregationDimensions=%v, want empty", got.AggregationDimensions)
	}
	if got.Raw != nil {
		t.Errorf("Raw=%v, want nil for empty input", got.Raw)
	}
}
