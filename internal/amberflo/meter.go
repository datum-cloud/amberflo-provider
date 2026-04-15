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
	"fmt"
	"net/http"
	"net/url"
	"slices"
)

// DesiredMeter is the controller-facing representation of a meter the
// reconciler wants to exist in Amberflo. Mirrors the DesiredCustomer
// pattern: callers assemble this struct; wire encoding lives here.
type DesiredMeter struct {
	// APIName is the stable identifier used as Amberflo meterApiName. For
	// the amberflo-provider this is always string(MeterDefinition.UID) —
	// the caller-supplied ID path documented in the Amberflo API. The
	// Milo-side spec.meterName is NOT used as the API name because it is
	// a reverse-DNS path that can exceed Amberflo's character set
	// constraints for meter IDs.
	APIName string
	// Label is the human-readable display label. Sourced from
	// MeterDefinition.spec.displayName with a fallback to spec.meterName
	// so the Amberflo UI always has a non-empty label.
	Label string
	// MeterType is the Amberflo meterType wire value. Empirically only
	// three values are accepted on POST /meters today: "sum_of_all_usage",
	// "active_users", and "event_duration". Anything else is rejected
	// with HTTP 400 "Invalid meter type". The controller applies the
	// Milo→Amberflo translation (and filters unsupported values) before
	// calling EnsureMeter.
	MeterType string
	// AggregationDimensions is the ordered list of dimension keys the
	// meter aggregates over. Required (and non-empty) for the
	// active_users meterType — Amberflo uses these to identify the
	// "thing" being counted uniquely. For sum_of_all_usage it is sent as
	// an empty array.
	AggregationDimensions []string
	// Unit is the UCUM unit string passed through from
	// MeterDefinition.spec.measurement.unit. Amberflo treats this as an
	// opaque label; no client-side validation.
	Unit string
	// Dimensions is the ordered list of attribute keys downstream systems
	// may group by. Passed through from spec.measurement.dimensions.
	Dimensions []string
}

// Meter is the provider-facing view of an Amberflo meter record. Raw is
// the server response verbatim so the controller can log/debug the
// last-known Amberflo payload.
type Meter struct {
	ID                    string
	APIName               string
	Label                 string
	MeterType             string
	AggregationDimensions []string
	Unit                  string
	Dimensions            []string
	LockingStatus         string
	Raw                   json.RawMessage
}

// Amberflo meter lockingStatus lifecycle values. The state machine is
// one-way: open → close_to_changes | close_to_deletions | deprecated;
// close_to_changes → deprecated only. A meter must be in `deprecated`
// before DELETE will succeed.
const (
	// lockingStatusActive is the state the provider always reconciles
	// toward for a live meter. The UI surfaces it as "active" (vs.
	// "draft" for `open`). Despite the name, label + dimension PUTs
	// still succeed while a meter is in this state.
	lockingStatusActive = "close_to_changes"
	// lockingStatusDeprecated is the precondition Amberflo enforces
	// before a meter can be deleted. DeleteMeter flips the meter here
	// via a PUT before issuing DELETE.
	lockingStatusDeprecated = "deprecated"
)

// wireMeter mirrors Amberflo's JSON payload shape for a meter. Only the
// fields the provider reads or writes are declared; unknown keys are
// preserved via Meter.Raw if a caller needs them.
//
// Notable shape choices:
//   - `aggregation` is intentionally absent. The server populates and
//     owns it based on `meterType`; supplying it on POST is silently
//     ignored, and including it in our drift comparison would cause
//     spurious PUTs once the server back-fills it.
//   - `aggregationDimensions` and `dimensions` are NOT marked omitempty
//     so they always serialize as `[]` rather than disappearing when
//     empty — Amberflo rejects requests where these fields are missing
//     for some meterTypes, and `[]` is unambiguous.
//   - `lockingStatus` is always serialized. The provider always creates
//     meters in the active state (close_to_changes), and the delete
//     path flips to deprecated before issuing DELETE. An omitempty
//     value would leave newly-created meters in "draft" in the UI.
type wireMeter struct {
	ID                    string   `json:"id,omitempty"`
	Label                 string   `json:"label,omitempty"`
	MeterAPIName          string   `json:"meterApiName"`
	MeterType             string   `json:"meterType,omitempty"`
	AggregationDimensions []string `json:"aggregationDimensions"`
	Unit                  string   `json:"unit,omitempty"`
	Dimensions            []string `json:"dimensions"`
	UseInBilling          bool     `json:"useInBilling"`
	LockingStatus         string   `json:"lockingStatus"`
}

