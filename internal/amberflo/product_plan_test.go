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
	"net/http"
	"strings"
	"testing"
)

func flatRate(v float64) DesiredPlanRate {
	return DesiredPlanRate{Flat: &v}
}

func baseDesiredProductPlan() DesiredProductPlan {
	return DesiredProductPlan{
		ID:   "offer-uid-1",
		Name: "Compute Allocated v1",
		Items: []DesiredPlanItem{
			{
				ID:           "cpu-allocated",
				Label:        "CPU Allocated",
				ChargeType:   PlanChargeTypeUsage,
				MeterAPIName: "meter-uid-cpu",
				Rates:        []DesiredPlanRate{flatRate(0.025)},
			},
		},
	}
}

func TestEnsureProductPlan_CreatesFlatUsage(t *testing.T) {
	c, f := newTestClient(t)
	got, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan())
	if err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}
	if got.ID != "offer-uid-1" {
		t.Errorf("ID=%q", got.ID)
	}
	if got.Name != "Compute Allocated v1" {
		t.Errorf("Name=%q", got.Name)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.productItems["meter-uid-cpu"]; !ok {
		t.Fatalf("expected product item for meter")
	}
	priceID := productItemPriceID("offer-uid-1", "cpu-allocated")
	price, ok := f.itemPrices[priceID]
	if !ok {
		t.Fatalf("expected product item price %s", priceID)
	}
	var leaf wireLeafNode
	if err := json.Unmarshal(price.Price, &leaf); err != nil {
		t.Fatalf("decode price: %v", err)
	}
	if leaf.Type != priceMachineLeafNode {
		t.Errorf("type=%q", leaf.Type)
	}
	if !leaf.AllowPartialBatch {
		t.Errorf("expected allowPartialBatch")
	}
	if len(leaf.Tiers) != 1 || leaf.Tiers[0].PricePerBatch != 0.025 {
		t.Errorf("tiers=%+v", leaf.Tiers)
	}
	plan, ok := f.productPlans["offer-uid-1"]
	if !ok {
		t.Fatalf("expected product plan stored")
	}
	if plan.ProductItemPriceIdsMap["meter-uid-cpu"] != priceID {
		t.Errorf("price map=%v", plan.ProductItemPriceIdsMap)
	}
}

func TestEnsureProductPlan_CreatesCustomPlanGenerator(t *testing.T) {
	c, f := newTestClient(t)
	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}

	f.mu.Lock()
	plan := f.productPlans["offer-uid-1"]
	f.mu.Unlock()
	if plan == nil {
		t.Fatal("expected product plan stored")
	}
	if plan.Type != productPlanTypeCustom {
		t.Errorf("Type=%q, want %q", plan.Type, productPlanTypeCustom)
	}
	if plan.PlanGenerator == nil {
		t.Fatal("expected planGenerator")
	}
	if plan.PlanGenerator.Type != productPlanTypeCustom {
		t.Errorf("planGenerator.Type=%q", plan.PlanGenerator.Type)
	}
	if plan.PlanGenerator.PlanName != "Compute Allocated v1" {
		t.Errorf("planName=%q", plan.PlanGenerator.PlanName)
	}
	if len(plan.PlanGenerator.PriceGenerators) != 1 {
		t.Fatalf("priceGenerators=%d", len(plan.PlanGenerator.PriceGenerators))
	}
	gen := plan.PlanGenerator.PriceGenerators[0]
	if gen.Type != priceGeneratorTypeNoDim {
		t.Errorf("generator type=%q", gen.Type)
	}
	if gen.ProductItemID != "meter-uid-cpu" {
		t.Errorf("productItemId=%q", gen.ProductItemID)
	}
	if gen.LeafNodeType != leafNodeTypePricePerUnit {
		t.Errorf("leafNodeType=%q", gen.LeafNodeType)
	}
	var tiers []wirePriceTier
	if err := json.Unmarshal(gen.PriceTiers, &tiers); err != nil {
		t.Fatalf("decode priceTiers: %v", err)
	}
	if len(tiers) != 1 || tiers[0].PricePerBatch != 0.025 {
		t.Errorf("tiers=%+v", tiers)
	}

	for _, req := range f.requestsCopy() {
		if req.Method != http.MethodPost || req.Path != productPlansPath {
			continue
		}
		if !strings.Contains(string(req.Body), `"type":"custom_pricing_plan"`) {
			t.Errorf("plan POST missing custom_pricing_plan: %s", req.Body)
		}
		if !strings.Contains(string(req.Body), `"planGenerator"`) {
			t.Errorf("plan POST missing planGenerator: %s", req.Body)
		}
		return
	}
	t.Fatal("expected product plan POST")
}

