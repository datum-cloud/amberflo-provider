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
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func respWithStatus(status int, retryAfter string) *http.Response {
	h := http.Header{}
	if retryAfter != "" {
		h.Set("Retry-After", retryAfter)
	}
	return &http.Response{StatusCode: status, Header: h}
}

func TestClassify_NilResponseIsTransient(t *testing.T) {
	err := classify(nil, nil, errors.New("connection refused"))
	if !IsTransient(err) {
		t.Fatalf("expected transient, got %T: %v", err, err)
	}
	if IsPermanent(err) {
		t.Fatalf("nil-response error must not be permanent")
	}
}

func TestClassify_Success(t *testing.T) {
	if err := classify(respWithStatus(200, ""), []byte("{}"), nil); err != nil {
		t.Fatalf("expected nil error for 200, got %v", err)
	}
	if err := classify(respWithStatus(204, ""), nil, nil); err != nil {
		t.Fatalf("expected nil error for 204, got %v", err)
	}
}

func TestClassify_429IsTransientWithRetryAfter(t *testing.T) {
	err := classify(respWithStatus(http.StatusTooManyRequests, "2"), []byte("slow down"), nil)
	if !IsTransient(err) {
		t.Fatalf("429 must be transient, got %v", err)
	}
	var te *TransientError
	if !errors.As(err, &te) {
		t.Fatalf("expected TransientError, got %T", err)
	}
	if te.StatusCode != 429 {
		t.Errorf("StatusCode=%d, want 429", te.StatusCode)
	}
	if te.RetryAfter != 2*time.Second {
		t.Errorf("RetryAfter=%v, want 2s", te.RetryAfter)
	}
}

func TestClassify_5xxIsTransient(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		err := classify(respWithStatus(status, ""), []byte("boom"), nil)
		if !IsTransient(err) {
			t.Errorf("status %d should be transient, got %v", status, err)
		}
	}
}

func TestClassify_Auth4xxIsPermanent(t *testing.T) {
	for _, status := range []int{401, 403} {
		err := classify(respWithStatus(status, ""), []byte(`{"error":"bad key"}`), nil)
		if !IsPermanent(err) {
			t.Errorf("status %d should be permanent, got %v", status, err)
		}
		var pe *PermanentError
		if !errors.As(err, &pe) {
			t.Errorf("expected PermanentError, got %T", err)
			continue
		}
		if pe.ResponseBody == "" {
			t.Errorf("ResponseBody should be captured")
		}
	}
}

func TestClassify_Other4xxIsPermanent(t *testing.T) {
	err := classify(respWithStatus(400, ""), []byte("bad request"), nil)
	if !IsPermanent(err) {
		t.Fatalf("status 400 should be permanent: %v", err)
	}
}

func TestClassify_RetryAfterNonNumeric(t *testing.T) {
	// Non-numeric Retry-After that isn't an HTTP date should be 0.
	err := classify(respWithStatus(503, "garbage"), []byte(""), nil)
	var te *TransientError
	if !errors.As(err, &te) {
		t.Fatalf("expected TransientError")
	}
	if te.RetryAfter != 0 {
		t.Errorf("RetryAfter=%v, want 0", te.RetryAfter)
	}
}

func TestErrCustomerNotFound_IsMatchesWrapped(t *testing.T) {
	wrapped := fmt.Errorf("%w: abc", ErrCustomerNotFound)
	if !errors.Is(wrapped, ErrCustomerNotFound) {
		t.Fatalf("errors.Is must traverse wrapping")
	}
}

func TestTransientAndPermanentUnwrap(t *testing.T) {
	inner := errors.New("root cause")
	t.Run("transient", func(t *testing.T) {
		te := &TransientError{Err: inner}
		if !errors.Is(te, inner) {
			t.Fatalf("TransientError should unwrap to inner")
		}
	})
	t.Run("permanent", func(t *testing.T) {
		pe := &PermanentError{Err: inner}
		if !errors.Is(pe, inner) {
			t.Fatalf("PermanentError should unwrap to inner")
		}
	})
}

func TestErrorMessagesMentionStatus(t *testing.T) {
	te := &TransientError{Err: errors.New("x"), StatusCode: 503}
	if got := te.Error(); !containsAll(got, "503", "x") {
		t.Errorf("TransientError message missing expected parts: %q", got)
	}
	pe := &PermanentError{Err: errors.New("y"), StatusCode: 400}
	if got := pe.Error(); !containsAll(got, "400", "y") {
		t.Errorf("PermanentError message missing expected parts: %q", got)
	}
}

func TestNilErrorAccessors(t *testing.T) {
	var te *TransientError
	if got := te.Error(); got != "<nil>" {
		t.Errorf("nil TransientError.Error() = %q", got)
	}
	if te.Unwrap() != nil {
		t.Errorf("nil TransientError.Unwrap() should be nil")
	}
	var pe *PermanentError
	if got := pe.Error(); got != "<nil>" {
		t.Errorf("nil PermanentError.Error() = %q", got)
	}
	if pe.Unwrap() != nil {
		t.Errorf("nil PermanentError.Unwrap() should be nil")
	}
}

func TestErrorMessagesWithoutStatus(t *testing.T) {
	te := &TransientError{Err: errors.New("network boom")}
	if got := te.Error(); !contains(got, "network boom") {
		t.Errorf("TransientError w/o status: %q", got)
	}
	pe := &PermanentError{Err: errors.New("bad input")}
	if got := pe.Error(); !contains(got, "bad input") {
		t.Errorf("PermanentError w/o status: %q", got)
	}
}

func TestClassify_NilResponseAndNilErrorProducesSyntheticMessage(t *testing.T) {
	err := classify(nil, nil, nil)
	if !IsTransient(err) {
		t.Fatalf("nil/nil should be transient: %v", err)
	}
	if !contains(err.Error(), "nil response") {
		t.Errorf("message should mention synthetic cause: %q", err.Error())
	}
}

func TestClassify_TruncatesLargeBody(t *testing.T) {
	huge := make([]byte, 5000)
	for i := range huge {
		huge[i] = 'x'
	}
	err := classify(respWithStatus(400, ""), huge, nil)
	var pe *PermanentError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PermanentError")
	}
	if !contains(pe.ResponseBody, "(truncated)") {
		t.Errorf("expected truncation marker in %d-byte body snippet", len(pe.ResponseBody))
	}
}

func TestClassify_UnexpectedStatusIsPermanent(t *testing.T) {
	// 3xx shouldn't reach classify normally (Go follows redirects), but
	// the default case should treat it as permanent so we surface it.
	err := classify(respWithStatus(302, ""), []byte("moved"), nil)
	if !IsPermanent(err) {
		t.Fatalf("unexpected-status should be permanent, got %v", err)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	future := time.Now().Add(3 * time.Second).UTC().Format(http.TimeFormat)
	got := parseRetryAfter(future)
	// Allow a generous floor since parsing rounds to seconds.
	if got <= 0 || got > 5*time.Second {
		t.Errorf("parseRetryAfter(HTTP date) = %v, want ~3s", got)
	}

	past := time.Now().Add(-5 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("parseRetryAfter(past HTTP date) = %v, want 0", got)
	}
}

func TestParseRetryAfter_Empty(t *testing.T) {
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("parseRetryAfter(\"\") = %v", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
