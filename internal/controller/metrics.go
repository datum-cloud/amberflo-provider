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
	"sync"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Reconcile-scoped Prometheus metrics. Registered on controller-runtime's
// default registry so they show up on the standard /metrics endpoint.

var (
	controllerMetricsOnce sync.Once

	reconcileDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "amberflo_provider_reconcile_duration_seconds",
			Help:    "End-to-end reconcile duration for amberflo-provider controllers.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"controller", "result"},
	)
)

func init() {
	controllerMetricsOnce.Do(func() {
		ctrlmetrics.Registry.MustRegister(reconcileDuration)
	})
}

// observeReconcile records a single reconcile outcome for the billing
// account controller. result is one of: success, error, requeue.
func observeReconcile(start time.Time, result string) {
	observeReconcileFor("billingaccount", start, result)
}

// observeReconcileFor records a single reconcile outcome against a named
// controller. Used by additional controllers (e.g. meterdefinition) that
// share the same histogram but vary the controller label.
func observeReconcileFor(controller string, start time.Time, result string) {
	reconcileDuration.
		WithLabelValues(controller, result).
		Observe(time.Since(start).Seconds())
}

// reconcileResult derives the metric label from a Reconcile return pair.
func reconcileResult(res ctrl.Result, err error) string {
	switch {
	case err != nil:
		return "error"
	case res.RequeueAfter > 0:
		return "requeue"
	default:
		return "success"
	}
}
