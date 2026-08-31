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
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// DesiredMeter is the controller-facing representation of a meter the
// reconciler wants to exist in Amberflo. Mirrors the DesiredCustomer
// pattern: callers assemble this struct; wire encoding lives here.
type DesiredMeter struct {
	// APIName is the stable identifier used as Amberflo meterApiName. For
	// the amberflo-provider this is MeterDefinition.metadata.name so a
	// fan-out delete-recreate keeps the same Amberflo key even when the
	// object UID changes. Amberflo limits this to 50 characters.
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

	// maxMeterAPINameLen is Amberflo's documented meterApiName limit.
	maxMeterAPINameLen = 50
	// maxMeterLabelLen caps disambiguated labels when leftover Amberflo
	// objects occupy the original display name. POST /meters still 400s
	// on those labels even when GET /meters?label= returns nothing.
	maxMeterLabelLen = 100
)

// MeterAPIName returns the Amberflo meterApiName for a MeterDefinition
// metadata.name. Names that already fit the 50-character limit pass
// through. Longer names are hashed to a stable 40-character hex SHA-1
// so POST /meters does not 400 on length. Ingest and Offer indexing
// must use the same helper so usage lands on the meter EnsureMeter
// created.
func MeterAPIName(name string) string {
	if len(name) <= maxMeterAPINameLen {
		return name
	}
	//nolint:gosec // Stable short id for Amberflo's 50-char cap, not a security hash.
	sum := sha1.Sum([]byte(name))
	return hex.EncodeToString(sum[:])
}

// disambiguatedMeterLabel keeps the original display name recognizable
// while remaining unique when that label is occupied by leftover
// Amberflo objects that GET /meters?label= cannot see.
func disambiguatedMeterLabel(label, apiName string) string {
	if label == "" {
		label = apiName
	}
	suffix := " (" + apiName + ")"
	if len(label)+len(suffix) <= maxMeterLabelLen {
		return label + suffix
	}
	keep := maxMeterLabelLen - len(suffix)
	if keep < 1 {
		if len(apiName) <= maxMeterLabelLen {
			return apiName
		}
		return apiName[:maxMeterLabelLen]
	}
	return label[:keep] + suffix
}

// meterLabelAcceptable reports whether the stored label is the desired
// display name or the disambiguated form written after a collision.
// Rewriting a unique label back to the occupied original 400s.
func meterLabelAcceptable(existing, want, apiName string) bool {
	return existing == want || existing == disambiguatedMeterLabel(want, apiName)
}

// k8sUIDMeterAPIName matches MeterDefinition.UID values previously used
// as Amberflo meterApiName (UUID form).
var k8sUIDMeterAPIName = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

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
// the provider only knows the meterApiName (= MeterDefinition.metadata.name), we
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

// GetMeterByLabel fetches a meter by its human-readable label. Labels are
// unique in Amberflo. Returns ErrMeterNotFound when no meter has that
// label. More than one match is a PermanentError.
//
// Wire details: GET /meters with no query is documented, but on staging
// that list omitted meters whose labels still collided on POST. GetMeter
// already uses the undocumented list-with-filter form
// GET /meters?meterApiName=<x>; this does the same for label. Results
// are still exact-matched so a prefix or substring filter cannot adopt
// the wrong row.
func (c *client) GetMeterByLabel(ctx context.Context, label string) (Meter, error) {
	if label == "" {
		return Meter{}, &PermanentError{Err: errors.New("label is required")}
	}

	path := "/meters?label=" + url.QueryEscape(label)
	var wms []wireMeter
	_, body, err := c.doJSON(ctx, http.MethodGet, path, nil, &wms)
	if err != nil {
		var perm *PermanentError
		if errors.As(err, &perm) && perm.StatusCode == http.StatusNotFound {
			return Meter{}, fmt.Errorf("%w: label %q", ErrMeterNotFound, label)
		}
		return Meter{}, err
	}

	var matches []wireMeter
	for _, wm := range wms {
		if wm.Label == label {
			matches = append(matches, wm)
		}
	}
	switch len(matches) {
	case 0:
		return Meter{}, fmt.Errorf("%w: label %q", ErrMeterNotFound, label)
	case 1:
		return meterFromWire(matches[0], body), nil
	default:
		return Meter{}, &PermanentError{
			Err: fmt.Errorf("multiple meters have label %q", label),
		}
	}
}