func TestEnsureProductPlan_DimensionGenerator(t *testing.T) {
	c, f := newTestClient(t)
	desired := DesiredProductPlan{
		ID:   "offer-dim",
		Name: "AI",
		Items: []DesiredPlanItem{{
			ID:           "tokens",
			ChargeType:   PlanChargeTypeUsage,
			MeterAPIName: "meter-tokens",
			Rates: []DesiredPlanRate{
				{Match: &DimensionFilter{Dimension: "model", Value: "sonnet"}, Flat: floatPtr(0.000003)},
				{Match: &DimensionFilter{Dimension: "model", Value: "opus"}, Flat: floatPtr(0.000015)},
			},
		}},
	}
	if _, err := c.EnsureProductPlan(context.Background(), desired); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}
	f.mu.Lock()
	plan := f.productPlans["offer-dim"]
	f.mu.Unlock()
	if plan.PlanGenerator == nil || len(plan.PlanGenerator.PriceGenerators) != 1 {
		t.Fatalf("generator=%+v", plan.PlanGenerator)
	}
	gen := plan.PlanGenerator.PriceGenerators[0]
	if gen.Type != priceGeneratorTypeDim {
		t.Errorf("type=%q", gen.Type)
	}
	if len(gen.DimensionKeys) != 1 || gen.DimensionKeys[0] != "model" {
		t.Errorf("dimensionKeys=%v", gen.DimensionKeys)
	}
}

func TestEnsureProductPlan_BackfillsGeneratorOnExistingPlan(t *testing.T) {
	c, f := newTestClient(t)
	desired := baseDesiredProductPlan()
	if _, err := c.EnsureProductPlan(context.Background(), desired); err != nil {
		t.Fatalf("create: %v", err)
	}

	f.mu.Lock()
	plan := f.productPlans["offer-uid-1"]
	plan.Type = ""
	plan.PlanGenerator = nil
	f.mu.Unlock()

	if _, err := c.EnsureProductPlan(context.Background(), desired); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	f.mu.Lock()
	updated := f.productPlans["offer-uid-1"]
	f.mu.Unlock()
	if updated.Type != productPlanTypeCustom {
		t.Errorf("Type=%q", updated.Type)
	}
	if updated.PlanGenerator == nil {
		t.Fatal("expected planGenerator after backfill")
	}
}

func TestEnsureProductPlan_PreservesDefaultFlag(t *testing.T) {
	c, f := newTestClient(t)
	desired := baseDesiredProductPlan()
	desired.Name = "Renamed"
	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("create: %v", err)
	}
	trueVal := true
	f.mu.Lock()
	f.productPlans["offer-uid-1"].IsDefault = &trueVal
	f.mu.Unlock()

	if _, err := c.EnsureProductPlan(context.Background(), desired); err != nil {
		t.Fatalf("update: %v", err)
	}
	f.mu.Lock()
	plan := f.productPlans["offer-uid-1"]
	f.mu.Unlock()
	if plan.IsDefault == nil || !*plan.IsDefault {
		t.Errorf("isDefault=%v, want true", plan.IsDefault)
	}
	if plan.ProductPlanName != "Renamed" {
		t.Errorf("name=%q", plan.ProductPlanName)
	}
}

func TestEnsureProductPlan_CreatesProductItemName(t *testing.T) {
	c, f := newTestClient(t)
	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}
	for _, req := range f.requestsCopy() {
		if req.Method != http.MethodPost || req.Path != productItemsPath {
			continue
		}
		if !strings.Contains(string(req.Body), `"productItemName"`) {
			t.Fatalf("product item POST missing productItemName: %s", req.Body)
		}
		return
	}
	t.Fatal("expected product item POST")
}

