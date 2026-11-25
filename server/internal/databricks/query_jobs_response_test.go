package databricks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

const statementResponseTestHost = "https://workspace.gcp.databricks.com"

type statementResponseRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn statementResponseRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func newStatementResponseTestHandler(t *testing.T, transport http.RoundTripper) *Handler {
	t.Helper()
	settings, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	host := statementResponseTestHost
	next := settings.Get()
	next.DatabricksHost = &host
	if err := settings.Save(next); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}
	secretStore, err := secrets.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := secretStore.Init("test-passphrase"); err != nil {
		t.Fatalf("secrets.Init: %v", err)
	}
	if err := secretStore.Unlock("test-passphrase"); err != nil {
		t.Fatalf("secrets.Unlock: %v", err)
	}
	if err := secretStore.SetDatabricksPAT("test-token"); err != nil {
		t.Fatalf("SetDatabricksPAT: %v", err)
	}
	handler := NewHandler(settings, secretStore)
	handler.httpc = &http.Client{Transport: transport}
	return handler
}

type statementUnexpectedEOFBody struct {
	read bool
}

func (body *statementUnexpectedEOFBody) Read(target []byte) (int, error) {
	if !body.read {
		body.read = true
		return copy(target, `{"data_array":[`), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func (*statementUnexpectedEOFBody) Close() error { return nil }

func TestDoStatementAPIMalformedChunkResponseIsTypedAndNotRetried(t *testing.T) {
	var calls atomic.Int32
	handler := newStatementResponseTestHandler(t, statementResponseRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data_array":[`)),
			Request:    request,
		}, nil
	}))

	var out statementResult
	err := handler.doStatementAPI(context.Background(), statementResponseTestHost, http.MethodGet, "/api/2.0/sql/statements/statement-1/result/chunks/0", nil, &out)
	if err == nil {
		t.Fatal("expected malformed response error")
	}
	if got := StatementHTTPStatus(err); got != http.StatusBadGateway {
		t.Fatalf("status=%d, want %d (err=%v)", got, http.StatusBadGateway, err)
	}
	if err.Error() != "Databricks API returned malformed JSON" {
		t.Fatalf("error=%q, want stable malformed-response message", err.Error())
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("malformed response did not retain JSON cause: %T %v", err, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("consumable chunk GET attempts=%d, want 1", got)
	}
}

func TestDoStatementAPIChunkReadFailureIsTypedAndNotRetried(t *testing.T) {
	var calls atomic.Int32
	handler := newStatementResponseTestHandler(t, statementResponseRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &statementUnexpectedEOFBody{},
			Request:    request,
		}, nil
	}))

	var out statementResult
	err := handler.doStatementAPI(context.Background(), statementResponseTestHost, http.MethodGet, "/api/2.0/sql/statements/statement-1/result/chunks/0", nil, &out)
	if err == nil {
		t.Fatal("expected response read error")
	}
	if got := StatementHTTPStatus(err); got != http.StatusBadGateway {
		t.Fatalf("status=%d, want %d (err=%v)", got, http.StatusBadGateway, err)
	}
	if err.Error() != "failed to read Databricks API response" {
		t.Fatalf("error=%q, want stable read-failure message", err.Error())
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read failure did not retain unexpected-EOF cause: %T %v", err, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("consumable chunk GET attempts=%d, want 1", got)
	}
}

func TestDoStatementAPIEmptySuccessIsTypedBadGateway(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "empty body", body: ""},
		{name: "JSON null", body: "  null\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := newStatementResponseTestHandler(t, statementResponseRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(test.body)),
					Request:    request,
				}, nil
			}))

			var out statementResponse
			err := handler.doStatementAPI(context.Background(), statementResponseTestHost, http.MethodGet, "/api/2.0/sql/statements/statement-1", nil, &out)
			if err == nil {
				t.Fatal("expected empty response error")
			}
			if got := StatementHTTPStatus(err); got != http.StatusBadGateway {
				t.Fatalf("status=%d, want %d (err=%v)", got, http.StatusBadGateway, err)
			}
			var apiErr *StatementAPIError
			if !errors.As(err, &apiErr) || apiErr.Cause == nil {
				t.Fatalf("empty response did not retain a typed cause: %T %v", err, err)
			}
		})
	}
}

func TestDoStatementAPIDeadlineIsTypedGatewayTimeout(t *testing.T) {
	handler := newStatementResponseTestHandler(t, statementResponseRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	var out statementResponse
	err := handler.doStatementAPI(ctx, statementResponseTestHost, http.MethodGet, "/api/2.0/sql/statements/statement-1", nil, &out)
	if err == nil {
		t.Fatal("expected deadline error")
	}
	if got := StatementHTTPStatus(err); got != http.StatusGatewayTimeout {
		t.Fatalf("status=%d, want %d (err=%v)", got, http.StatusGatewayTimeout, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error did not retain its cause: %T %v", err, err)
	}
}
