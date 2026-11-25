package providers

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/providers/openai_compat"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

func factoryBedrockEventStreamFrame(t *testing.T, inner string) []byte {
	t.Helper()
	wrapped, err := json.Marshal(map[string]any{
		"chunk": map[string]any{
			"bytes": base64.StdEncoding.EncodeToString([]byte(inner)),
		},
	})
	if err != nil {
		t.Fatalf("marshal Bedrock event stream payload: %v", err)
	}
	total := 12 + len(wrapped) + 4
	frame := make([]byte, total)
	binary.BigEndian.PutUint32(frame[0:4], uint32(total))
	copy(frame[12:], wrapped)
	return frame
}

func TestFactoryCreateProvider_CompatibleUsesActiveProviderConfigEndpointSuffix(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotModel string
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotModel, _ = body["model"].(string)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"rid-compatible\",\"output_text\":\"ok\"}}\n\n")
	}))
	defer srv.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	if err := secretsStore.SetCompatibleAPIKey("cfg-compatible", "compatible-key"); err != nil {
		t.Fatalf("SetCompatibleAPIKey: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Provider = config.ProviderSelection{
		ProviderID: "openai",
		Model:      "stale-model",
		ProfileID:  nil,
	}
	settings.ProviderConfigs = []config.ProviderConfig{
		{
			ID:             "cfg-compatible",
			DisplayName:    "Custom Compatible",
			ProviderID:     "compatible",
			Model:          "cfg-model",
			ApiType:        strPtr("responses"),
			BaseURL:        strPtr(srv.URL + "/v1"),
			AuthType:       strPtr("api_key"),
			AuthHeader:     strPtr("Authorization"),
			AuthScheme:     strPtr("Bearer"),
			EndpointSuffix: strPtr("/beta"),
			QueryParams:    map[string]string{"via": "factory"},
			ModelInBody:    boolPtr(true),
			ModelInPath:    boolPtr(false),
		},
	}
	settings.ActiveProviderConfigID = strPtr("cfg-compatible")
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProvider("")
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := streamProvider(ctx, provider)
	if err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected events from provider")
	}

	last := events[len(events)-1]
	if last.Event != types.EventFinal {
		t.Fatalf("expected final event, got %q", last.Event)
	}

	if gotPath != "/v1/responses/beta?via=factory" {
		t.Fatalf("request path = %q, want %q", gotPath, "/v1/responses/beta?via=factory")
	}
	if gotModel != "cfg-model" {
		t.Fatalf("request model = %q, want %q", gotModel, "cfg-model")
	}
	if gotAuth != "Bearer compatible-key" {
		t.Fatalf("authorization header = %q, want %q", gotAuth, "Bearer compatible-key")
	}
}

func TestFactoryCreateProviderForSelectionDoesNotDriftToNewActiveConfig(t *testing.T) {
	t.Parallel()

	requestedCalls := 0
	activeCalls := 0
	requestedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"run-selection\",\"output_text\":\"ok\"}}\n\n")
	}))
	defer requestedServer.Close()
	activeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		activeCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer activeServer.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	settings := config.DefaultSettings()
	noAuth := "none"
	responses := "responses"
	settings.ProviderConfigs = []config.ProviderConfig{
		{ID: "run-config", ProviderID: "compatible", Model: "run-model", BaseURL: &requestedServer.URL, AuthType: &noAuth, ApiType: &responses},
		{ID: "new-active-config", ProviderID: "compatible", Model: "new-model", BaseURL: &activeServer.URL, AuthType: &noAuth, ApiType: &responses},
	}
	settings.ActiveProviderConfigID = strPtr("new-active-config")
	settings.Provider = config.ProviderSelection{ProviderID: "compatible", Model: "new-model", ProfileID: strPtr("new-active-config")}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	snapshot, err := factory.SnapshotConnection(settingsStore.Get(), "run-config", "run-model", "", "")
	if err != nil {
		t.Fatalf("SnapshotConnection: %v", err)
	}
	provider, err := factory.CreateProviderFromSnapshot(snapshot, HostedToolPolicy{})
	if err != nil {
		t.Fatalf("CreateProviderFromSnapshot: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := streamProvider(ctx, provider); err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if requestedCalls != 1 || activeCalls != 0 {
		t.Fatalf("requested/active calls=%d/%d, want 1/0", requestedCalls, activeCalls)
	}
	if _, err := factory.SnapshotConnection(settingsStore.Get(), "deleted-config", "run-model", "", ""); err == nil {
		t.Fatal("missing snapshotted config error=nil")
	}
}