func TestEnsureProductPlan_TieredGraduated(t *testing.T) {
	c, _ := newTestClient(t)
	desired := DesiredProductPlan{
		ID:   "offer-tiered",
		Name: "Data Transfer",
		Items: []DesiredPlanItem{{
			ID:           "egress",
			ChargeType:   PlanChargeTypeUsage,
			MeterAPIName: "meter-egress",
			Rates: []DesiredPlanRate{{
				Tiers: []DesiredPriceTier{
					{StartAfterUnit: 0, BatchSize: 1, PricePerBatch: 0.12, AllowPartialBatch: true},
					{StartAfterUnit: 100, BatchSize: 1, PricePerBatch: 0.09, AllowPartialBatch: true},
				},
			}},
		}},
	}
	if _, err := c.EnsureProductPlan(context.Background(), desired); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}
}

func TestEnsureProductPlan_DimensionMatch(t *testing.T) {
	c, f := newTestClient(t)
	desired := DesiredProductPlan{
		ID:   "offer-dim",
		Name: "AI",
		Items: []DesiredPlanItem{{
			ID:           "tokens",
			ChargeType:   PlanChargeTypeUsage,
			MeterAPIName: "meter-tokens",
			Rates: []DesiredPlanRate{
				{Match: &DimensionFilter{Dimension: "model", Value: "sonnet"}, Flat: floatPtr(0.000003)},
				{Match: &DimensionFilter{Dimension: "model", Value: "opus"}, Flat: floatPtr(0.000015)},
			},
		}},
	}
	if _, err := c.EnsureProductPlan(context.Background(), desired); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}
	priceID := productItemPriceID("offer-dim", "tokens")
	f.mu.Lock()
	price := f.itemPrices[priceID]
	f.mu.Unlock()
	var matrix wireDimensionMatrixNode
	if err := json.Unmarshal(price.Price, &matrix); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if matrix.Type != priceMachineDimMatrix {
		t.Errorf("type=%q", matrix.Type)
	}
	if len(matrix.DimensionKeys) != 1 || matrix.DimensionKeys[0] != "model" {
		t.Errorf("keys=%v", matrix.DimensionKeys)
	}
	if len(matrix.DimensionsPrices) != 2 {
		t.Errorf("dims=%d", len(matrix.DimensionsPrices))
	}
}

func TestEnsureProductPlan_UnmatchedWithMatchRejected(t *testing.T) {
	c, _ := newTestClient(t)
	flat := 0.01
	desired := DesiredProductPlan{
		ID:   "offer-bad-default",
		Name: "Bad",
		Items: []DesiredPlanItem{{
			ID:           "tokens",
			ChargeType:   PlanChargeTypeUsage,
			MeterAPIName: "meter-tokens",
			Rates: []DesiredPlanRate{
				{Match: &DimensionFilter{Dimension: "model", Value: "sonnet"}, Flat: floatPtr(0.000003)},
				{Flat: &flat}, // unmatched catch-all — not representable in Amberflo
			},
		}},
	}
	_, err := c.EnsureProductPlan(context.Background(), desired)
	if err == nil {
		t.Fatal("expected permanent error for unmatched rate alongside Match")
	}
	if !IsPermanent(err) {
		t.Fatalf("want permanent error, got %v", err)
	}
	if !strings.Contains(err.Error(), "unmatched catch-all") || !strings.Contains(err.Error(), "other") {
		t.Fatalf("want catch-all / sentinel guidance in error, got %v", err)
	}
}

func TestEnsureProductPlan_OneTimeAndRecurringFees(t *testing.T) {
	c, f := newTestClient(t)
	desired := DesiredProductPlan{
		ID:   "offer-fees",
		Name: "With Fees",
		Items: []DesiredPlanItem{
			{ID: "setup", Label: "Setup", ChargeType: PlanChargeTypeOneTime, Amount: 10},
			{ID: "platform", Label: "Platform", ChargeType: PlanChargeTypeRecurring, Amount: 5},
		},
	}
	if _, err := c.EnsureProductPlan(context.Background(), desired); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}
	f.mu.Lock()
	plan := f.productPlans["offer-fees"]
	f.mu.Unlock()
	if plan.FeeMap["setup"].Cost != 10 || !plan.FeeMap["setup"].IsOneTimeFee {
		t.Errorf("setup fee=%+v", plan.FeeMap["setup"])
	}
	if plan.FeeMap["platform"].Cost != 5 || plan.FeeMap["platform"].IsOneTimeFee {
		t.Errorf("platform fee=%+v", plan.FeeMap["platform"])
	}
}