// GetMeter fetches a meter by its meterApiName. Returns ErrMeterNotFound
// when no such meter exists.
//
// Wire details: Amberflo's `GET /meters/{id}` route is keyed by the
// server-assigned UUID, not by the caller-supplied meterApiName. Since
// the provider only knows the meterApiName (= MeterDefinition.UID), we
// query the list-with-filter form `GET /meters?meterApiName=<x>` and
// return the first match. Empty response → NotFound.
func (c *client) GetMeter(ctx context.Context, meterAPIName string) (Meter, error) {
	if meterAPIName == "" {
		return Meter{}, &PermanentError{Err: errors.New("meterAPIName is required")}
	}

	path := "/meters?meterApiName=" + url.QueryEscape(meterAPIName)
	var wms []wireMeter
	_, body, err := c.doJSON(ctx, http.MethodGet, path, nil, &wms)
	if err != nil {
		var perm *PermanentError
		if errors.As(err, &perm) && perm.StatusCode == http.StatusNotFound {
			return Meter{}, fmt.Errorf("%w: %s", ErrMeterNotFound, meterAPIName)
		}
		return Meter{}, err
	}
	if len(wms) == 0 {
		return Meter{}, fmt.Errorf("%w: %s", ErrMeterNotFound, meterAPIName)
	}
	// Amberflo's list-with-filter returns every meter matching the
	// filter. meterApiName is globally unique per the API contract, so
	// we take the first match; if somehow >1 rows come back, log enough
	// to help diagnose but keep using the first (deterministic).
	if len(wms) > 1 {
		// Not ideal — surface via Raw so debug logs have the full payload.
		return meterFromWire(wms[0], body), nil
	}
	return meterFromWire(wms[0], body), nil
}

// EnsureMeter creates or updates the meter so Amberflo matches the
// DesiredMeter. The call is idempotent: if Amberflo already agrees, no
// write happens and only the GET is issued.
func (c *client) EnsureMeter(ctx context.Context, desired DesiredMeter) (Meter, error) {
	if desired.APIName == "" {
		return Meter{}, &PermanentError{Err: errors.New("DesiredMeter.APIName is required")}
	}
	if desired.MeterType == "" {
		return Meter{}, &PermanentError{Err: errors.New("DesiredMeter.MeterType is required")}
	}

	want := buildWireMeter(desired)

	existing, err := c.GetMeter(ctx, desired.APIName)
	switch {
	case errors.Is(err, ErrMeterNotFound):
		return c.putMeter(ctx, http.MethodPost, want)
	case err != nil:
		return Meter{}, err
	}

	if !meterNeedsUpdate(existing, want) {
		return existing, nil
	}

	// Update path. Empirically verified against app.amberflo.io: a PUT
	// to /meters without the server-assigned `id` in the body 400s with
	// "Meter already exists with meterApiName: …" because the API
	// treats the request as an upsert-create. The id must come from a
	// prior GET (Amberflo does NOT accept client-supplied ids on POST;
	// it mints its own UUID). existing.ID should always be populated
	// here because GetMeter just returned successfully.
	if existing.ID == "" {
		return Meter{}, &PermanentError{
			Err: fmt.Errorf("existing meter %s has no server id; cannot PUT", desired.APIName),
		}
	}
	want.ID = existing.ID
	return c.putMeter(ctx, http.MethodPut, want)
}