func TestDispatchSnapshotSurvivesQueueAndToolLoopSettingsMutation(t *testing.T) {
	t.Parallel()

	snapshotCalls := 0
	editedCalls := 0
	snapshotServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshotCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"immutable-selection\",\"output_text\":\"ok\"}}\n\n")
	}))
	defer snapshotServer.Close()
	editedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		editedCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer editedServer.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	noAuth := "none"
	responses := "responses"
	configSnapshot := config.ProviderConfig{
		ID: "stable-id", ProviderID: "compatible", Model: "run-model",
		BaseURL: &snapshotServer.URL, AuthType: &noAuth, ApiType: &responses,
	}
	editedConfig := configSnapshot
	editedConfig.BaseURL = &editedServer.URL
	settings := config.DefaultSettings()
	settings.ProviderConfigs = []config.ProviderConfig{configSnapshot}
	settings.ActiveProviderConfigID = strPtr("stable-id")
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	snapshot, err := factory.SnapshotConnection(settingsStore.Get(), "stable-id", "run-model", "", "")
	if err != nil {
		t.Fatalf("SnapshotConnection: %v", err)
	}
	settings.ProviderConfigs = []config.ProviderConfig{editedConfig}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("edited settings save: %v", err)
	}
	provider, err := factory.CreateProviderFromSnapshot(snapshot, HostedToolPolicy{})
	if err != nil {
		t.Fatalf("CreateProviderFromSnapshot: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := streamProvider(ctx, provider); err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	// A second provider creation represents a post-tool-call engine iteration.
	// It must reuse the same queued snapshot rather than reload edited settings.
	provider, err = factory.CreateProviderFromSnapshot(snapshot, HostedToolPolicy{})
	if err != nil {
		t.Fatalf("CreateProviderFromSnapshot follow-up: %v", err)
	}
	if _, err := streamProvider(ctx, provider); err != nil {
		t.Fatalf("streamProvider follow-up: %v", err)
	}
	nestedCtx := WithDispatchSnapshot(ctx, snapshot)
	provider, err = factory.CreateProviderForContext(nestedCtx, "", HostedToolPolicy{})
	if err != nil {
		t.Fatalf("CreateProviderForContext nested: %v", err)
	}
	if _, err := streamProvider(nestedCtx, provider); err != nil {
		t.Fatalf("streamProvider nested: %v", err)
	}
	if _, err := factory.CreateProviderForContext(nestedCtx, "other-model", HostedToolPolicy{}); err == nil {
		t.Fatal("nested model override escaped finalized target")
	}
	if snapshotCalls != 3 || editedCalls != 0 {
		t.Fatalf("snapshot/edited calls=%d/%d, want 3/0", snapshotCalls, editedCalls)
	}
}

func TestProviderAPITypeUsesActualNativeAdapterProtocol(t *testing.T) {
	t.Parallel()
	configuredChat := "chat_completions"
	configuredMessages := "messages"
	configuredResponses := "responses"
	settings := config.DefaultSettings()
	tests := []struct {
		provider string
		apiType  *string
		want     string
	}{
		{provider: "openai", apiType: &configuredChat, want: "responses"},
		{provider: "anthropic", apiType: &configuredChat, want: "messages"},
		{provider: "gemini", apiType: &configuredChat, want: "generate_content"},
		{provider: "bedrock", apiType: &configuredMessages, want: "invoke_stream"},
		{provider: "compatible", apiType: &configuredResponses, want: "responses"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.provider, func(t *testing.T) {
			cfg := &config.ProviderConfig{ProviderID: test.provider, ApiType: test.apiType}
			if got := providerAPIType(settings, test.provider, cfg, "", nil); got != test.want {
				t.Fatalf("providerAPIType(%s)=%q, want %q", test.provider, got, test.want)
			}
		})
	}
}

func TestFactoryCreateProviderForSelectionUsesExactLegacyProfile(t *testing.T) {
	t.Parallel()

	requestedCalls := 0
	activeCalls := 0
	requestedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"legacy-selection\",\"output_text\":\"ok\"}}\n\n")
	}))
	defer requestedServer.Close()
	activeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		activeCalls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer activeServer.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	settings := config.DefaultSettings()
	settings.ProviderConfigs = nil
	settings.ActiveProviderConfigID = nil
	settings.Provider = config.ProviderSelection{ProviderID: "compatible", Model: "new-model", ProfileID: strPtr("new-profile")}
	settings.CompatibleProfiles = []config.CompatibleProfile{
		{ID: "run-profile", DisplayName: "Run profile", ApiType: "responses", BaseURL: requestedServer.URL, Auth: config.ProviderAuth{Type: "none", Header: "Authorization"}},
		{ID: "new-profile", DisplayName: "New profile", ApiType: "responses", BaseURL: activeServer.URL, Auth: config.ProviderAuth{Type: "none", Header: "Authorization"}},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	usageRepository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("usage.NewJSONRepository: %v", err)
	}
	factory := NewFactory(settingsStore, secretsStore, usageRepository)
	snapshot, err := factory.SnapshotLegacyProfile(settingsStore.Get(), "run-profile", "run-model", "")
	if err != nil {
		t.Fatalf("SnapshotLegacyProfile: %v", err)
	}
	provider, err := factory.CreateProviderFromSnapshot(snapshot, HostedToolPolicy{})
	if err != nil {
		t.Fatalf("CreateProviderFromSnapshot: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := streamProvider(ctx, provider); err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if requestedCalls != 1 || activeCalls != 0 {
		t.Fatalf("requested/active calls=%d/%d, want 1/0", requestedCalls, activeCalls)
	}
	invocations, err := usageRepository.List(context.Background(), usage.Query{Limit: 10})
	if err != nil {
		t.Fatalf("usage list: %v", err)
	}
	if len(invocations) != 1 || invocations[0].ConnectionID != nil || invocations[0].ProfileID == nil || *invocations[0].ProfileID != "run-profile" || invocations[0].UpstreamRequestCount != 1 {
		t.Fatalf("legacy usage attribution=%#v", invocations)
	}

	if _, err := factory.SnapshotLegacyProfile(settingsStore.Get(), "deleted-profile", "run-model", ""); err == nil {
		t.Fatal("missing snapshotted legacy profile error=nil")
	}
}

func TestFactoryCreateProvider_CompatibleNoAuthDoesNotRequireUnlockedSecrets(t *testing.T) {
	t.Parallel()

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"rid-compatible-noauth\",\"output_text\":\"ok\"}}\n\n")
	}))
	defer srv.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore, err := secrets.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Provider = config.ProviderSelection{
		ProviderID: "compatible",
		Model:      "cfg-model",
	}
	settings.ProviderConfigs = []config.ProviderConfig{
		{
			ID:          "cfg-compatible-noauth",
			DisplayName: "Local no-auth compatible",
			ProviderID:  "compatible",
			Model:       "cfg-model",
			ApiType:     strPtr("responses"),
			BaseURL:     strPtr(srv.URL + "/v1"),
			AuthType:    strPtr("none"),
			ModelInBody: boolPtr(true),
			ModelInPath: boolPtr(false),
		},
	}
	settings.ActiveProviderConfigID = strPtr("cfg-compatible-noauth")
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProvider("")
	if err != nil {
		t.Fatalf("CreateProvider with locked secrets and no auth: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events, err := streamProvider(ctx, provider)
	if err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected events from provider")
	}
	if gotAuth != "" {
		t.Fatalf("authorization header = %q, want empty", gotAuth)
	}
}