func TestEnsureProductPlan_NoopWhenEqual(t *testing.T) {
	c, f := newTestClient(t)
	desired := baseDesiredProductPlan()
	if _, err := c.EnsureProductPlan(context.Background(), desired); err != nil {
		t.Fatalf("create: %v", err)
	}
	before := len(f.requestsCopy())
	if _, err := c.EnsureProductPlan(context.Background(), desired); err != nil {
		t.Fatalf("second: %v", err)
	}
	after := f.requestsCopy()
	// Second call: GET plan + GET item + GET price; no POST plan.
	posts := 0
	for _, r := range after[before:] {
		if r.Method == http.MethodPost && r.Path == productPlansPath {
			posts++
		}
	}
	if posts != 0 {
		t.Errorf("expected no plan POST on noop, got %d", posts)
	}
}

func TestDeleteProductPlan_ToleratesNotFound(t *testing.T) {
	c, _ := newTestClient(t)
	if err := c.DeleteProductPlan(context.Background(), "missing"); err != nil {
		t.Fatalf("DeleteProductPlan: %v", err)
	}
}

func TestDeleteProductPlan_RemovesExisting(t *testing.T) {
	c, f := newTestClient(t)
	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.DeleteProductPlan(context.Background(), "offer-uid-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	f.mu.Lock()
	_, ok := f.productPlans["offer-uid-1"]
	f.mu.Unlock()
	if ok {
		t.Fatal("plan still present")
	}
	counts := methodCounts(f.requestsCopy())
	if counts[http.MethodDelete] != 1 {
		t.Errorf("expected 1 DELETE, got %#v", counts)
	}
}

func TestEnsureProductPlan_GetUsesQueryParams(t *testing.T) {
	c, f := newTestClient(t)
	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}

	for _, req := range f.requestsCopy() {
		if req.Method != http.MethodGet {
			continue
		}
		switch req.Path {
		case productPlansPath:
			if req.Query != "productPlanId=offer-uid-1" {
				t.Errorf("product plan GET query=%q, want productPlanId=offer-uid-1", req.Query)
			}
		case productItemsPath:
			if req.Query != "productItemId=meter-uid-cpu" {
				t.Errorf("product item GET query=%q, want productItemId=meter-uid-cpu", req.Query)
			}
		case productItemPricePath:
			wantID := productItemPriceID("offer-uid-1", "cpu-allocated")
			if req.Query != "id="+wantID {
				t.Errorf("product item price GET query=%q, want id=%s", req.Query, wantID)
			}
		}
	}
}

func TestEnsureProductPlan_ReusesLeftoverItemByDisplayName(t *testing.T) {
	c, f := newTestClient(t)
	const leftoverID = "3e421f8e-5c58-4598-abbc-7372f3a21539"
	f.seedProductItem(wireProductItem{
		ID:              leftoverID,
		ProductItemName: "CPU Allocated",
		MeterAPIName:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		ProductID:       "1",
		LockingStatus:   lockingStatusActive,
	})

	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}

	item, ok := f.fetchProductItem(leftoverID)
	if !ok {
		t.Fatal("expected leftover product item to be kept")
	}
	if item.MeterAPIName != "meter-uid-cpu" {
		t.Errorf("meterApiName=%q, want meter-uid-cpu", item.MeterAPIName)
	}
	if _, created := f.fetchProductItem("meter-uid-cpu"); created {
		t.Error("must not POST a second product item keyed by meterApiName")
	}

	f.mu.Lock()
	plan := f.productPlans["offer-uid-1"]
	f.mu.Unlock()
	if plan == nil {
		t.Fatal("expected product plan stored")
	}
	priceID := productItemPriceID("offer-uid-1", "cpu-allocated")
	if plan.ProductItemPriceIdsMap[leftoverID] != priceID {
		t.Errorf("price map=%v, want leftover UUID key", plan.ProductItemPriceIdsMap)
	}
	if _, keyedByAPIName := plan.ProductItemPriceIdsMap["meter-uid-cpu"]; keyedByAPIName {
		t.Error("must not key productItemPriceIdsMap by meterApiName when reusing a UUID item")
	}
	if plan.PlanGenerator == nil || len(plan.PlanGenerator.PriceGenerators) != 1 {
		t.Fatalf("generator=%+v", plan.PlanGenerator)
	}
	if plan.PlanGenerator.PriceGenerators[0].ProductItemID != leftoverID {
		t.Errorf("generator productItemId=%q, want leftover UUID", plan.PlanGenerator.PriceGenerators[0].ProductItemID)
	}
}

