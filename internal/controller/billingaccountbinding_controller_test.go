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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	billingv1alpha1 "go.miloapis.com/billing/api/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
)

// BillingAccountBinding flow tests. The provider has no dedicated binding
// reconciler; the customer reconciler watches bindings via a map-func and
// aggregates Active bindings into the customer's `projects` trait.
//
// Each assertion inspects the Amberflo fake's stored customer directly
// via the UID derived from the created BillingAccount.

var _ = Describe("BillingAccountBindingFlow", func() {
	BeforeEach(func() {
		fakeHTTP.Reset()
		drainEvents()
	})

	Context("BindingAdd_ExtendsProjectsTrait", func() {
		It("propagates a new Active binding's project into the customer trait", func() {
			name := "binding-add"
			account := &billingv1alpha1.BillingAccount{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec:       billingv1alpha1.BillingAccountSpec{CurrencyCode: "USD"},
			}
			mustCreate(ctx, account)
			DeferCleanup(func() { mustDelete(ctx, account); waitForNoFinalizer(ctx, accountKey(name)) })

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				return c.Enabled
			})

			binding := newBinding("bind-project-a", name, "project-a")
			mustCreate(ctx, binding)
			DeferCleanup(func() { deleteBinding(ctx, binding) })

			updateBindingPhase(ctx, toKey(binding), billingv1alpha1.BillingAccountBindingPhaseActive)

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				projects := decodeProjectsTrait(c.Traits)
				return len(projects) == 1 && projects[0] == "project-a"
			})
		})
	})

	Context("BindingSupersede_RemovesFromTrait", func() {
		It("drops a project whose binding transitions to Superseded", func() {
			name := "binding-supersede"
			account := &billingv1alpha1.BillingAccount{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec:       billingv1alpha1.BillingAccountSpec{CurrencyCode: "USD"},
			}
			mustCreate(ctx, account)
			DeferCleanup(func() { mustDelete(ctx, account); waitForNoFinalizer(ctx, accountKey(name)) })

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				return c.Enabled
			})

			keep := newBinding("bind-keep", name, "proj-keep")
			drop := newBinding("bind-drop", name, "proj-drop")
			mustCreate(ctx, keep)
			DeferCleanup(func() { deleteBinding(ctx, keep) })
			mustCreate(ctx, drop)
			DeferCleanup(func() { deleteBinding(ctx, drop) })

			updateBindingPhase(ctx, toKey(keep), billingv1alpha1.BillingAccountBindingPhaseActive)
			updateBindingPhase(ctx, toKey(drop), billingv1alpha1.BillingAccountBindingPhaseActive)

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				projects := decodeProjectsTrait(c.Traits)
				return equalStringSlice(projects, []string{"proj-drop", "proj-keep"})
			})

			updateBindingPhase(ctx, toKey(drop), billingv1alpha1.BillingAccountBindingPhaseSuperseded)

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				projects := decodeProjectsTrait(c.Traits)
				return equalStringSlice(projects, []string{"proj-keep"})
			})
		})
	})

	Context("BindingDelete_RemovesFromTrait", func() {
		It("removes the project when the binding is background-deleted", func() {
			name := "binding-delete"
			account := &billingv1alpha1.BillingAccount{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec:       billingv1alpha1.BillingAccountSpec{CurrencyCode: "USD"},
			}
			mustCreate(ctx, account)
			DeferCleanup(func() { mustDelete(ctx, account); waitForNoFinalizer(ctx, accountKey(name)) })

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				return c.Enabled
			})

			binding := newBinding("bind-to-delete", name, "proj-del")
			mustCreate(ctx, binding)
			updateBindingPhase(ctx, toKey(binding), billingv1alpha1.BillingAccountBindingPhaseActive)

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				return equalStringSlice(decodeProjectsTrait(c.Traits), []string{"proj-del"})
			})

			deleteBinding(ctx, binding)

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				return len(decodeProjectsTrait(c.Traits)) == 0
			})
		})
	})

	Context("MultipleActiveBindings_Aggregate", func() {
		It("reflects all Active bindings sorted in the trait", func() {
			name := "binding-multi"
			account := &billingv1alpha1.BillingAccount{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec:       billingv1alpha1.BillingAccountSpec{CurrencyCode: "USD"},
			}
			mustCreate(ctx, account)
			DeferCleanup(func() { mustDelete(ctx, account); waitForNoFinalizer(ctx, accountKey(name)) })

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				return c.Enabled
			})

			bindings := []*billingv1alpha1.BillingAccountBinding{
				newBinding("bind-multi-a", name, "a"),
				newBinding("bind-multi-b", name, "b"),
				newBinding("bind-multi-c", name, "c"),
			}
			for _, b := range bindings {
				mustCreate(ctx, b)
				DeferCleanup(func() { deleteBinding(ctx, b) })
				updateBindingPhase(ctx, toKey(b), billingv1alpha1.BillingAccountBindingPhaseActive)
			}

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				return equalStringSlice(decodeProjectsTrait(c.Traits), []string{"a", "b", "c"})
			})
		})
	})

	Context("DeterministicSortedOutput", func() {
		It("emits projects in sorted order regardless of creation order", func() {
			name := "binding-sort"
			account := &billingv1alpha1.BillingAccount{
				ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
				Spec:       billingv1alpha1.BillingAccountSpec{CurrencyCode: "USD"},
			}
			mustCreate(ctx, account)
			DeferCleanup(func() { mustDelete(ctx, account); waitForNoFinalizer(ctx, accountKey(name)) })

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				return c.Enabled
			})

			order := []struct {
				name    string
				project string
			}{
				{"bind-sort-c", "c"},
				{"bind-sort-a", "a"},
				{"bind-sort-b", "b"},
			}
			for _, o := range order {
				b := newBinding(o.name, name, o.project)
				mustCreate(ctx, b)
				DeferCleanup(func() { deleteBinding(ctx, b) })
				updateBindingPhase(ctx, toKey(b), billingv1alpha1.BillingAccountBindingPhaseActive)
			}

			waitForCustomerInFake(ctx, accountKey(name), func(c storedCustomer) bool {
				projects := decodeProjectsTrait(c.Traits)
				// Also confirm the encoded JSON round-trips cleanly.
				if _, err := json.Marshal(projects); err != nil {
					return false
				}
				return equalStringSlice(projects, []string{"a", "b", "c"})
			})
		})
	})
})

// equalStringSlice returns true when a and b contain the same elements
// in the same order.
func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
