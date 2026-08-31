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
	"errors"
	"net/http"
	"testing"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	billingv1alpha1 "go.miloapis.com/billing/api/v1alpha1"

	"go.miloapis.com/amberflo-provider/internal/amberflo"
)

type stubAmberfloClient struct {
	deletePlanErr error
	ensurePlanErr error
}

func (s *stubAmberfloClient) EnsureCustomer(context.Context, amberflo.DesiredCustomer) (amberflo.Customer, error) {
	return amberflo.Customer{}, nil
}
func (s *stubAmberfloClient) DisableCustomer(context.Context, string) error { return nil }
func (s *stubAmberfloClient) GetCustomer(context.Context, string) (amberflo.Customer, error) {
	return amberflo.Customer{}, nil
}
func (s *stubAmberfloClient) EnsureMeter(context.Context, amberflo.DesiredMeter) (amberflo.Meter, error) {
	return amberflo.Meter{}, nil
}
func (s *stubAmberfloClient) DeleteMeter(context.Context, string, string) error { return nil }
func (s *stubAmberfloClient) GetMeter(context.Context, string) (amberflo.Meter, error) {
	return amberflo.Meter{}, nil
}
func (s *stubAmberfloClient) GetMeterByLabel(context.Context, string) (amberflo.Meter, error) {
	return amberflo.Meter{}, nil
}
func (s *stubAmberfloClient) SubmitUsage(context.Context, []amberflo.UsageRecord) error { return nil }
func (s *stubAmberfloClient) ListInvoices(context.Context, string) ([]amberflo.CustomerProductInvoice, error) {
	return nil, nil
}
func (s *stubAmberfloClient) GetLatestInvoice(context.Context, string) (amberflo.CustomerProductInvoice, error) {
	return amberflo.CustomerProductInvoice{}, nil
}
func (s *stubAmberfloClient) GetInvoice(context.Context, amberflo.InvoiceKey) (amberflo.CustomerProductInvoice, error) {
	return amberflo.CustomerProductInvoice{}, nil
}
func (s *stubAmberfloClient) ListPaymentSettings(context.Context) ([]amberflo.PaymentSetting, error) {
	return nil, nil
}
func (s *stubAmberfloClient) ListPaymentMethodSwitches(context.Context, string) ([]amberflo.PaymentMethodSwitch, error) {
	return nil, nil
}
func (s *stubAmberfloClient) SchedulePaymentMethodSwitch(_ context.Context, sw amberflo.PaymentMethodSwitch) (amberflo.PaymentMethodSwitch, error) {
	return sw, nil
}
func (s *stubAmberfloClient) EnsureProductPlan(context.Context, amberflo.DesiredProductPlan) (amberflo.ProductPlan, error) {
	return amberflo.ProductPlan{}, s.ensurePlanErr
}
func (s *stubAmberfloClient) DeleteProductPlan(context.Context, string) error {
	return s.deletePlanErr
}
func (s *stubAmberfloClient) GetProductPlan(context.Context, string) (amberflo.ProductPlan, error) {
	return amberflo.ProductPlan{}, nil
}
func (s *stubAmberfloClient) EnsureCustomerPlan(context.Context, amberflo.DesiredCustomerPlan) (amberflo.CustomerPlan, error) {
	return amberflo.CustomerPlan{}, nil
}
func (s *stubAmberfloClient) CancelCustomerPlan(context.Context, string, string) error { return nil }
func (s *stubAmberfloClient) ListCustomerPlans(context.Context, string) ([]amberflo.CustomerPlan, error) {
	return nil, nil
}

func TestMeterDefinitionReconciler_handleAmberfloError_RequeuesPermanent(t *testing.T) {
	r := &MeterDefinitionReconciler{}
	result, err := r.handleAmberfloError(
		logr.Discard(),
		&billingv1alpha1.MeterDefinition{},
		&amberflo.PermanentError{Err: errors.New("label occupied"), StatusCode: http.StatusBadRequest},
	)
	if err != nil {
		t.Fatalf("handleAmberfloError: %v", err)
	}
	if result.RequeueAfter != permanentDisableRequeueAfter {
		t.Errorf("RequeueAfter=%s, want %s", result.RequeueAfter, permanentDisableRequeueAfter)
	}
}