func TestFactoryCreateProvider_BedrockResponsesUsesActiveProviderConfigEndpointSuffix(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotModel string
	var gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotModel, _ = body["model"].(string)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"rid-bedrock\",\"output_text\":\"ok\"}}\n\n")
	}))
	defer srv.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	if err := secretsStore.SetBedrockAPIKey("bedrock-key"); err != nil {
		t.Fatalf("SetBedrockAPIKey: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Provider = config.ProviderSelection{
		ProviderID: "bedrock",
		Model:      "legacy-bedrock-model",
		ProfileID:  nil,
	}
	settings.ProviderConfigs = []config.ProviderConfig{
		{
			ID:             "cfg-bedrock",
			DisplayName:    "Bedrock Responses",
			ProviderID:     "bedrock",
			Model:          "bedrock-model",
			ApiType:        strPtr("responses"),
			BaseURL:        strPtr(srv.URL + "/openai/v1"),
			AuthType:       strPtr("api_key"),
			AuthHeader:     strPtr("Authorization"),
			AuthScheme:     strPtr("Bearer"),
			EndpointSuffix: strPtr("/suffix"),
			ModelInBody:    boolPtr(true),
			ModelInPath:    boolPtr(false),
		},
	}
	settings.ActiveProviderConfigID = strPtr("cfg-bedrock")
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProvider("")
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := streamProvider(ctx, provider)
	if err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected events from provider")
	}

	last := events[len(events)-1]
	if last.Event != types.EventFinal {
		t.Fatalf("expected final event, got %q", last.Event)
	}

	if gotPath != "/openai/v1/responses/suffix" {
		t.Fatalf("request path = %q, want %q", gotPath, "/openai/v1/responses/suffix")
	}
	if gotModel != "bedrock-model" {
		t.Fatalf("request model = %q, want %q", gotModel, "bedrock-model")
	}
	if gotAuth != "Bearer bedrock-key" {
		t.Fatalf("authorization header = %q, want %q", gotAuth, "Bearer bedrock-key")
	}
}

func TestBedrockCompatibleBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		apiType openai_compat.APIType
		region  string
		raw     string
		want    string
	}{
		{
			name:    "chat completions default uses runtime v1",
			apiType: openai_compat.APIChatCompletions,
			region:  "us-east-1",
			want:    "https://bedrock-runtime.us-east-1.amazonaws.com/v1",
		},
		{
			name:    "responses default uses mantle v1",
			apiType: openai_compat.APIResponses,
			region:  " eu-west-2 ",
			raw:     "  ",
			want:    "https://bedrock-mantle.eu-west-2.api.aws/v1",
		},
		{
			name:    "explicit proxy root is preserved",
			apiType: openai_compat.APIResponses,
			region:  "us-east-1",
			raw:     "https://proxy.example.test/",
			want:    "https://proxy.example.test",
		},
		{
			name:    "explicit proxy path is preserved",
			apiType: openai_compat.APIChatCompletions,
			region:  "us-east-1",
			raw:     " https://proxy.example.test/openai/v1/ ",
			want:    "https://proxy.example.test/openai/v1",
		},
		{
			name:    "runtime root gains v1 for chat completions",
			apiType: openai_compat.APIChatCompletions,
			region:  "ap-southeast-2",
			raw:     "https://bedrock-runtime.ap-southeast-2.amazonaws.com/",
			want:    "https://bedrock-runtime.ap-southeast-2.amazonaws.com/v1",
		},
		{
			name:    "mantle root gains v1 for responses",
			apiType: openai_compat.APIResponses,
			region:  "us-west-2",
			raw:     "https://bedrock-mantle.us-west-2.api.aws",
			want:    "https://bedrock-mantle.us-west-2.api.aws/v1",
		},
		{
			name:    "existing v1 trailing slash is normalized",
			apiType: openai_compat.APIResponses,
			region:  "us-west-2",
			raw:     "https://bedrock-mantle.us-west-2.api.aws/v1/",
			want:    "https://bedrock-mantle.us-west-2.api.aws/v1",
		},
		{
			name:    "different official surface is not rewritten",
			apiType: openai_compat.APIResponses,
			region:  "us-east-1",
			raw:     "https://bedrock-runtime.us-east-1.amazonaws.com",
			want:    "https://bedrock-runtime.us-east-1.amazonaws.com",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := bedrockCompatibleBaseURL(tt.apiType, tt.region, tt.raw); got != tt.want {
				t.Fatalf("bedrockCompatibleBaseURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFactoryCreateProvider_CompatibleResponsesFileInputModeDefaultsNativeSupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		providerID    string
		secret        func(*testing.T, *secrets.Store)
		fileInputMode *string
		wantFilenames []string
	}{
		{
			name:       "generic compatible default",
			providerID: "compatible",
			secret: func(t *testing.T, store *secrets.Store) {
				t.Helper()
				if err := store.SetCompatibleAPIKey("cfg-compatible-files", "compatible-key"); err != nil {
					t.Fatalf("SetCompatibleAPIKey: %v", err)
				}
			},
			wantFilenames: []string{"model.xlsx", "deck.pptx", "deck.key"},
		},
		{
			name:       "bedrock responses default",
			providerID: "bedrock",
			secret: func(t *testing.T, store *secrets.Store) {
				t.Helper()
				if err := store.SetBedrockAPIKey("bedrock-key"); err != nil {
					t.Fatalf("SetBedrockAPIKey: %v", err)
				}
			},
			wantFilenames: []string{"model.xlsx", "deck.pptx", "deck.key"},
		},
		{
			name:          "generic compatible explicit sidecar only",
			providerID:    "compatible",
			fileInputMode: strPtr("sidecar_only"),
			secret: func(t *testing.T, store *secrets.Store) {
				t.Helper()
				if err := store.SetCompatibleAPIKey("cfg-compatible-files", "compatible-key"); err != nil {
					t.Fatalf("SetCompatibleAPIKey: %v", err)
				}
			},
		},
		{
			name:          "generic compatible native supported opt in",
			providerID:    "compatible",
			fileInputMode: strPtr("native_supported"),
			secret: func(t *testing.T, store *secrets.Store) {
				t.Helper()
				if err := store.SetCompatibleAPIKey("cfg-compatible-files", "compatible-key"); err != nil {
					t.Fatalf("SetCompatibleAPIKey: %v", err)
				}
			},
			wantFilenames: []string{"model.xlsx", "deck.pptx", "deck.key"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var gotBody map[string]any

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Fatalf("decode request body: %v", err)
				}

				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "event: response.completed\n")
				_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"rid-files\",\"output_text\":\"ok\"}}\n\n")
			}))
			defer srv.Close()

			settingsStore := newTestSettingsStore(t)
			secretsStore := newUnlockedSecretsStore(t)
			tt.secret(t, secretsStore)

			settings := config.DefaultSettings()
			settings.Provider = config.ProviderSelection{
				ProviderID: tt.providerID,
				Model:      "cfg-model",
				ProfileID:  nil,
			}
			settings.ProviderConfigs = []config.ProviderConfig{
				{
					ID:            "cfg-" + tt.providerID + "-files",
					DisplayName:   "File Mode Test",
					ProviderID:    tt.providerID,
					Model:         "cfg-model",
					ApiType:       strPtr("responses"),
					BaseURL:       strPtr(srv.URL + "/v1"),
					AuthType:      strPtr("api_key"),
					AuthHeader:    strPtr("Authorization"),
					AuthScheme:    strPtr("Bearer"),
					ModelInBody:   boolPtr(true),
					ModelInPath:   boolPtr(false),
					FileInputMode: tt.fileInputMode,
				},
			}
			settings.ActiveProviderConfigID = strPtr("cfg-" + tt.providerID + "-files")
			if err := settingsStore.Save(settings); err != nil {
				t.Fatalf("settings save: %v", err)
			}

			factory := NewFactory(settingsStore, secretsStore)
			provider, err := factory.CreateProvider("")
			if err != nil {
				t.Fatalf("CreateProvider: %v", err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			req := types.ChatRequest{
				SystemPrompt: "",
				Messages: []types.ChatMessage{
					{
						Role:    types.RoleUser,
						Content: strPtr("analyze this spreadsheet"),
						FileAttachments: []types.FileAttachment{
							{
								ArtifactID:       "artifact-xlsx",
								FileName:         "model.xlsx",
								MimeType:         "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
								InlineDataBase64: "UEsDBAo=",
							},
							{
								ArtifactID:       "artifact-pptx",
								FileName:         "deck.pptx",
								MimeType:         "application/vnd.openxmlformats-officedocument.presentationml.presentation",
								InlineDataBase64: "UEsDBAo=",
							},
							{
								ArtifactID:       "artifact-key",
								FileName:         "deck.key",
								MimeType:         "application/vnd.apple.keynote",
								InlineDataBase64: "S0VZ",
							},
						},
					},
				},
			}

			events, err := streamProviderRequest(ctx, provider, req)
			if err != nil {
				t.Fatalf("streamProviderRequest: %v", err)
			}
			if len(events) == 0 || events[len(events)-1].Event != types.EventFinal {
				t.Fatalf("expected final event, got %#v", events)
			}

			gotFilenames := responsesInputFileFilenames(gotBody)
			if strings.Join(gotFilenames, ",") != strings.Join(tt.wantFilenames, ",") {
				t.Fatalf("input_file filenames=%v, want %v; body=%#v", gotFilenames, tt.wantFilenames, gotBody)
			}
		})
	}
}

