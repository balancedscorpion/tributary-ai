package requestguard

import (
	"errors"
	"strings"
	"testing"
)

func TestMarshalJSONReturnsTypedTooLargeError(t *testing.T) {
	_, err := MarshalJSON(map[string]any{"message": strings.Repeat("x", 64)}, Context{
		ProviderID:  "test-provider",
		Model:       "test-model",
		RequestKind: "unit",
		Endpoint:    "https://example.test/v1/messages?api_key=secret#frag",
		MaxBytes:    8,
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("errors.Is(err, ErrRequestTooLarge)=false for %v", err)
	}
	if !errors.Is(err, ErrContextOverflow) {
		t.Fatalf("errors.Is(err, ErrContextOverflow)=false for %v", err)
	}
	if got := ErrorCodeFor(err); got != ErrorCodeContextOverflow {
		t.Fatalf("ErrorCodeFor(err) = %q, want %q", got, ErrorCodeContextOverflow)
	}
	var tooLarge *RequestTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("errors.As RequestTooLargeError=false for %T", err)
	}
	if tooLarge.ProviderID != "test-provider" || tooLarge.Model != "test-model" || tooLarge.RequestKind != "unit" {
		t.Fatalf("unexpected error context: %#v", tooLarge)
	}
	if tooLarge.Endpoint != "https://example.test/v1/messages" {
		t.Fatalf("endpoint = %q, want sanitized endpoint without query", tooLarge.Endpoint)
	}
	if tooLarge.ByteSize <= tooLarge.MaxBytes {
		t.Fatalf("byte guard did not record overflow: %#v", tooLarge)
	}
}

func TestMarshalJSONUsesDefaultMaxBytes(t *testing.T) {
	body, err := MarshalJSON(map[string]any{"ok": true}, Context{})
	if err != nil {
		t.Fatalf("MarshalJSON returned error: %v", err)
	}
	if len(body) == 0 {
		t.Fatalf("expected marshalled body")
	}
	if NormalizeMaxBytes(0) != DefaultMaxBytes {
		t.Fatalf("NormalizeMaxBytes(0) = %d, want %d", NormalizeMaxBytes(0), DefaultMaxBytes)
	}
}

func TestNormalizeProviderErrorDetectsContextPressure(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		message    string
	}{
		{
			name:       "openai context length",
			statusCode: 400,
			message:    `{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 128000 tokens."}}`,
		},
		{
			name:       "anthropic prompt too long",
			statusCode: 400,
			message:    `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`,
		},
		{
			name:       "gemini input token count",
			statusCode: 400,
			message:    "The input token count (1200000) exceeds the maximum number of tokens allowed",
		},
		{
			name:       "payload too large status",
			statusCode: 413,
			message:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, retryable := NormalizeProviderError(tt.statusCode, tt.message)
			if code != ErrorCodeContextOverflow {
				t.Fatalf("code = %q, want %q", code, ErrorCodeContextOverflow)
			}
			if retryable {
				t.Fatalf("retryable = true, want false")
			}
		})
	}
}

func TestNormalizeProviderErrorKeepsNonContextHTTPHandling(t *testing.T) {
	code, retryable := NormalizeProviderError(429, "rate limit")
	if code != "HTTP_ERROR" {
		t.Fatalf("code = %q, want HTTP_ERROR", code)
	}
	if !retryable {
		t.Fatalf("retryable = false, want true")
	}
}