func TestOfferReconciler_handleAmberfloError_RequeuesPermanent(t *testing.T) {
	r := &OfferReconciler{}
	result, err := r.handleAmberfloError(
		logr.Discard(),
		&billingv1alpha1.Offer{},
		&amberflo.PermanentError{Err: errors.New("product item name exists"), StatusCode: http.StatusBadRequest},
	)
	if err != nil {
		t.Fatalf("handleAmberfloError: %v", err)
	}
	if result.RequeueAfter != permanentDisableRequeueAfter {
		t.Errorf("RequeueAfter=%s, want %s", result.RequeueAfter, permanentDisableRequeueAfter)
	}
}

func TestBillingEntitlementReconciler_handleAmberfloError_RequeuesPermanent(t *testing.T) {
	r := &BillingEntitlementReconciler{}
	result, err := r.handleAmberfloError(
		logr.Discard(),
		&billingv1alpha1.BillingEntitlement{},
		&amberflo.PermanentError{Err: errors.New("end time in the past"), StatusCode: http.StatusBadRequest},
	)
	if err != nil {
		t.Fatalf("handleAmberfloError: %v", err)
	}
	if result.RequeueAfter != permanentDisableRequeueAfter {
		t.Errorf("RequeueAfter=%s, want %s", result.RequeueAfter, permanentDisableRequeueAfter)
	}
}

func TestOfferReconciler_reconcileDelete_ReleasesFinalizerWhenPlanUndeletable(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := billingv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	now := metav1.Now()
	offer := &billingv1alpha1.Offer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "payg-v1",
			UID:               types.UID("7ae96078-5081-4bef-8d86-58e99a6710a9"),
			Finalizers:        []string{ProductPlanFinalizer},
			DeletionTimestamp: &now,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(offer.DeepCopy()).Build()
	r := &OfferReconciler{
		Client: c,
		AmberfloClient: &stubAmberfloClient{
			deletePlanErr: &amberflo.PermanentError{
				Err:        errors.New("lockingStatus close_to_changes prevents product plan from being deleted"),
				StatusCode: http.StatusBadRequest,
			},
		},
	}

	var live billingv1alpha1.Offer
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(offer), &live); err != nil {
		t.Fatalf("get offer: %v", err)
	}
	result, err := r.reconcileDelete(context.Background(), logr.Discard(), &live, string(offer.UID))
	if err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("RequeueAfter=%s, want 0 after releasing finalizer", result.RequeueAfter)
	}
	if controllerutil.ContainsFinalizer(&live, ProductPlanFinalizer) {
		t.Fatal("expected finalizer cleared on local object")
	}

	var stored billingv1alpha1.Offer
	err = c.Get(context.Background(), client.ObjectKeyFromObject(offer), &stored)
	if apierrors.IsNotFound(err) {
		return
	}
	if err != nil {
		t.Fatalf("get stored offer: %v", err)
	}
	if controllerutil.ContainsFinalizer(&stored, ProductPlanFinalizer) {
		t.Fatal("expected finalizer released so payg-v1 can leave Terminating")
	}
}

func TestOfferReconciler_reconcileDelete_RequeuesTransient(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := billingv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	now := metav1.Now()
	offer := &billingv1alpha1.Offer{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "payg-v1",
			UID:               types.UID("7ae96078-5081-4bef-8d86-58e99a6710a9"),
			Finalizers:        []string{ProductPlanFinalizer},
			DeletionTimestamp: &now,
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(offer.DeepCopy()).Build()
	r := &OfferReconciler{
		Client: c,
		AmberfloClient: &stubAmberfloClient{
			deletePlanErr: &amberflo.TransientError{Err: errors.New("503"), StatusCode: http.StatusServiceUnavailable},
		},
	}

	var live billingv1alpha1.Offer
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(offer), &live); err != nil {
		t.Fatalf("get offer: %v", err)
	}
	result, err := r.reconcileDelete(context.Background(), logr.Discard(), &live, string(offer.UID))
	if err != nil {
		t.Fatalf("reconcileDelete: %v", err)
	}
	if result.RequeueAfter != transientRequeueAfter {
		t.Errorf("RequeueAfter=%s, want %s", result.RequeueAfter, transientRequeueAfter)
	}

	var stored billingv1alpha1.Offer
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(offer), &stored); err != nil {
		t.Fatalf("get stored offer: %v", err)
	}
	if !controllerutil.ContainsFinalizer(&stored, ProductPlanFinalizer) {
		t.Fatal("transient delete must keep the finalizer")
	}
}
