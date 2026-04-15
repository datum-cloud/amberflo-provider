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
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	billingv1alpha1 "go.miloapis.com/billing/api/v1alpha1"

	. "github.com/onsi/ginkgo/v2" //nolint:revive // dot-import convention
	. "github.com/onsi/gomega"
)

const (
	// envtestTimeout caps Eventually loops. envtest is slower than a
	// real apiserver; 10s gives enough headroom on a busy CI runner.
	envtestTimeout = 10 * time.Second
	// envtestInterval is the poll interval for Eventually loops.
	envtestInterval = 250 * time.Millisecond
)

// mustCreate creates obj and fails the test on any error.
func mustCreate(ctx context.Context, obj client.Object) {
	GinkgoHelper()
	Expect(k8sClient.Create(ctx, obj)).To(Succeed())
}

// mustDelete deletes obj. Missing objects are tolerated.
func mustDelete(ctx context.Context, obj client.Object) {
	GinkgoHelper()
	err := k8sClient.Delete(ctx, obj)
	if err != nil && !apierrors.IsNotFound(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

// waitForFinalizer polls until the given BillingAccount carries the
// customer-link finalizer. Returns the fetched object.
func waitForFinalizer(ctx context.Context, key client.ObjectKey) *billingv1alpha1.BillingAccount {
	GinkgoHelper()
	var account billingv1alpha1.BillingAccount
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, key, &account)).To(Succeed())
		g.Expect(slices.Contains(account.Finalizers, CustomerLinkFinalizer)).To(BeTrue(),
			"finalizer %q missing on %s", CustomerLinkFinalizer, key.String())
	}, envtestTimeout, envtestInterval).Should(Succeed())
	return &account
}

// waitForNoFinalizer polls until the given BillingAccount no longer has
// the customer-link finalizer (or is gone entirely).
func waitForNoFinalizer(ctx context.Context, key client.ObjectKey) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		var account billingv1alpha1.BillingAccount
		err := k8sClient.Get(ctx, key, &account)
		if apierrors.IsNotFound(err) {
			return
		}
		g.Expect(err).NotTo(HaveOccurred())
		for _, f := range account.Finalizers {
			g.Expect(f).NotTo(Equal(CustomerLinkFinalizer),
				"finalizer still present on %s", key.String())
		}
	}, envtestTimeout, envtestInterval).Should(Succeed())
}

// waitForEventReason polls the FakeRecorder's event channel for an event
// with the given reason, draining events that don't match. Returns the
// matched event string when found.
func waitForEventReason(reason string) string {
	GinkgoHelper()
	deadline := time.Now().Add(envtestTimeout)
	for time.Now().Before(deadline) {
		select {
		case ev := <-fakeRecorder.Events:
			// FakeRecorder emits strings shaped "type reason message".
			if strings.Contains(ev, " "+reason+" ") || strings.HasPrefix(ev, "Normal "+reason) ||
				strings.HasPrefix(ev, "Warning "+reason) {
				return ev
			}
		case <-time.After(envtestInterval):
		}
	}
	Fail("timed out waiting for event reason=" + reason)
	return ""
}

// drainEvents flushes the fake recorder's event channel. Useful between
// test specs so a lingering Synced from a prior spec does not satisfy a
// new spec's waitForEventReason.
func drainEvents() {
	for {
		select {
		case <-fakeRecorder.Events:
		default:
			return
		}
	}
}

// toKey returns a client.ObjectKey for any client.Object.
func toKey(obj client.Object) client.ObjectKey {
	return client.ObjectKeyFromObject(obj)
}

// newBinding returns an in-memory BillingAccountBinding in the default
// namespace referencing the supplied account and project.
func newBinding(name, account, project string) *billingv1alpha1.BillingAccountBinding {
	return &billingv1alpha1.BillingAccountBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
		},
		Spec: billingv1alpha1.BillingAccountBindingSpec{
			BillingAccountRef: billingv1alpha1.BillingAccountRef{Name: account},
			ProjectRef:        billingv1alpha1.ProjectRef{Name: project},
		},
	}
}

// updateBindingPhase fetches the latest binding by key, flips its
// status.phase, and writes it back via the status subresource.
func updateBindingPhase(
	ctx context.Context,
	key client.ObjectKey,
	phase billingv1alpha1.BillingAccountBindingPhase,
) {
	GinkgoHelper()
	Eventually(func(g Gomega) {
		var fresh billingv1alpha1.BillingAccountBinding
		g.Expect(k8sClient.Get(ctx, key, &fresh)).To(Succeed())
		fresh.Status.Phase = phase
		g.Expect(k8sClient.Status().Update(ctx, &fresh)).To(Succeed())
	}, envtestTimeout, envtestInterval).Should(Succeed())
}

// deleteBinding issues a background-propagation delete and waits for removal.
func deleteBinding(ctx context.Context, binding *billingv1alpha1.BillingAccountBinding) {
	GinkgoHelper()
	policy := metav1.DeletePropagationBackground
	err := k8sClient.Delete(ctx, binding, &client.DeleteOptions{PropagationPolicy: &policy})
	if err != nil && !apierrors.IsNotFound(err) {
		Expect(err).NotTo(HaveOccurred())
	}
	Eventually(func(g Gomega) {
		var fresh billingv1alpha1.BillingAccountBinding
		err := k8sClient.Get(ctx, client.ObjectKeyFromObject(binding), &fresh)
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(),
			"binding %s still present (err=%v)", binding.Name, err)
	}, envtestTimeout, envtestInterval).Should(Succeed())
}

// waitForCustomerInFake polls until the recording fake sees a stored
// customer matching the given BillingAccount UID and predicate returns
// true. Returns the final observed customer.
func waitForCustomerInFake(
	ctx context.Context,
	accountKey client.ObjectKey,
	predicate func(storedCustomer) bool,
) storedCustomer {
	GinkgoHelper()
	var last storedCustomer
	Eventually(func(g Gomega) {
		var account billingv1alpha1.BillingAccount
		g.Expect(k8sClient.Get(ctx, accountKey, &account)).To(Succeed())
		c, ok := fakeHTTP.FetchCustomer(string(account.UID))
		g.Expect(ok).To(BeTrue(), "no customer yet for UID=%s", account.UID)
		if predicate != nil {
			g.Expect(predicate(c)).To(BeTrue(), "predicate not satisfied; c=%+v", c)
		}
		last = c
	}, envtestTimeout, envtestInterval).Should(Succeed())
	return last
}

// decodeProjectsTrait unpacks the JSON-encoded projects trait the
// provider writes onto the Amberflo customer.
func decodeProjectsTrait(traits map[string]string) []string {
	raw, ok := traits["projects"]
	if !ok {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
