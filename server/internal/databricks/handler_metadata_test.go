package databricks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

type metadataRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn metadataRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func newDatabricksMetadataTestHandler(t *testing.T, transport http.RoundTripper) *Handler {
	t.Helper()
	settings, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	next := settings.Get()
	host := "https://workspace.gcp.databricks.com"
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

func TestDatabricksMetadataHandlersUseBoundedProviderPagination(t *testing.T) {
	const opaqueToken = "opaque+/= page"
	const nextToken = "next+/= page"
	tests := []struct {
		name             string
		localPath        string
		upstreamPath     string
		resultKey        string
		pageToken        string
		maxResults       string
		requiredUpstream map[string]string
	}{
		{
			name:         "catalogs remain callable without a page token",
			localPath:    "/api/databricks/catalogs",
			upstreamPath: "/api/2.1/unity-catalog/catalogs",
			resultKey:    "catalogs",
			maxResults:   "100",
		},
		{
			name:         "schemas forward an opaque page token",
			localPath:    "/api/databricks/schemas?catalog_name=main",
			upstreamPath: "/api/2.1/unity-catalog/schemas",
			resultKey:    "schemas",
			pageToken:    opaqueToken,
			maxResults:   "100",
			requiredUpstream: map[string]string{
				"catalog_name": "main",
			},
		},
		{
			name:         "tables omit unused heavy fields",
			localPath:    "/api/databricks/tables?catalog_name=main&schema_name=default",
			upstreamPath: "/api/2.1/unity-catalog/tables",
			resultKey:    "tables",
			pageToken:    opaqueToken,
			maxResults:   "50",
			requiredUpstream: map[string]string{
				"catalog_name":    "main",
				"schema_name":     "default",
				"omit_columns":    "true",
				"omit_properties": "true",
				"omit_username":   "true",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var upstreamURL *url.URL
			payload, err := json.Marshal(map[string]any{
				test.resultKey:    []map[string]any{{"name": "item"}},
				"next_page_token": nextToken,
			})
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			handler := newDatabricksMetadataTestHandler(t, metadataRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				copyURL := *request.URL
				upstreamURL = &copyURL
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(string(payload))),
					Request:    request,
				}, nil
			}))

			localTarget := test.localPath
			separator := "?"
			if strings.Contains(localTarget, "?") {
				separator = "&"
			}
			if test.pageToken != "" {
				localTarget += separator + url.Values{"page_token": []string{test.pageToken}}.Encode()
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://local"+localTarget, nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if upstreamURL == nil {
				t.Fatal("Databricks upstream was not called")
			}
			if upstreamURL.Path != test.upstreamPath {
				t.Fatalf("upstream path=%q, want %q", upstreamURL.Path, test.upstreamPath)
			}
			query := upstreamURL.Query()
			if got := query.Get("max_results"); got != test.maxResults {
				t.Fatalf("max_results=%q, want %q", got, test.maxResults)
			}
			if got := query.Get("page_token"); got != test.pageToken {
				t.Fatalf("page_token=%q, want %q", got, test.pageToken)
			}
			for key, want := range test.requiredUpstream {
				if got := query.Get(key); got != want {
					t.Fatalf("%s=%q, want %q", key, got, want)
				}
			}

			var response map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if hasMore, _ := response["has_more"].(bool); !hasMore {
				t.Fatalf("has_more=%#v, want true", response["has_more"])
			}
			if got, _ := response["next_page_token"].(string); got != nextToken {
				t.Fatalf("next_page_token=%q, want %q", got, nextToken)
			}
		})
	}
}

func TestDoDatabricksMalformedSuccessIsBadGateway(t *testing.T) {
	var calls atomic.Int32
	handler := newDatabricksMetadataTestHandler(t, metadataRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"tables":[`)),
			Request:    request,
		}, nil
	}))
	var out map[string]any
	err := handler.doDatabricks(context.Background(), "https://workspace.gcp.databricks.com", http.MethodGet, "/api/2.1/unity-catalog/tables", nil, &out)
	if err == nil {
		t.Fatal("expected malformed response error")
	}
	if got := httpStatusForErr(err); got != http.StatusBadGateway {
		t.Fatalf("status=%d, want %d (err=%v)", got, http.StatusBadGateway, err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "unexpected end of json") {
		t.Fatalf("raw JSON decoder error leaked: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("GET attempts=%d, want 3", got)
	}
}

type unexpectedEOFBody struct {
	read bool
}

func (body *unexpectedEOFBody) Read(target []byte) (int, error) {
	if !body.read {
		body.read = true
		return copy(target, `{"tables":[`), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func (*unexpectedEOFBody) Close() error { return nil }

func TestDoDatabricksReadFailureIsNotDecodedAsPartialJSON(t *testing.T) {
	handler := newDatabricksMetadataTestHandler(t, metadataRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &unexpectedEOFBody{},
			Request:    request,
		}, nil
	}))
	var out map[string]any
	err := handler.doDatabricks(context.Background(), "https://workspace.gcp.databricks.com", http.MethodGet, "/api/2.1/unity-catalog/tables", nil, &out)
	if err == nil {
		t.Fatal("expected response read error")
	}
	if got := httpStatusForErr(err); got != http.StatusBadGateway {
		t.Fatalf("status=%d, want %d (err=%v)", got, http.StatusBadGateway, err)
	}
	if err.Error() != "failed to read Databricks API response" {
		t.Fatalf("error=%q, want stable upstream read error", err.Error())
	}
}

func TestDoDatabricksContextDeadlineIsGatewayTimeout(t *testing.T) {
	handler := newDatabricksMetadataTestHandler(t, metadataRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	var out map[string]any
	err := handler.doDatabricks(ctx, "https://workspace.gcp.databricks.com", http.MethodGet, "/api/2.1/unity-catalog/tables", nil, &out)
	if err == nil {
		t.Fatal("expected deadline error")
	}
	if got := httpStatusForErr(err); got != http.StatusGatewayTimeout {
		t.Fatalf("status=%d, want %d (err=%v)", got, http.StatusGatewayTimeout, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error should retain deadline cause: %v", err)
	}
}

func TestDoDatabricksRetriesTransientGetOnly(t *testing.T) {
	var calls atomic.Int32
	handler := newDatabricksMetadataTestHandler(t, metadataRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		status := http.StatusServiceUnavailable
		body := `{"message":"try again"}`
		if call == 2 {
			status = http.StatusOK
			body = `{"catalogs":[{"name":"main"}]}`
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	}))
	var out struct {
		Catalogs []struct {
			Name string `json:"name"`
		} `json:"catalogs"`
	}
	if err := handler.doDatabricks(context.Background(), "https://workspace.gcp.databricks.com", http.MethodGet, "/api/2.1/unity-catalog/catalogs", nil, &out); err != nil {
		t.Fatalf("doDatabricks: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("GET attempts=%d, want 2", got)
	}
	if len(out.Catalogs) != 1 || out.Catalogs[0].Name != "main" {
		t.Fatalf("catalogs=%#v", out.Catalogs)
	}
}