func TestFactoryCreateProvider_CompatibleChatCompletionsDefaultsNativeSupported(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	if err := secretsStore.SetCompatibleAPIKey("cfg-compatible-chat-files", "compatible-key"); err != nil {
		t.Fatalf("SetCompatibleAPIKey: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Provider = config.ProviderSelection{
		ProviderID: "compatible",
		Model:      "cfg-model",
		ProfileID:  nil,
	}
	settings.ProviderConfigs = []config.ProviderConfig{
		{
			ID:          "cfg-compatible-chat-files",
			DisplayName: "Chat File Mode Test",
			ProviderID:  "compatible",
			Model:       "cfg-model",
			ApiType:     strPtr("chat_completions"),
			BaseURL:     strPtr(srv.URL + "/v1"),
			AuthType:    strPtr("api_key"),
			AuthHeader:  strPtr("Authorization"),
			AuthScheme:  strPtr("Bearer"),
			ModelInBody: boolPtr(true),
			ModelInPath: boolPtr(false),
		},
	}
	settings.ActiveProviderConfigID = strPtr("cfg-compatible-chat-files")
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProvider("")
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: strPtr("analyze this spreadsheet"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-xlsx",
						FileName:         "model.xlsx",
						MimeType:         "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
						InlineDataBase64: "UEsDBAo=",
					},
					{
						ArtifactID:       "artifact-pptx",
						FileName:         "deck.pptx",
						MimeType:         "application/vnd.openxmlformats-officedocument.presentationml.presentation",
						InlineDataBase64: "UEsDBAo=",
					},
					{
						ArtifactID:       "artifact-key",
						FileName:         "deck.key",
						MimeType:         "application/vnd.apple.keynote",
						InlineDataBase64: "S0VZ",
					},
				},
			},
		},
	}

	events, err := streamProviderRequest(ctx, provider, req)
	if err != nil {
		t.Fatalf("streamProviderRequest: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Event != types.EventFinal {
		t.Fatalf("expected final event, got %#v", events)
	}

	gotFilenames := chatCompletionsInputFileFilenames(gotBody)
	if strings.Join(gotFilenames, ",") != "model.xlsx,deck.pptx,deck.key" {
		t.Fatalf("chat completions file filenames=%v, want [model.xlsx deck.pptx deck.key]; body=%#v", gotFilenames, gotBody)
	}
}

