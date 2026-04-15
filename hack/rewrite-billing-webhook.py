#!/usr/bin/env python3
"""Rewrite billing webhook configurations stdin -> stdout.

Replaces any webhook clientConfig.url containing host.docker.internal
(or any other URL) with an in-cluster Service reference pointing at
billing-webhook.billing-system:443. Idempotent: untouched configs pass
through with no changes and apply as a no-op.
"""
import json
import sys


def rewrite(cfg):
    changed = False
    for w in cfg.get("webhooks", []):
        cc = w.get("clientConfig", {})
        url = cc.get("url")
        if not url:
            continue
        # Extract path component.
        path = "/" + url.split("/", 3)[-1]
        cc.pop("url", None)
        cc["service"] = {
            "name": "billing-webhook",
            "namespace": "billing-system",
            "path": path,
            "port": 443,
        }
        changed = True
    return changed


def main() -> int:
    mode = sys.argv[1] if len(sys.argv) > 1 else "list"
    data = json.load(sys.stdin)
    if mode == "one":
        # Single object on stdin: rewrite and emit it. Emit even if
        # unchanged — the caller uses `kubectl replace` which is a no-op
        # when the object matches what's stored.
        rewrite(data)
        json.dump(data, sys.stdout)
        return 0

    kept = []
    for cfg in data.get("items", []):
        if rewrite(cfg):
            kept.append(cfg)
    if not kept:
        return 0
    out = {
        "apiVersion": "v1",
        "kind": "List",
        "items": kept,
    }
    json.dump(out, sys.stdout)
    return 0


if __name__ == "__main__":
    sys.exit(main())
