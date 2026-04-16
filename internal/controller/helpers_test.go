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

var _ = Describe("amberfloMeterType", func() {
	It("maps the supported Milo aggregations onto Amberflo meterType wire values", func() {
		cases := map[billingv1alpha1.MeterAggregation]string{
			billingv1alpha1.MeterAggregationSum:         "sum_of_all_usage",
			billingv1alpha1.MeterAggregationCount:       "sum_of_all_usage",
			billingv1alpha1.MeterAggregationUniqueCount: "active_users",
		}
		for in, want := range cases {
			got, ok := amberfloMeterType(in)
			Expect(ok).To(BeTrue(), "expected %s to be supported", in)
			Expect(got).To(Equal(want))
		}
	})
	It("returns false for aggregations Amberflo does not expose", func() {
		for _, in := range []billingv1alpha1.MeterAggregation{
			billingv1alpha1.MeterAggregationMax,
			billingv1alpha1.MeterAggregationMin,
			billingv1alpha1.MeterAggregationLatest,
			billingv1alpha1.MeterAggregationAverage,
		} {
			got, ok := amberfloMeterType(in)
			Expect(ok).To(BeFalse(), "%s should be unsupported", in)
			Expect(got).To(BeEmpty())
		}
	})
})

var _ = Describe("desiredMeterFromDefinition", func() {
	It("maps spec fields onto DesiredMeter with UID-derived APIName", func() {
		md := &billingv1alpha1.MeterDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cpu-seconds",
				UID:  types.UID("uid-cpu"),
			},
			Spec: billingv1alpha1.MeterDefinitionSpec{
				MeterName:   "compute.miloapis.com/cpu-seconds",
				DisplayName: "CPU Seconds",
				Measurement: billingv1alpha1.MeterMeasurement{
					Unit:       "s",
					Dimensions: []string{"region", "tier"},
				},
				Billing: billingv1alpha1.MeterBilling{
					ConsumedUnit: "s",
					PricingUnit:  "h",
				},
			},
		}
		got := desiredMeterFromDefinition(md, "sum_of_all_usage")
		Expect(got.APIName).To(Equal("uid-cpu"))
		Expect(got.Label).To(Equal("CPU Seconds"))
		Expect(got.MeterType).To(Equal("sum_of_all_usage"))
		Expect(got.Unit).To(Equal("s"))
		Expect(got.Dimensions).To(Equal([]string{"region", "tier"}))
		// sum_of_all_usage does not use aggregationDimensions.
		Expect(got.AggregationDimensions).To(BeEmpty())
	})
	It("populates aggregationDimensions for active_users", func() {
		md := &billingv1alpha1.MeterDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "active-projects",
				UID:  types.UID("uid-ap"),
			},
			Spec: billingv1alpha1.MeterDefinitionSpec{
				MeterName:   "compute.miloapis.com/active-projects",
				DisplayName: "Active Projects",
				Measurement: billingv1alpha1.MeterMeasurement{
					Unit:       "{project}",
					Dimensions: []string{"project_id"},
				},
				Billing: billingv1alpha1.MeterBilling{
					ConsumedUnit: "{project}",
					PricingUnit:  "{project}",
				},
			},
		}
		got := desiredMeterFromDefinition(md, "active_users")
		Expect(got.MeterType).To(Equal("active_users"))
		Expect(got.AggregationDimensions).To(Equal([]string{"project_id"}))
		Expect(got.Dimensions).To(Equal([]string{"project_id"}))
	})
	It("falls back to meterName when displayName is empty", func() {
		md := &billingv1alpha1.MeterDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "no-display",
				UID:  types.UID("uid-nd"),
			},
			Spec: billingv1alpha1.MeterDefinitionSpec{
				MeterName: "compute.miloapis.com/fallback",
				Measurement: billingv1alpha1.MeterMeasurement{
					Unit: "{request}",
				},
				Billing: billingv1alpha1.MeterBilling{
					ConsumedUnit: "{request}",
					PricingUnit:  "{request}",
				},
			},
		}
		got := desiredMeterFromDefinition(md, "sum_of_all_usage")
		Expect(got.Label).To(Equal("compute.miloapis.com/fallback"))
		Expect(got.Dimensions).To(BeNil())
	})
})

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
