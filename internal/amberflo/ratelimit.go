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

	"golang.org/x/time/rate"
)

// rateLimiter is a thin wrapper over golang.org/x/time/rate.Limiter. A
// single limiter is shared across every method on a Client, so concurrent
// reconciles cannot collectively exceed the configured QPS budget.
type rateLimiter struct {
	limiter *rate.Limiter
}

// newRateLimiter constructs a limiter with the given per-second rate. A
// non-positive rate defaults to 10 req/s with burst 10, matching Amberflo
// community guidance (their rate limits are not officially published).
func newRateLimiter(perSec int) *rateLimiter {
	if perSec <= 0 {
		perSec = 10
	}
	return &rateLimiter{
		limiter: rate.NewLimiter(rate.Limit(perSec), perSec),
	}
}

// Wait blocks until the limiter admits one request or ctx is cancelled.
func (r *rateLimiter) Wait(ctx context.Context) error {
	return r.limiter.Wait(ctx)
}