func TestCompatibleFileInputModeDefaultsNativeSupported(t *testing.T) {
	t.Parallel()

	if got := compatibleFileInputMode("", openai_compat.APIChatCompletions); got != openai_compat.FileInputNativeSupported {
		t.Fatalf("chat completions default file input mode=%q, want %q", got, openai_compat.FileInputNativeSupported)
	}
	if got := compatibleFileInputMode("", openai_compat.APIResponses); got != openai_compat.FileInputNativeSupported {
		t.Fatalf("responses default file input mode=%q, want %q", got, openai_compat.FileInputNativeSupported)
	}
	if got := compatibleFileInputMode("sidecar_only", openai_compat.APIChatCompletions); got != openai_compat.FileInputSidecarOnly {
		t.Fatalf("explicit sidecar file input mode=%q, want %q", got, openai_compat.FileInputSidecarOnly)
	}
}

func TestFactoryCreateProvider_BedrockMessagesUsesRuntimeInvokeStreamWithCloudCredentials(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotAuth string
	var gotAPIKey string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(factoryBedrockEventStreamFrame(t, `{"type":"message_stop"}`))
	}))
	defer srv.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	if err := secretsStore.SetBedrockAPIKey("stale-bedrock-key"); err != nil {
		t.Fatalf("SetBedrockAPIKey: %v", err)
	}
	if err := secretsStore.SetBedrockCredentials("AKIA_TEST_KEY", "test-secret", ""); err != nil {
		t.Fatalf("SetBedrockCredentials: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Provider = config.ProviderSelection{ProviderID: "bedrock", Model: "legacy-bedrock-model"}
	settings.ProviderConfigs = []config.ProviderConfig{
		{
			ID:         "cfg-bedrock-messages",
			ProviderID: "bedrock",
			Model:      "anthropic.claude-opus-4-7",
			ApiType:    strPtr("messages"),
			BaseURL:    strPtr(srv.URL),
			AuthType:   strPtr("cloud_credentials"),
			Region:     strPtr("us-east-1"),
		},
	}
	settings.ActiveProviderConfigID = strPtr("cfg-bedrock-messages")
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProvider("")
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := streamProvider(ctx, provider)
	if err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Event != types.EventFinal {
		t.Fatalf("expected final event, got %#v", events)
	}

	wantPath := "/model/anthropic.claude-opus-4-7/invoke-with-response-stream"
	if gotPath != wantPath {
		t.Fatalf("request path = %q, want %q", gotPath, wantPath)
	}
	if gotAPIKey != "" {
		t.Fatalf("cloud credentials auth should not send x-api-key, got %q", gotAPIKey)
	}
	if !strings.Contains(gotAuth, "/us-east-1/bedrock/aws4_request") {
		t.Fatalf("authorization should sign Bedrock Runtime requests with bedrock service, got %q", gotAuth)
	}
	if gotBody["anthropic_version"] != "bedrock-2023-05-31" {
		t.Fatalf("unexpected anthropic_version: %#v", gotBody["anthropic_version"])
	}
	if _, ok := gotBody["messages"].([]any); !ok {
		t.Fatalf("expected messages array, got %#v", gotBody["messages"])
	}
	if _, ok := gotBody["model"]; ok {
		t.Fatalf("Bedrock Runtime body should not include model, got %#v", gotBody["model"])
	}
	if _, ok := gotBody["stream"]; ok {
		t.Fatalf("Bedrock Runtime body should not include stream, got %#v", gotBody["stream"])
	}
	if _, ok := gotBody["toolConfig"]; ok {
		t.Fatalf("Bedrock Runtime body should not include toolConfig, got %#v", gotBody["toolConfig"])
	}
}

func TestFactoryCreateProvider_BedrockRejectsUnsupportedExplicitSurface(t *testing.T) {
	t.Parallel()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	if err := secretsStore.SetBedrockAPIKey("bedrock-key"); err != nil {
		t.Fatalf("SetBedrockAPIKey: %v", err)
	}
	apiType := "messages"
	unsupported := "future_surface"
	settings := config.DefaultSettings()
	settings.ProviderConfigs = []config.ProviderConfig{{
		ID: "cfg-bedrock-unsupported", ProviderID: "bedrock", Model: "anthropic.claude-test",
		ApiType: &apiType, APISurface: &unsupported,
	}}
	settings.ActiveProviderConfigID = strPtr("cfg-bedrock-unsupported")
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}
	if _, err := NewFactory(settingsStore, secretsStore).CreateProvider(""); err == nil || !strings.Contains(err.Error(), "unsupported Bedrock API surface") {
		t.Fatalf("CreateProvider error=%v, want unsupported surface failure", err)
	}
}

