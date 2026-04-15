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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	billingv1alpha1 "go.miloapis.com/billing/api/v1alpha1"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Pure-function unit tests for the controller package's helpers. These
// do not need envtest and run fast.

var _ = Describe("sortedCopy", func() {
	It("returns nil for an empty input", func() {
		Expect(sortedCopy(nil)).To(BeNil())
		Expect(sortedCopy([]string{})).To(BeNil())
	})
	It("deduplicates and sorts", func() {
		Expect(sortedCopy([]string{"b", "a", "c", "a", ""})).To(Equal([]string{"a", "b", "c"}))
	})
})

var _ = Describe("desiredCustomerFromAccount", func() {
	It("maps spec fields onto a DesiredCustomer with UID-derived ID", func() {
		account := &billingv1alpha1.BillingAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "ba-123",
				Namespace: "default",
				UID:       types.UID("uid-abc-123"),
			},
			Spec: billingv1alpha1.BillingAccountSpec{
				CurrencyCode: "USD",
				ContactInfo: &billingv1alpha1.BillingContactInfo{
					Email: "ops@example.com",
					Name:  "Example Inc.",
				},
				PaymentTerms: &billingv1alpha1.PaymentTerms{
					NetDays:           30,
					InvoiceFrequency:  "Monthly",
					InvoiceDayOfMonth: 5,
				},
			},
		}
		got := desiredCustomerFromAccount(account, []string{"p-b", "p-a", "p-a"})
		Expect(got.ID).To(Equal("uid-abc-123"))
		Expect(got.Name).To(Equal("ba-123"))
		Expect(got.Email).To(Equal("ops@example.com"))
		Expect(got.CurrencyCode).To(Equal("USD"))
		Expect(got.Projects).To(Equal([]string{"p-a", "p-b"}))
		Expect(got.PaymentTerms).NotTo(BeNil())
		Expect(got.PaymentTerms.NetDays).To(Equal(30))
		Expect(got.PaymentTerms.InvoiceFrequency).To(Equal("Monthly"))
		Expect(got.PaymentTerms.InvoiceDayOfMonth).To(Equal(5))
	})
	It("falls back to account name when ContactInfo is absent", func() {
		account := &billingv1alpha1.BillingAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bare",
				Namespace: "default",
				UID:       types.UID("uid-bare"),
			},
			Spec: billingv1alpha1.BillingAccountSpec{CurrencyCode: "EUR"},
		}
		got := desiredCustomerFromAccount(account, nil)
		Expect(got.ID).To(Equal("uid-bare"))
		Expect(got.Name).To(Equal("bare"))
		Expect(got.Email).To(BeEmpty())
		Expect(got.PaymentTerms).To(BeNil())
	})
	It("uses account name as display regardless of ContactInfo.Name", func() {
		account := &billingv1alpha1.BillingAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "named-account",
				Namespace: "default",
				UID:       types.UID("uid-named"),
			},
			Spec: billingv1alpha1.BillingAccountSpec{
				CurrencyCode: "USD",
				ContactInfo: &billingv1alpha1.BillingContactInfo{
					Email: "finance@example.com",
					Name:  "Some Other Label",
				},
			},
		}
		got := desiredCustomerFromAccount(account, nil)
		// Per the pivot brief, display name is always account.Name,
		// even when ContactInfo.Name is set.
		Expect(got.Name).To(Equal("named-account"))
	})
})

var _ = Describe("projectsFromActiveBindings", func() {
	mk := func(project string, phase billingv1alpha1.BillingAccountBindingPhase) billingv1alpha1.BillingAccountBinding {
		return billingv1alpha1.BillingAccountBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "b-" + project, Namespace: "default"},
			Spec: billingv1alpha1.BillingAccountBindingSpec{
				BillingAccountRef: billingv1alpha1.BillingAccountRef{Name: "acct"},
				ProjectRef:        billingv1alpha1.ProjectRef{Name: project},
			},
			Status: billingv1alpha1.BillingAccountBindingStatus{Phase: phase},
		}
	}

	It("returns nil for an empty list", func() {
		Expect(projectsFromActiveBindings(nil)).To(BeNil())
		Expect(projectsFromActiveBindings([]billingv1alpha1.BillingAccountBinding{})).To(BeNil())
	})
	It("filters out superseded and phase-less bindings", func() {
		in := []billingv1alpha1.BillingAccountBinding{
			mk("p-active", billingv1alpha1.BillingAccountBindingPhaseActive),
			mk("p-super", billingv1alpha1.BillingAccountBindingPhaseSuperseded),
			mk("p-blank", ""),
		}
		Expect(projectsFromActiveBindings(in)).To(Equal([]string{"p-active"}))
	})
	It("deduplicates and sorts active-phase output", func() {
		in := []billingv1alpha1.BillingAccountBinding{
			mk("c", billingv1alpha1.BillingAccountBindingPhaseActive),
			mk("a", billingv1alpha1.BillingAccountBindingPhaseActive),
			mk("b", billingv1alpha1.BillingAccountBindingPhaseActive),
			mk("a", billingv1alpha1.BillingAccountBindingPhaseActive),
		}
		Expect(projectsFromActiveBindings(in)).To(Equal([]string{"a", "b", "c"}))
	})
	It("skips bindings with empty ProjectRef.Name", func() {
		in := []billingv1alpha1.BillingAccountBinding{
			mk("", billingv1alpha1.BillingAccountBindingPhaseActive),
			mk("p-one", billingv1alpha1.BillingAccountBindingPhaseActive),
		}
		Expect(projectsFromActiveBindings(in)).To(Equal([]string{"p-one"}))
	})
})
