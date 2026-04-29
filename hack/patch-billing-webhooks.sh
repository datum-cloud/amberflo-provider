#!/usr/bin/env bash
# Compensate for bugs in billing's published `overlays/dev` bundle
# (ghcr.io/datum-cloud/billing-kustomize) so `task test:install-dependencies`
# leaves the cluster with a working billing service for e2e.
#
# Specifically:
#   1. Rewrite webhook clientConfig.url (host.docker.internal:9443) to
#      target the in-cluster billing-webhook Service.
#   2. Extend the billing-serving-cert Certificate dnsNames with the
#      Service's in-cluster DNS names so the TLS handshake succeeds.
#   3. Set billing-config.webhookServer.port=9443 explicitly (the
#      bundle leaves it unset, which controller-runtime treats as
#      "port 0" = random).
#   4. Restart billing-controller-manager so it picks up 2+3.
#
# Idempotent; safe to run repeatedly.
set -euo pipefail

echo "==> rewriting billing webhook URLs to in-cluster Service..."
for kind in MutatingWebhookConfiguration ValidatingWebhookConfiguration; do
  names=$(task test-infra:kubectl -- get "$kind" \
    -l kustomize.toolkit.fluxcd.io/name=billing \
    -o jsonpath='{.items[*].metadata.name}')
  for name in ${names}; do
    task test-infra:kubectl -- get "$kind" "$name" -o json \
      | python3 hack/rewrite-billing-webhook.py one \
      | (task test-infra:kubectl -- replace -f - >/dev/null || true)
  done
done

echo "==> extending billing-serving-cert DNS names..."
task test-infra:kubectl -- -n billing-system patch certificate billing-serving-cert \
  --type=json \
  -p='[{"op":"replace","path":"/spec/dnsNames","value":["billing-webhook.billing-system.svc","billing-webhook.billing-system.svc.cluster.local","host.docker.internal"]}]' \
  >/dev/null || true

echo "==> pinning billing-config.webhookServer.port..."
task test-infra:kubectl -- -n billing-system patch configmap billing-config \
  --type=merge \
  -p '{"data":{"config.yaml":"apiVersion: apiserver.config.miloapis.com/v1alpha1\nkind: BillingOperator\nmetricsServer:\n  bindAddress: \"0\"\nwebhookServer:\n  port: 9443\n"}}' \
  >/dev/null || true

echo "==> restarting billing-controller-manager..."
task test-infra:kubectl -- -n billing-system rollout restart \
  deployment/billing-controller-manager >/dev/null
task test-infra:kubectl -- -n billing-system rollout status \
  deployment/billing-controller-manager --timeout=120s

echo "==> billing webhook shim applied"