func TestCapabilityAwareHostedToolPolicyUsesCanonicalEndpointFamily(t *testing.T) {
	t.Parallel()

	enabled := true
	responses := "responses"
	messages := "messages"
	openAIBase := "https://api.openai.com/v1"
	anthropicBase := "https://api.anthropic.com"
	bedrockBase := "https://bedrock-runtime.us-east-1.amazonaws.com"
	tests := []struct {
		name        string
		cfg         config.ProviderConfig
		model       string
		wantWebOff  bool
		wantCodeOff bool
		webID       string
		codeID      string
	}{
		{
			name: "OpenAI", model: "gpt-5", webID: "openai-responses/web_search", codeID: "openai-responses/code_interpreter",
			cfg: config.ProviderConfig{ProviderID: "openai", ApiType: &responses, BaseURL: &openAIBase,
				WebSearch: &config.ProviderWebSearchConfig{Enabled: &enabled}, CodeExecution: &config.ProviderCodeExecutionConfig{Enabled: &enabled}},
		},
		{
			name: "Anthropic", model: "claude-sonnet-4-6", webID: "anthropic-messages/web_search", codeID: "anthropic-messages/code_execution",
			cfg: config.ProviderConfig{ProviderID: "anthropic", ApiType: &messages, BaseURL: &anthropicBase,
				WebSearch: &config.ProviderWebSearchConfig{Enabled: &enabled}, CodeExecution: &config.ProviderCodeExecutionConfig{Enabled: &enabled}},
		},
		{
			name: "Bedrock", model: "anthropic.claude-sonnet", wantWebOff: true, wantCodeOff: true,
			webID: "openai-responses/web_search", codeID: "openai-responses/code_interpreter",
			cfg: config.ProviderConfig{ProviderID: "bedrock", ApiType: &messages, BaseURL: &bedrockBase,
				WebSearch: &config.ProviderWebSearchConfig{Enabled: &enabled}, CodeExecution: &config.ProviderCodeExecutionConfig{Enabled: &enabled}},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			policy := capabilityAwareHostedToolPolicy(test.cfg, test.model, HostedToolPolicy{})
			if got := policy.Disables(test.webID); got != test.wantWebOff {
				t.Fatalf("web disabled=%v, want %v; policy=%#v", got, test.wantWebOff, policy)
			}
			if got := policy.Disables(test.codeID); got != test.wantCodeOff {
				t.Fatalf("code disabled=%v, want %v; policy=%#v", got, test.wantCodeOff, policy)
			}
		})
	}
}

func TestFactoryCreateProvider_BedrockMessagesUsesRuntimeInvokeStreamWithAPIKey(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotAuth string
	var gotAPIKey string
	var gotAMZDate string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.RequestURI()
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("x-api-key")
		gotAMZDate = r.Header.Get("x-amz-date")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(factoryBedrockEventStreamFrame(t, `{"type":"message_stop"}`))
	}))
	defer srv.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	if err := secretsStore.SetBedrockAPIKey("bedrock-key"); err != nil {
		t.Fatalf("SetBedrockAPIKey: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Provider = config.ProviderSelection{ProviderID: "bedrock", Model: "legacy-bedrock-model"}
	settings.ProviderConfigs = []config.ProviderConfig{
		{
			ID:         "cfg-bedrock-messages-api-key",
			ProviderID: "bedrock",
			Model:      "anthropic.claude-opus-4-7",
			ApiType:    strPtr("messages"),
			BaseURL:    strPtr(srv.URL + "/v1/messages"),
			AuthType:   strPtr("api_key"),
			Region:     strPtr("us-east-1"),
		},
	}
	settings.ActiveProviderConfigID = strPtr("cfg-bedrock-messages-api-key")
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProvider("")
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := streamProvider(ctx, provider)
	if err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Event != types.EventFinal {
		t.Fatalf("expected final event, got %#v", events)
	}

	wantPath := "/model/anthropic.claude-opus-4-7/invoke-with-response-stream"
	if gotPath != wantPath {
		t.Fatalf("request path = %q, want %q", gotPath, wantPath)
	}
	if gotAuth != "Bearer bedrock-key" {
		t.Fatalf("API key auth should use bearer authorization, got %q", gotAuth)
	}
	if gotAPIKey != "" || gotAMZDate != "" {
		t.Fatalf("API key auth should not send x-api-key or AWS signing headers, got x-api-key=%q x-amz-date=%q", gotAPIKey, gotAMZDate)
	}
}

func TestFactoryCreateProvider_CompatibleLegacyProfileIncludesBuiltInTools(t *testing.T) {
	t.Parallel()

	var gotTools []any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotTools, _ = body["tools"].([]any)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"rid-compatible-legacy\",\"output_text\":\"ok\"}}\n\n")
	}))
	defer srv.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	if err := secretsStore.SetCompatibleAPIKey("default", "compatible-key"); err != nil {
		t.Fatalf("SetCompatibleAPIKey: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Provider = config.ProviderSelection{
		ProviderID: "compatible",
		Model:      "cfg-model",
		ProfileID:  strPtr("default"),
	}
	settings.ProviderConfigs = nil
	settings.ActiveProviderConfigID = nil
	settings.CompatibleProfiles = []config.CompatibleProfile{
		{
			ID:          "default",
			DisplayName: "Legacy Compatible",
			ApiType:     "responses",
			BaseURL:     srv.URL + "/v1",
			Auth: config.ProviderAuth{
				Type:   "api_key",
				Header: "Authorization",
				Scheme: strPtr("Bearer"),
			},
			WebSearch: &config.ProviderWebSearchConfig{
				Enabled: boolPtr(true),
			},
			CodeExecution: &config.ProviderCodeExecutionConfig{
				Enabled:       boolPtr(true),
				ContainerMode: strPtr("auto"),
				MemoryLimit:   strPtr("4g"),
			},
		},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProvider("")
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := streamProvider(ctx, provider)
	if err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected events from provider")
	}

	if len(gotTools) != 2 {
		t.Fatalf("expected 2 built-in tools, got %#v", gotTools)
	}
	webTool, ok := gotTools[0].(map[string]any)
	if !ok || webTool["type"] != "web_search" {
		t.Fatalf("unexpected web search tool: %#v", gotTools[0])
	}
	codeTool, ok := gotTools[1].(map[string]any)
	if !ok || codeTool["type"] != "code_interpreter" {
		t.Fatalf("unexpected code execution tool: %#v", gotTools[1])
	}
}