// EnsureMeter creates or updates the meter so Amberflo matches the
// DesiredMeter. The call is idempotent: if Amberflo already agrees, no
// write happens and only the GET is issued.
func (c *client) EnsureMeter(ctx context.Context, desired DesiredMeter) (Meter, error) {
	if desired.APIName == "" {
		return Meter{}, &PermanentError{Err: errors.New("DesiredMeter.APIName is required")}
	}
	desired.APIName = MeterAPIName(desired.APIName)
	if desired.MeterType == "" {
		return Meter{}, &PermanentError{Err: errors.New("DesiredMeter.MeterType is required")}
	}

	want := buildWireMeter(desired)

	existing, err := c.GetMeter(ctx, desired.APIName)
	switch {
	case errors.Is(err, ErrMeterNotFound):
		meter, err := c.putMeter(ctx, http.MethodPost, want)
		if err == nil {
			return meter, nil
		}
		if !isMeterLabelExistsError(err) {
			return Meter{}, err
		}
		return c.replaceLegacyMeter(ctx, want)
	case err != nil:
		return Meter{}, err
	}

	if !meterNeedsUpdate(existing, want) {
		if err := c.adoptOrphanProductItem(ctx, want.Label, want.MeterAPIName); err != nil {
			return Meter{}, err
		}
		return existing, nil
	}

	if existing.ID == "" {
		return Meter{}, &PermanentError{
			Err: fmt.Errorf("existing meter %s has no server id; cannot PUT", desired.APIName),
		}
	}
	want.ID = existing.ID
	if meterLabelAcceptable(existing.Label, want.Label, want.MeterAPIName) {
		want.Label = existing.Label
	}
	meter, err := c.putMeter(ctx, http.MethodPut, want)
	if err != nil {
		return Meter{}, err
	}
	if err := c.adoptOrphanProductItem(ctx, want.Label, want.MeterAPIName); err != nil {
		return Meter{}, err
	}
	return meter, nil
}

// replaceLegacyMeter handles POST /meters rejecting a duplicate label.
// Amberflo forbids changing meterApiName once a meter leaves `open`
// (close_to_deletion forbids the rename; close_to_changes forbids any
// modification). We never PUT a rename.
//
// A leftover UID-keyed meter is deleted, then the stable-named meter is
// created. GET /meters?label= often misses even when POST still 400s on
// that label, so the create path never retries the original label. A
// leftover product item can still occupy the display name; those are
// reused by pointing the existing item at the new meterApiName. A
// collision with another live metadata.name is a PermanentError.
func (c *client) replaceLegacyMeter(ctx context.Context, want wireMeter) (Meter, error) {
	existing, err := c.GetMeterByLabel(ctx, want.Label)
	switch {
	case errors.Is(err, ErrMeterNotFound):
		return c.createMeterReusingProductItem(ctx, want)
	case err != nil:
		return Meter{}, fmt.Errorf("label collision but meter lookup failed: %w", err)
	}
	if existing.APIName == want.MeterAPIName {
		if existing.ID == "" {
			return Meter{}, &PermanentError{
				Err: fmt.Errorf("existing meter %s has no server id; cannot PUT", want.MeterAPIName),
			}
		}
		want.ID = existing.ID
		meter, err := c.putMeter(ctx, http.MethodPut, want)
		if err != nil {
			return Meter{}, err
		}
		if err := c.adoptOrphanProductItem(ctx, want.Label, want.MeterAPIName); err != nil {
			return Meter{}, err
		}
		return meter, nil
	}
	if !isLegacyUIDMeterAPIName(existing.APIName) {
		return Meter{}, &PermanentError{
			Err: fmt.Errorf("meter with label %q already exists as meterApiName %q", want.Label, existing.APIName),
		}
	}
	if err := c.deleteMeterRecord(ctx, existing); err != nil {
		return Meter{}, fmt.Errorf("deleting leftover meter %s: %w", existing.APIName, err)
	}
	return c.createMeterReusingProductItem(ctx, want)
}