func TestEnsureProductPlan_AdoptsLeftoverWithInvalidLongMeterAPIName(t *testing.T) {
	c, f := newTestClient(t)
	longName := "assistant-miloapis-com-conversation-cache-read-tokens"
	hashed := MeterAPIName(longName)
	f.seedProductItem(wireProductItem{
		ID:              longName,
		ProductItemName: "Cached prompt tokens",
		MeterAPIName:    longName,
		ProductID:       "1",
		LockingStatus:   "open",
	})
	desired := DesiredProductPlan{
		ID:   "offer-cache",
		Name: "PAYG 3",
		Items: []DesiredPlanItem{{
			ID:           "cache-read",
			Label:        "Cached prompt tokens",
			ChargeType:   PlanChargeTypeUsage,
			MeterAPIName: hashed,
			Rates:        []DesiredPlanRate{flatRate(0.001)},
		}},
	}
	if _, err := c.EnsureProductPlan(context.Background(), desired); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}
}

func TestEnsureProductPlan_MintsUniqueItemWhenLeftoverDeprecated(t *testing.T) {
	c, f := newTestClient(t)
	const leftoverID = "3e421f8e-5c58-4598-abbc-7372f3a21539"
	f.seedProductItem(wireProductItem{
		ID:              leftoverID,
		ProductItemName: "CPU Allocated",
		MeterAPIName:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		ProductID:       "1",
		LockingStatus:   lockingStatusDeprecated,
	})

	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}

	leftover, ok := f.fetchProductItem(leftoverID)
	if !ok {
		t.Fatal("expected leftover product item to be kept")
	}
	if leftover.MeterAPIName != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("immutable leftover meterApiName=%q", leftover.MeterAPIName)
	}

	created, ok := f.fetchProductItem("meter-uid-cpu")
	if !ok {
		t.Fatal("expected a new product item keyed by meterApiName")
	}
	wantName := disambiguatedMeterLabel("CPU Allocated", "meter-uid-cpu")
	if created.ProductItemName != wantName {
		t.Errorf("productItemName=%q, want %q", created.ProductItemName, wantName)
	}
	if created.MeterAPIName != "meter-uid-cpu" {
		t.Errorf("new item meterApiName=%q", created.MeterAPIName)
	}

	f.mu.Lock()
	plan := f.productPlans["offer-uid-1"]
	f.mu.Unlock()
	priceID := productItemPriceID("offer-uid-1", "cpu-allocated")
	if plan.ProductItemPriceIdsMap["meter-uid-cpu"] != priceID {
		t.Errorf("price map=%v, want meterApiName key", plan.ProductItemPriceIdsMap)
	}
}

func TestEnsureProductPlan_ReusesUUIDSiblingOnSameMeter(t *testing.T) {
	c, f := newTestClient(t)
	const leftoverID = "3e421f8e-5c58-4598-abbc-7372f3a21539"
	const siblingID = "dd14562d-46cf-4ace-b1e9-38e6d1089ac4"
	f.seedProductItem(wireProductItem{
		ID:              leftoverID,
		ProductItemName: "CPU Allocated",
		MeterAPIName:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		ProductID:       "1",
		LockingStatus:   lockingStatusDeprecated,
	})
	f.seedProductItem(wireProductItem{
		ID:              siblingID,
		ProductItemName: disambiguatedMeterLabel("CPU Allocated", "meter-uid-cpu"),
		MeterAPIName:    "meter-uid-cpu",
		ProductID:       "1",
		LockingStatus:   lockingStatusClose,
	})

	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}

	if _, created := f.fetchProductItem("meter-uid-cpu"); created {
		t.Error("must not POST a third product item keyed by meterApiName")
	}
	f.mu.Lock()
	plan := f.productPlans["offer-uid-1"]
	f.mu.Unlock()
	priceID := productItemPriceID("offer-uid-1", "cpu-allocated")
	if plan == nil || plan.ProductItemPriceIdsMap[siblingID] != priceID {
		t.Errorf("price map=%v, want sibling UUID key", plan.ProductItemPriceIdsMap)
	}
}