func TestFactoryCreateProvider_CompatibleHostedToolPolicySuppressesBuiltInTools(t *testing.T) {
	t.Parallel()

	var gotTools []any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotTools, _ = body["tools"].([]any)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"rid-compatible-policy\",\"output_text\":\"ok\"}}\n\n")
	}))
	defer srv.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	if err := secretsStore.SetCompatibleAPIKey("default", "compatible-key"); err != nil {
		t.Fatalf("SetCompatibleAPIKey: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Provider = config.ProviderSelection{
		ProviderID: "compatible",
		Model:      "cfg-model",
		ProfileID:  strPtr("default"),
	}
	settings.ProviderConfigs = nil
	settings.ActiveProviderConfigID = nil
	settings.CompatibleProfiles = []config.CompatibleProfile{
		{
			ID:          "default",
			DisplayName: "Legacy Compatible",
			ApiType:     "responses",
			BaseURL:     srv.URL + "/v1",
			Auth: config.ProviderAuth{
				Type:   "api_key",
				Header: "Authorization",
				Scheme: strPtr("Bearer"),
			},
			WebSearch: &config.ProviderWebSearchConfig{
				Enabled: boolPtr(true),
			},
			CodeExecution: &config.ProviderCodeExecutionConfig{
				Enabled:       boolPtr(true),
				ContainerMode: strPtr("auto"),
			},
		},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProviderWithHostedToolPolicy("", HostedToolPolicy{
		Disabled: map[string]bool{
			"openai-responses/web_search":       true,
			"openai-responses/code_interpreter": true,
		},
	})
	if err != nil {
		t.Fatalf("CreateProviderWithHostedToolPolicy: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := streamProvider(ctx, provider)
	if err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected events from provider")
	}

	if len(gotTools) != 0 {
		t.Fatalf("hosted policy should suppress built-in tools, got %#v", gotTools)
	}
}

func TestFactoryCreateProvider_AnthropicHostedToolPolicySuppressesBuiltInTools(t *testing.T) {
	t.Parallel()

	var gotTools []any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotTools, _ = body["tools"].([]any)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_anthropic_policy\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0,\"total_tokens\":1}}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	if err := secretsStore.SetProviderKey("anthropic", "anthropic-key"); err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}

	settings := config.DefaultSettings()
	settings.Provider = config.ProviderSelection{
		ProviderID: "anthropic",
		Model:      "claude-test-model",
		ProfileID:  nil,
	}
	settings.ProviderConfigs = nil
	settings.ActiveProviderConfigID = nil
	settings.Anthropic.BaseURL = strPtr(srv.URL)
	settings.Anthropic.WebSearch = &config.ProviderWebSearchConfig{
		Enabled:  boolPtr(true),
		ToolType: strPtr("web_search_20250305"),
	}
	settings.Anthropic.CodeExecution = &config.ProviderCodeExecutionConfig{
		Enabled:  boolPtr(true),
		ToolType: strPtr("code_execution_20250522"),
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProviderWithHostedToolPolicy("", HostedToolPolicy{
		Disabled: map[string]bool{
			"anthropic-messages/web_search":     true,
			"anthropic-messages/code_execution": true,
		},
	})
	if err != nil {
		t.Fatalf("CreateProviderWithHostedToolPolicy: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	events, err := streamProvider(ctx, provider)
	if err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if len(events) == 0 {
		t.Fatalf("expected events from provider")
	}

	if len(gotTools) != 0 {
		t.Fatalf("hosted policy should suppress Anthropic built-in tools, got %#v", gotTools)
	}
}

func TestFactoryCreateProviderForConnectionDoesNotMutateGlobalTarget(t *testing.T) {
	t.Parallel()

	requestedA := 0
	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedA++
		http.Error(w, "wrong connection", http.StatusTeapot)
	}))
	defer serverA.Close()
	var gotModel string
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-target\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer serverB.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	settings := config.DefaultSettings()
	settings.ProviderConfigs = []config.ProviderConfig{
		{ID: "connection-a", DisplayName: "A", ProviderID: "compatible", Model: "model-a", ApiType: strPtr("chat_completions"), BaseURL: strPtr(serverA.URL), AuthType: strPtr("none")},
		{ID: "connection-b", DisplayName: "B", ProviderID: "compatible", Model: "model-b", ApiType: strPtr("chat_completions"), BaseURL: strPtr(serverB.URL), AuthType: strPtr("none")},
	}
	settings.ActiveProviderConfigID = strPtr("connection-a")
	settings.Provider = config.ProviderSelection{ProviderID: "compatible", Model: "model-a", ProfileID: strPtr("connection-a")}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProviderForConnectionWithHostedToolPolicy("connection-b", "run-model", HostedToolPolicy{})
	if err != nil {
		t.Fatalf("CreateProviderForConnectionWithHostedToolPolicy: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := streamProvider(ctx, provider); err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if requestedA != 0 {
		t.Fatalf("globally active connection received %d requests", requestedA)
	}
	if gotModel != "run-model" {
		t.Fatalf("request model = %q, want run-model", gotModel)
	}
	gotSettings := settingsStore.Get()
	if gotSettings.ActiveProviderConfigID == nil || *gotSettings.ActiveProviderConfigID != "connection-a" || gotSettings.Provider.Model != "model-a" {
		t.Fatalf("explicit run target mutated global settings: %#v", gotSettings)
	}
	if _, err := factory.CreateProviderForConnection("missing", ""); err == nil || !strings.Contains(err.Error(), "unknown provider connection id") {
		t.Fatalf("missing connection error = %v", err)
	}
}

func TestFactoryCapabilityGateSuppressesHostedToolsOnBedrockResponses(t *testing.T) {
	t.Parallel()

	var gotTools []any
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		gotTools, _ = body["tools"].([]any)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"rid-bedrock-capability\",\"output_text\":\"ok\"}}\n\n")
	}))
	defer providerServer.Close()

	settingsStore := newTestSettingsStore(t)
	secretsStore := newUnlockedSecretsStore(t)
	if err := secretsStore.SetBedrockAPIKey("bedrock-key"); err != nil {
		t.Fatalf("SetBedrockAPIKey: %v", err)
	}
	settings := config.DefaultSettings()
	settings.ProviderConfigs = []config.ProviderConfig{{
		ID: "bedrock-responses", DisplayName: "Bedrock Responses", ProviderID: "bedrock", Model: "openai.gpt-oss-120b",
		ApiType: strPtr("responses"), APISurface: strPtr("responses"), BaseURL: strPtr(providerServer.URL + "/v1"), AuthType: strPtr("api_key"),
		CapabilityControls: map[string]string{"hosted_web_search": "on", "hosted_code_execution": "on"},
	}}
	settings.ActiveProviderConfigID = strPtr("bedrock-responses")
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings save: %v", err)
	}

	factory := NewFactory(settingsStore, secretsStore)
	provider, err := factory.CreateProviderForConnection("bedrock-responses", "")
	if err != nil {
		t.Fatalf("CreateProviderForConnection: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := streamProvider(ctx, provider); err != nil {
		t.Fatalf("streamProvider: %v", err)
	}
	if len(gotTools) != 0 {
		t.Fatalf("Bedrock capability gate attached unsupported hosted tools: %#v", gotTools)
	}
}