// createMeterReusingProductItem POSTs a meter with a unique label and
// useInBilling=false, then retargets any leftover product item that
// still holds the original display name. Occupied labels like
// "ALB Requests" stay dead; ingest keys on meterApiName.
func (c *client) createMeterReusingProductItem(ctx context.Context, want wireMeter) (Meter, error) {
	item, found, err := c.productItemNamed(ctx, want.Label)
	if err != nil {
		return Meter{}, fmt.Errorf("label collision: %w", err)
	}
	if found {
		if err := c.rejectLiveForeignProductItem(ctx, item, want.MeterAPIName); err != nil {
			return Meter{}, err
		}
	}

	create := want
	create.Label = disambiguatedMeterLabel(want.Label, want.MeterAPIName)
	create.UseInBilling = false
	meter, err := c.putMeter(ctx, http.MethodPost, create)
	if err != nil {
		return Meter{}, err
	}
	if found {
		if err := c.pointProductItemAtMeter(ctx, item, want.MeterAPIName); err != nil {
			if IsTransient(err) {
				return Meter{}, fmt.Errorf("label collision: %w", err)
			}
			// Staging leftovers are often deprecated RelationTargets and
			// cannot be retargeted. The meter is already created; ingest
			// keys on meterApiName, so a dead leftover item is fine.
		}
	}
	return meter, nil
}

// adoptOrphanProductItem retargets a leftover product item onto a meter
// that already exists. A live foreign meter is left alone so a working
// EnsureMeter does not fail closed on someone else's item.
func (c *client) adoptOrphanProductItem(ctx context.Context, label, meterAPIName string) error {
	if label == "" {
		return nil
	}
	item, found, err := c.productItemNamed(ctx, label)
	if err != nil || !found {
		return err
	}
	if err := c.rejectLiveForeignProductItem(ctx, item, meterAPIName); err != nil {
		return nil
	}
	if err := c.pointProductItemAtMeter(ctx, item, meterAPIName); err != nil && IsTransient(err) {
		return err
	}
	return nil
}