func TestEnsureProductPlan_MintsUniquePriceWhenLeftoverNameExists(t *testing.T) {
	c, f := newTestClient(t)
	const leftoverItemID = "b0daf6b1-e724-45ce-9b01-a96a79d2c479"
	const leftoverPriceID = "payg-v1-compute-vcpu-price"
	f.seedProductItem(wireProductItem{
		ID:              leftoverItemID,
		ProductItemName: "CPU Allocated",
		MeterAPIName:    leftoverItemID,
		ProductID:       "1",
		LockingStatus:   lockingStatusClose,
	})
	f.seedProductItemPrice(wireProductItemPrice{
		ID:                   leftoverPriceID,
		ProductItemID:        leftoverItemID,
		ProductItemPriceName: "CPU Allocated",
		LockingStatus:        lockingStatusClose,
	})

	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}

	priceID := productItemPriceID("offer-uid-1", "cpu-allocated")
	f.mu.Lock()
	price := f.itemPrices[priceID]
	leftover := f.itemPrices[leftoverPriceID]
	plan := f.productPlans["offer-uid-1"]
	f.mu.Unlock()
	if price == nil {
		t.Fatal("expected a new product item price keyed by offer+item")
	}
	wantName := disambiguatedMeterLabel("CPU Allocated", priceID)
	if price.ProductItemPriceName != wantName {
		t.Errorf("productItemPriceName=%q, want %q", price.ProductItemPriceName, wantName)
	}
	if leftover == nil || leftover.ProductItemPriceName != "CPU Allocated" {
		t.Errorf("leftover price name=%v, want original label", leftover)
	}
	if plan == nil || plan.ProductItemPriceIdsMap[leftoverItemID] != priceID {
		t.Errorf("price map=%v, want leftover item UUID key", plan.ProductItemPriceIdsMap)
	}

	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("EnsureProductPlan second pass: %v", err)
	}
}

func TestEnsureProductPlan_PreservesExistingPriceNameOnItemRetarget(t *testing.T) {
	c, f := newTestClient(t)
	const leftoverID = "3e421f8e-5c58-4598-abbc-7372f3a21539"
	const siblingID = "dd14562d-46cf-4ace-b1e9-38e6d1089ac4"
	priceID := productItemPriceID("offer-uid-1", "cpu-allocated")
	unique := disambiguatedMeterLabel("CPU Allocated", priceID)
	f.seedProductItem(wireProductItem{
		ID:              leftoverID,
		ProductItemName: "CPU Allocated",
		MeterAPIName:    "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		ProductID:       "1",
		LockingStatus:   lockingStatusDeprecated,
	})
	f.seedProductItem(wireProductItem{
		ID:              siblingID,
		ProductItemName: unique,
		MeterAPIName:    "meter-uid-cpu",
		ProductID:       "1",
		LockingStatus:   lockingStatusClose,
	})
	f.seedProductItemPrice(wireProductItemPrice{
		ID:                   priceID,
		ProductItemID:        leftoverID,
		ProductItemPriceName: unique,
		LockingStatus:        lockingStatusClose,
	})

	if _, err := c.EnsureProductPlan(context.Background(), baseDesiredProductPlan()); err != nil {
		t.Fatalf("EnsureProductPlan: %v", err)
	}

	f.mu.Lock()
	price := f.itemPrices[priceID]
	f.mu.Unlock()
	if price == nil {
		t.Fatal("expected existing price to be kept")
	}
	if price.ProductItemPriceName != unique {
		t.Errorf("productItemPriceName=%q, want preserved %q", price.ProductItemPriceName, unique)
	}
	if price.ProductItemID != siblingID {
		t.Errorf("productItemId=%q, want sibling %q", price.ProductItemID, siblingID)
	}
}

func TestEnsureProductPlan_EmptyIDPermanent(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.EnsureProductPlan(context.Background(), DesiredProductPlan{})
	if !IsPermanent(err) {
		t.Fatalf("expected permanent, got %v", err)
	}
}

func floatPtr(v float64) *float64 { return &v }