// DeleteMeter removes a meter keyed by its meterApiName.
//
// Wire details (all empirically verified against app.amberflo.io):
//   - DELETE is keyed by the server-assigned UUID, not meterApiName.
//     DELETE /meters/{meterApiName} returns 200 silently without
//     deleting. We resolve meterApiName→id via a GET first.
//   - DELETE is rejected with HTTP 400 ("'lockingStatus' X prevents
//     meter from being deleted") unless the meter is in the
//     `deprecated` lockingStatus. Since the provider keeps meters in
//     `close_to_changes` (active) during their lifetime, we PUT to
//     flip the state to `deprecated` before DELETE.
//   - PUT-to-deprecated is idempotent: walking the state to
//     `deprecated` when it is already `deprecated` returns 200 with no
//     side effect beyond updateTime.
//
// NotFound tolerance: any step returning NotFound is treated as
// success. The desired end state is "no meter in Amberflo"; any
// absence satisfies that goal. Mirrors DisableCustomer's pattern.
func (c *client) DeleteMeter(ctx context.Context, meterAPIName string) error {
	if meterAPIName == "" {
		return &PermanentError{Err: errors.New("meterAPIName is required")}
	}

	existing, err := c.GetMeter(ctx, meterAPIName)
	if err != nil {
		if errors.Is(err, ErrMeterNotFound) {
			return nil
		}
		return err
	}
	if existing.ID == "" {
		// Unexpected: GET succeeded but returned no server id. Treat
		// as NotFound so we do not wedge the finalizer — the next
		// reconcile will re-observe and retry if the record reappears.
		return nil
	}

	// Walk to `deprecated` unless we are already there. The live API
	// is idempotent either way, so the guard only saves a round-trip.
	if existing.LockingStatus != lockingStatusDeprecated {
		if err := c.deprecateMeter(ctx, existing); err != nil {
			if errors.Is(err, ErrMeterNotFound) {
				return nil
			}
			return err
		}
	}

	path := fmt.Sprintf("/meters/%s", url.PathEscape(existing.ID))
	_, _, err = c.doJSON(ctx, http.MethodDelete, path, nil, nil)
	if err != nil {
		var perm *PermanentError
		if errors.As(err, &perm) && perm.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	return nil
}

// deprecateMeter PUTs the existing meter with `lockingStatus:
// "deprecated"` so DELETE will subsequently succeed. All mutable
// fields are echoed from the existing record; only lockingStatus
// changes. NotFound is surfaced as ErrMeterNotFound so DeleteMeter can
// treat it as success.
func (c *client) deprecateMeter(ctx context.Context, existing Meter) error {
	wm := wireMeter{
		ID:                    existing.ID,
		Label:                 existing.Label,
		MeterAPIName:          existing.APIName,
		MeterType:             existing.MeterType,
		AggregationDimensions: append([]string{}, existing.AggregationDimensions...),
		Unit:                  existing.Unit,
		Dimensions:            append([]string{}, existing.Dimensions...),
		UseInBilling:          true,
		LockingStatus:         lockingStatusDeprecated,
	}
	_, _, err := c.doJSON(ctx, http.MethodPut, "/meters", wm, nil)
	if err != nil {
		var perm *PermanentError
		if errors.As(err, &perm) && perm.StatusCode == http.StatusNotFound {
			return fmt.Errorf("%w: %s", ErrMeterNotFound, existing.APIName)
		}
		return err
	}
	return nil
}

// putMeter issues a create/update against POST/PUT /meters, decoding
// the response into a Meter.
func (c *client) putMeter(ctx context.Context, method string, wm wireMeter) (Meter, error) {
	switch method {
	case http.MethodPost, http.MethodPut:
	default:
		return Meter{}, &PermanentError{Err: fmt.Errorf("unsupported method %q", method)}
	}
	var got wireMeter
	_, body, err := c.doJSON(ctx, method, "/meters", wm, &got)
	if err != nil {
		return Meter{}, err
	}
	// Servers occasionally echo an empty body; fall back to the request.
	if got.MeterAPIName == "" && got.ID == "" {
		got = wm
	}
	return meterFromWire(got, body), nil
}

// buildWireMeter renders a DesiredMeter into the Amberflo wire shape.
// Dimensions are copied verbatim (MeterDefinition preserves the caller's
// order in spec.measurement.dimensions, and Amberflo is order-sensitive
// for display). aggregationDimensions and dimensions are guaranteed
// non-nil so the JSON encoder emits `[]` rather than `null` — Amberflo
// rejects null for these fields on some meter types.
func buildWireMeter(d DesiredMeter) wireMeter {
	label := d.Label
	if label == "" {
		label = d.APIName
	}
	dims := append([]string{}, d.Dimensions...)
	aggDims := append([]string{}, d.AggregationDimensions...)
	return wireMeter{
		Label:                 label,
		MeterAPIName:          d.APIName,
		MeterType:             d.MeterType,
		AggregationDimensions: aggDims,
		Unit:                  d.Unit,
		Dimensions:            dims,
		UseInBilling:          true,
		// Always stamp active. Amberflo defaults to `open` (UI: "draft")
		// when the field is omitted, which is not the shape the provider
		// ever wants. On update, if an operator manually walked the
		// state forward to `deprecated` in the UI the server rejects a
		// back-flip; we let that 400 surface as a PermanentError so the
		// operator can investigate rather than silently masking it.
		LockingStatus: lockingStatusActive,
	}
}

// meterNeedsUpdate returns true when the existing Meter does not already
// match the desired wire representation. Comparison is limited to fields
// the provider actually manages — `aggregation` is server-controlled
// (derived from meterType), so it is intentionally omitted to avoid
// spurious PUTs once the server back-fills it.
//
// existing.ID (server-assigned UUID) is NOT compared against want —
// want never carries an id until the caller populates it from the GET
// response. Its presence is a sanity signal: an existing Meter
// returned by GetMeter should always have a non-empty ID; EnsureMeter
// refuses to issue a PUT otherwise.
func meterNeedsUpdate(existing Meter, want wireMeter) bool {
	if existing.APIName != want.MeterAPIName {
		return true
	}
	if existing.Label != want.Label {
		return true
	}
	if existing.MeterType != want.MeterType {
		return true
	}
	if existing.Unit != want.Unit {
		return true
	}
	if existing.LockingStatus != want.LockingStatus {
		return true
	}
	if !slices.Equal(existing.AggregationDimensions, want.AggregationDimensions) {
		return true
	}
	return !slices.Equal(existing.Dimensions, want.Dimensions)
}

// meterFromWire converts the wire representation plus raw body into the
// provider-facing Meter.
func meterFromWire(wm wireMeter, raw []byte) Meter {
	var rawCopy json.RawMessage
	if len(raw) > 0 {
		rawCopy = make(json.RawMessage, len(raw))
		copy(rawCopy, raw)
	}
	dims := append([]string(nil), wm.Dimensions...)
	aggDims := append([]string(nil), wm.AggregationDimensions...)
	return Meter{
		ID:                    wm.ID,
		APIName:               wm.MeterAPIName,
		Label:                 wm.Label,
		MeterType:             wm.MeterType,
		AggregationDimensions: aggDims,
		Unit:                  wm.Unit,
		Dimensions:            dims,
		LockingStatus:         wm.LockingStatus,
		Raw:                   rawCopy,
	}
}
