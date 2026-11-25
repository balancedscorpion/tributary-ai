package requestguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	// DefaultMaxBytes is the local hard cap for marshalled provider JSON bodies.
	// Keep this in code/config rather than environment so provider behavior is
	// deterministic across local launches.
	DefaultMaxBytes int64 = 20 << 20

	ErrorCodeContextOverflow       = "CONTEXT_OVERFLOW"
	ErrorCodeRequestTooLarge       = ErrorCodeContextOverflow
	LegacyErrorCodeRequestTooLarge = "REQUEST_TOO_LARGE"
)

var (
	ErrContextOverflow = errors.New("provider context window or request size exceeded")
	ErrRequestTooLarge = errors.New("provider request body exceeds byte limit")
)

type Context struct {
	ProviderID  string
	Model       string
	RequestKind string
	Endpoint    string
	MaxBytes    int64
}

type RequestTooLargeError struct {
	ProviderID  string
	Model       string
	RequestKind string
	Endpoint    string
	ByteSize    int64
	MaxBytes    int64
}

func (e *RequestTooLargeError) Error() string {
	if e == nil {
		return ErrRequestTooLarge.Error()
	}
	parts := []string{
		fmt.Sprintf("bytes=%d", e.ByteSize),
		fmt.Sprintf("max_bytes=%d", e.MaxBytes),
	}
	if e.ProviderID != "" {
		parts = append(parts, "provider="+e.ProviderID)
	}
	if e.Model != "" {
		parts = append(parts, "model="+e.Model)
	}
	if e.RequestKind != "" {
		parts = append(parts, "request="+e.RequestKind)
	}
	if e.Endpoint != "" {
		parts = append(parts, "endpoint="+e.Endpoint)
	}
	return "provider request body exceeds byte limit (" + strings.Join(parts, ", ") + ")"
}

func (e *RequestTooLargeError) Is(target error) bool {
	return target == ErrRequestTooLarge || target == ErrContextOverflow
}

func (e *RequestTooLargeError) Fields() map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{
		"provider":     e.ProviderID,
		"model":        e.Model,
		"request_kind": e.RequestKind,
		"endpoint":     e.Endpoint,
		"bytes":        e.ByteSize,
		"max_bytes":    e.MaxBytes,
	}
}

func MarshalJSON(payload any, ctx Context) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err := CheckBytes(body, ctx); err != nil {
		return nil, err
	}
	return body, nil
}

func CheckBytes(body []byte, ctx Context) error {
	maxBytes := NormalizeMaxBytes(ctx.MaxBytes)
	byteSize := int64(len(body))
	if byteSize <= maxBytes {
		return nil
	}
	return &RequestTooLargeError{
		ProviderID:  strings.TrimSpace(ctx.ProviderID),
		Model:       strings.TrimSpace(ctx.Model),
		RequestKind: strings.TrimSpace(ctx.RequestKind),
		Endpoint:    SanitizedEndpoint(ctx.Endpoint),
		ByteSize:    byteSize,
		MaxBytes:    maxBytes,
	}
}

func NormalizeMaxBytes(maxBytes int64) int64 {
	if maxBytes <= 0 {
		return DefaultMaxBytes
	}
	return maxBytes
}

func IsTooLarge(err error) bool {
	return errors.Is(err, ErrRequestTooLarge)
}

func IsContextOverflow(err error) bool {
	return errors.Is(err, ErrContextOverflow)
}

func ErrorCodeFor(err error) string {
	if IsContextOverflow(err) {
		return ErrorCodeContextOverflow
	}
	return "PROVIDER_ERROR"
}

func NormalizeProviderError(statusCode int, message string) (code string, retryable bool) {
	if IsProviderContextOverflow(statusCode, message) {
		return ErrorCodeContextOverflow, false
	}
	return "HTTP_ERROR", statusCode >= 500 || statusCode == 429 || statusCode == 408
}

func NormalizeStreamError(message string) (code string, retryable bool) {
	if IsProviderContextOverflow(0, message) {
		return ErrorCodeContextOverflow, false
	}
	return "PROVIDER_ERROR", true
}

func IsProviderContextOverflow(statusCode int, message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return statusCode == httpStatusRequestEntityTooLarge
	}
	if statusCode == httpStatusRequestEntityTooLarge {
		return true
	}

	knownSignals := []string{
		"context_length_exceeded",
		"maximum context length",
		"context window",
		"context limit",
		"prompt is too long",
		"input is too long",
		"too many input tokens",
		"request too large",
		"payload too large",
		"body too large",
		"input length and max_tokens exceed context",
		"exceeds the maximum number of tokens",
		"exceeds maximum number of tokens",
		"exceeded maximum token limit",
		"exceeds token limit",
		"exceed the max input tokens",
		"input token count",
	}
	for _, signal := range knownSignals {
		if strings.Contains(msg, signal) {
			return true
		}
	}

	if statusCode >= 400 && statusCode < 500 {
		hasLimit := strings.Contains(msg, "exceed") || strings.Contains(msg, "too long") || strings.Contains(msg, "limit")
		hasContext := strings.Contains(msg, "context") || strings.Contains(msg, "token") || strings.Contains(msg, "prompt")
		return hasLimit && hasContext
	}
	return false
}

func SanitizedEndpoint(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	u, err := url.Parse(trimmed)
	if err != nil || u == nil {
		return stripQueryAndFragment(trimmed)
	}
	if u.Host == "" {
		return stripQueryAndFragment(trimmed)
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.Scheme == "" {
		return u.Host + path
	}
	return u.Scheme + "://" + u.Host + path
}

const httpStatusRequestEntityTooLarge = 413

func stripQueryAndFragment(raw string) string {
	if idx := strings.IndexAny(raw, "?#"); idx >= 0 {
		return raw[:idx]
	}
	return raw
}
