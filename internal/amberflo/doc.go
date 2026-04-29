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

// Package amberflo provides a typed HTTP client for the Amberflo control
// plane (https://app.amberflo.io). It models the narrow slice of Amberflo's
// surface the amberflo-provider controller needs for MVP: customer
// create/update/get and soft-disable.
//
// The client is intentionally thin: it authenticates with the X-API-KEY
// header, serializes the provider's DesiredCustomer shape into Amberflo's
// wire format, classifies HTTP responses into transient vs. permanent
// errors for the reconciler, applies a shared token-bucket rate limiter,
// and retries transient failures with exponential backoff honouring
// Retry-After.
//
// Usage events are NOT part of this package's scope. Workloads push usage
// directly to ingest.amberflo.io; this package only talks to the control
// plane.
package amberflo