func (c *client) productItemNamed(ctx context.Context, label string) (wireProductItem, bool, error) {
	items, err := c.listProductItems(ctx)
	if err != nil {
		return wireProductItem{}, false, fmt.Errorf("listing product items: %w", err)
	}
	var matches []wireProductItem
	for _, item := range items {
		if item.ProductItemName == label {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return wireProductItem{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		return wireProductItem{}, false, &PermanentError{
			Err: fmt.Errorf("multiple product items have name %q", label),
		}
	}
}

// productItemForMeter returns the product item already pointed at
// meterAPIName. Amberflo assigns UUID ids on create, so GET-by-id using
// the stable meterApiName misses these leftovers.
func (c *client) productItemForMeter(ctx context.Context, meterAPIName string) (wireProductItem, bool, error) {
	if meterAPIName == "" {
		return wireProductItem{}, false, nil
	}
	items, err := c.listProductItems(ctx)
	if err != nil {
		return wireProductItem{}, false, fmt.Errorf("listing product items: %w", err)
	}
	var matches []wireProductItem
	for _, item := range items {
		if item.MeterAPIName == meterAPIName {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return wireProductItem{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		var live []wireProductItem
		for _, m := range matches {
			if m.LockingStatus != lockingStatusDeprecated {
				live = append(live, m)
			}
		}
		if len(live) == 1 {
			return live[0], true, nil
		}
		return wireProductItem{}, false, &PermanentError{
			Err: fmt.Errorf("multiple product items have meter %q", meterAPIName),
		}
	}
}

func (c *client) rejectLiveForeignProductItem(ctx context.Context, item wireProductItem, wantAPIName string) error {
	if item.MeterAPIName == "" || item.MeterAPIName == wantAPIName {
		return nil
	}
	if len(item.MeterAPIName) > maxMeterAPINameLen || isLegacyUIDMeterAPIName(item.MeterAPIName) {
		return nil
	}
	meter, err := c.GetMeter(ctx, item.MeterAPIName)
	if errors.Is(err, ErrMeterNotFound) || IsPermanent(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if meter.APIName == wantAPIName || isLegacyUIDMeterAPIName(meter.APIName) {
		return nil
	}
	return &PermanentError{
		Err: fmt.Errorf("product item %q still has meter %q", item.ProductItemName, meter.APIName),
	}
}

// pointProductItemAtMeter updates leftover.meterApiName to the stable
// MeterDefinition name. Deprecated items reject other field changes
// until they leave deprecated, so those are walked back to
// close_to_changes first.
func (c *client) pointProductItemAtMeter(ctx context.Context, item wireProductItem, meterAPIName string) error {
	if item.MeterAPIName == meterAPIName {
		return nil
	}
	if item.LockingStatus == lockingStatusDeprecated {
		unlocked := item
		unlocked.LockingStatus = lockingStatusActive
		if err := c.updateProductItem(ctx, unlocked); err != nil {
			return fmt.Errorf("unlocking leftover product item %s: %w", item.ID, err)
		}
		item = unlocked
	}
	item.MeterAPIName = meterAPIName
	if err := c.updateProductItem(ctx, item); err != nil {
		return fmt.Errorf("retargeting leftover product item %s: %w", item.ID, err)
	}
	return nil
}

// isLegacyUIDMeterAPIName reports whether apiName is a Kubernetes UID
// previously used as Amberflo meterApiName.
func isLegacyUIDMeterAPIName(apiName string) bool {
	return k8sUIDMeterAPIName.MatchString(apiName)
}

// isMeterLabelExistsError reports whether err is Amberflo's duplicate-label
// rejection on POST /meters.
func isMeterLabelExistsError(err error) bool {
	var perm *PermanentError
	if !errors.As(err, &perm) {
		return false
	}
	msg := perm.ResponseBody
	if msg == "" && perm.Err != nil {
		msg = perm.Err.Error()
	}
	return strings.Contains(msg, "already exists with 'label':")
}

// DeleteMeter removes a meter keyed by its meterApiName. When that lookup
// misses, label finds a leftover UID-keyed meter so a pre-migration
// MeterDefinition can still be finalized. A label match whose meterApiName
// is neither the requested name nor a UID is left alone.
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
func (c *client) DeleteMeter(ctx context.Context, meterAPIName, label string) error {
	if meterAPIName == "" {
		return &PermanentError{Err: errors.New("meterAPIName is required")}
	}

	existing, err := c.GetMeter(ctx, meterAPIName)
	if err != nil && !errors.Is(err, ErrMeterNotFound) {
		return err
	}
	if errors.Is(err, ErrMeterNotFound) {
		if label == "" {
			return nil
		}
		existing, err = c.GetMeterByLabel(ctx, label)
		if err != nil {
			if errors.Is(err, ErrMeterNotFound) {
				return nil
			}
			return err
		}
		if existing.APIName != meterAPIName && !isLegacyUIDMeterAPIName(existing.APIName) {
			return nil
		}
	}
	return c.deleteMeterRecord(ctx, existing)
}

// deleteMeterRecord deprecates then DELETEs an already-fetched meter.
func (c *client) deleteMeterRecord(ctx context.Context, existing Meter) error {
	if existing.ID == "" {
		return nil
	}

	if existing.LockingStatus != lockingStatusDeprecated {
		if err := c.deprecateMeter(ctx, existing); err != nil {
			if errors.Is(err, ErrMeterNotFound) {
				return nil
			}
			return err
		}
	}

	path := fmt.Sprintf("/meters/%s", url.PathEscape(existing.ID))
	_, _, err := c.doJSON(ctx, http.MethodDelete, path, nil, nil)
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
	if !meterLabelAcceptable(existing.Label, want.Label, want.MeterAPIName) {
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