func TestFactoryCapabilityGateAllowsWebSearchOnAzureOpenAIResponses(t *testing.T) {
	t.Parallel()
	enabled := true
	apiType := "responses"
	baseURL := "https://example.services.ai.azure.com/openai/v1"
	cfg := config.ProviderConfig{
		ProviderID: "compatible",
		Model:      "gpt-5.5",
		ApiType:    &apiType,
		BaseURL:    &baseURL,
		WebSearch:  &config.ProviderWebSearchConfig{Enabled: &enabled},
	}

	policy := capabilityAwareHostedToolPolicy(cfg, cfg.Model, HostedToolPolicy{})
	if policy.Disables("openai-responses/web_search") {
		t.Fatalf("Azure OpenAI Responses web search was incorrectly disabled: %#v", policy.Disabled)
	}
	if got := openAIWebSearchConfigWithPolicy(cfg.WebSearch, policy, "openai-responses"); got == nil || !got.Enabled {
		t.Fatalf("Azure OpenAI Responses web search config was stripped: %#v", got)
	}
}

func newTestSettingsStore(t *testing.T) *config.Store {
	t.Helper()

	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	return store
}

func newUnlockedSecretsStore(t *testing.T) *secrets.Store {
	t.Helper()

	store, err := secrets.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := store.Init("test-passphrase"); err != nil {
		t.Fatalf("secrets init: %v", err)
	}
	if err := store.Unlock("test-passphrase"); err != nil {
		t.Fatalf("secrets unlock: %v", err)
	}
	return store
}

func streamProvider(ctx context.Context, provider types.Provider) ([]types.StreamEvent, error) {
	req := types.ChatRequest{
		SystemPrompt: "",
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: strPtr("hello")},
		},
	}

	ch, err := provider.StreamChat(ctx, req)
	if err != nil {
		return nil, err
	}

	events := make([]types.StreamEvent, 0, 4)
	for ev := range ch {
		events = append(events, ev)
	}
	return events, nil
}

func streamProviderRequest(ctx context.Context, provider types.Provider, req types.ChatRequest) ([]types.StreamEvent, error) {
	ch, err := provider.StreamChat(ctx, req)
	if err != nil {
		return nil, err
	}

	events := make([]types.StreamEvent, 0, 4)
	for ev := range ch {
		events = append(events, ev)
	}
	return events, nil
}

func responsesInputFileFilenames(body map[string]any) []string {
	var filenames []string
	items, _ := body["input"].([]any)
	for _, item := range items {
		msg, _ := item.(map[string]any)
		content, _ := msg["content"].([]any)
		for _, rawBlock := range content {
			block, _ := rawBlock.(map[string]any)
			if typ, _ := block["type"].(string); typ != "input_file" {
				continue
			}
			filename, _ := block["filename"].(string)
			if strings.TrimSpace(filename) != "" {
				filenames = append(filenames, filename)
			}
		}
	}
	return filenames
}

func chatCompletionsInputFileFilenames(body map[string]any) []string {
	var filenames []string
	messages, _ := body["messages"].([]any)
	for _, rawMessage := range messages {
		message, _ := rawMessage.(map[string]any)
		content, _ := message["content"].([]any)
		for _, rawBlock := range content {
			block, _ := rawBlock.(map[string]any)
			if typ, _ := block["type"].(string); typ != "file" {
				continue
			}
			file, _ := block["file"].(map[string]any)
			filename, _ := file["filename"].(string)
			if strings.TrimSpace(filename) != "" {
				filenames = append(filenames, filename)
			}
		}
	}
	return filenames
}

func strPtr(s string) *string { return &s }

func boolPtr(v bool) *bool { return &v }

func TestWebSearchConfigFactoriesIgnoreStalePersistedToolSchemas(t *testing.T) {
	enabled := true
	maxUses := 99
	stale := config.ProviderWebSearchConfig{
		Enabled:           &enabled,
		ToolType:          strPtr("web_search_preview"),
		MaxUses:           &maxUses,
		SearchContextSize: strPtr("legacy"),
		AllowedDomains:    []string{"old.example"},
		BlockedDomains:    []string{"new.example"},
		AllowedCallers:    []string{"code_execution_20250522"},
	}

	openAI := openAIWebSearchConfig(&stale)
	if openAI == nil || openAI.ToolType != "web_search" || openAI.SearchContextSize != "" || len(openAI.AllowedDomains) != 0 || len(openAI.BlockedDomains) != 0 {
		t.Fatalf("OpenAI-compatible Web Search retained stale schema: %#v", openAI)
	}

	anthropicConfig := anthropicWebSearchConfig(&stale)
	if anthropicConfig == nil || anthropicConfig.ToolType != "web_search_20260209" || anthropicConfig.MaxUses == nil || *anthropicConfig.MaxUses != 5 {
		t.Fatalf("Anthropic Web Search did not use current native schema: %#v", anthropicConfig)
	}
	if len(anthropicConfig.AllowedCallers) != 1 || anthropicConfig.AllowedCallers[0] != "direct" || len(anthropicConfig.AllowedDomains) != 0 || len(anthropicConfig.BlockedDomains) != 0 {
		t.Fatalf("Anthropic Web Search retained stale schema: %#v", anthropicConfig)
	}
}
