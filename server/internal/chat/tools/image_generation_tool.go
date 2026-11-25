package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

const generateImageToolName = "generate_image"
const maxImageResponseBytes = 64 << 20

type imageGenerationTool struct {
	settings *config.Store
	secrets  *secrets.Store
	client   *http.Client
	mu       sync.Mutex
	perRun   map[string]int
}

func newGenerateImageTool(settings *config.Store, secretStore *secrets.Store) Tool {
	return &imageGenerationTool{
		settings: settings,
		secrets:  secretStore,
		client:   &http.Client{Timeout: 3 * time.Minute},
		perRun:   map[string]int{},
	}
}

func (t *imageGenerationTool) Definition() types.ToolDefinition {
	return types.ToolDefinition{Type: "function", Function: types.ToolFunctionDefinition{
		Name:        generateImageToolName,
		Description: "Generate a real raster image using the configured image-generation model. Use this when the user asks to create, render, draw, illustrate, or generate an image. The result is displayed inline and saved as a reusable file artifact. Do not substitute HTML, SVG, charts, or interactive artifacts when this tool is available.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt":  map[string]any{"type": "string", "description": "A detailed description of the image to generate."},
				"title":   map[string]any{"type": "string", "description": "Optional short file title for the generated image."},
				"size":    map[string]any{"type": "string", "description": "Optional output size. Use auto unless the user requests a particular composition."},
				"quality": map[string]any{"type": "string", "enum": []string{"auto", "low", "medium", "high"}, "description": "Optional generation quality."},
				"format":  map[string]any{"type": "string", "enum": []string{"png", "jpeg", "webp"}, "description": "Optional output file format."},
				"dry_run": map[string]any{"type": "boolean", "description": "Validate configuration without sending a billable image request. Use only for Settings tests."},
			},
			"required": []string{"prompt"}, "additionalProperties": false,
		},
	}}
}

func (t *imageGenerationTool) Execute(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
	if t == nil || t.settings == nil {
		return imageGenerationFailure("not_configured", "Image generation is not configured for this app instance.", ""), nil
	}
	var in struct {
		Prompt  string `json:"prompt"`
		Title   string `json:"title"`
		Size    string `json:"size"`
		Quality string `json:"quality"`
		Format  string `json:"format"`
		DryRun  bool   `json:"dry_run"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return imageGenerationFailure("invalid_input", "Invalid image-generation arguments.", ""), nil
	}
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" || len(in.Prompt) > 32000 {
		return imageGenerationFailure("invalid_input", "Prompt is required and must be no longer than 32,000 characters.", ""), nil
	}

	app := t.settings.Get()
	imageCfg := app.ImageGeneration
	if imageCfg == nil || imageCfg.Enabled == nil || !*imageCfg.Enabled {
		return imageGenerationFailure("not_configured", "Image generation is disabled. Enable the Image Generation connection in Settings > Connections.", ""), nil
	}
	if t.secrets == nil {
		return imageGenerationFailure("not_configured", "Image generation credentials are not configured for this app instance.", ""), nil
	}
	providerCfg, ok := selectedImageProvider(app, imageCfg)
	if !ok || providerCfg.BaseURL == nil || strings.TrimSpace(*providerCfg.BaseURL) == "" {
		return imageGenerationFailure("not_configured", "The selected image-generation connection is not configured.", ""), nil
	}
	model := valueOr(imageCfg.Model, "gpt-image-2")
	if model == "" {
		return imageGenerationFailure("not_configured", "An image-generation model is required in Settings.", ""), nil
	}
	defaults := imageCfg.Defaults
	size := strings.TrimSpace(in.Size)
	quality := strings.TrimSpace(in.Quality)
	format := strings.TrimSpace(in.Format)
	if defaults != nil {
		if size == "" {
			size = valueOr(defaults.Size, "auto")
		}
		if quality == "" {
			quality = valueOr(defaults.Quality, "auto")
		}
		if format == "" {
			format = valueOr(defaults.Format, "png")
		}
	}
	if size == "" {
		size = "auto"
	}
	if quality == "" {
		quality = "auto"
	}
	if format == "" {
		format = "png"
	}
	if !oneOf(quality, "auto", "low", "medium", "high") || !oneOf(format, "png", "jpeg", "webp") {
		return imageGenerationFailure("invalid_input", "Unsupported image quality or format.", ""), nil
	}

	apiKey, secretHeaders, err := t.credentials(providerCfg)
	if err != nil || (strings.TrimSpace(apiKey) == "" && len(secretHeaders) == 0) {
		return imageGenerationFailure("auth", "The selected image connection has no available credential. Re-save its API key in Settings.", ""), nil
	}
	endpoint, err := imageGenerationEndpoint(*providerCfg.BaseURL, providerCfg.QueryParams)
	if err != nil {
		return imageGenerationFailure("not_configured", "The selected image connection has an invalid base URL.", ""), nil
	}
	if in.DryRun || exec.RunID == "tool_test" {
		return Output{OK: true, ResultFormat: ResultFormatJSON, ResultMetadata: map[string]any{"operation": "image_generation", "dry_run": true, "provider_id": providerCfg.ProviderID, "connection_id": providerCfg.ID, "model": model}, Result: map[string]any{"status": "ready", "dry_run": true, "message": "Image generation connection and credential are configured; no billable request was sent."}}, nil
	}

	maxPerRun := 1
	if imageCfg.MaxImagesPerRun != nil && *imageCfg.MaxImagesPerRun > 0 {
		maxPerRun = *imageCfg.MaxImagesPerRun
	}
	if !t.reserve(exec.RunID, maxPerRun) {
		return imageGenerationFailure("limit_reached", fmt.Sprintf("This run is limited to %d generated image(s).", maxPerRun), ""), nil
	}
	succeeded := false
	defer func() {
		if !succeeded {
			t.release(exec.RunID)
		}
	}()
	usageTracker, err := beginImageUsage(ctx, exec, providerCfg, model)
	if err != nil {
		return imageGenerationFailure("accounting_unavailable", "Token accounting is temporarily unavailable, so no billable image request was sent.", ""), nil
	}
	defer usageTracker.finish(ctx)

	emitImageProgress(exec, "requesting", 10, "Sending prompt to the image model")
	payload := map[string]any{"model": model, "prompt": in.Prompt, "n": 1, "size": size, "quality": quality, "output_format": format}
	started := time.Now()
	var response *http.Response
	var responseBody []byte
	for attempt := 0; attempt < 2; attempt++ {
		if err := usageTracker.dispatch(ctx); err != nil {
			usageTracker.fail("PERSISTENCE_ERROR")
			return imageGenerationFailure("accounting_unavailable", "Token accounting is temporarily unavailable, so no billable image request was sent.", ""), nil
		}
		response, responseBody, err = t.request(ctx, endpoint, providerCfg, apiKey, secretHeaders, payload, usageTracker.invocationID())
		if err != nil {
			break
		}
		// A transport failure or 5xx can arrive after generation and billing, so
		// automatically repeating it could create a second paid image. Only a
		// rate-limit response with no usage evidence is safe to retry here; manual
		// retries become their own fully tracked invocation.
		if response.StatusCode != http.StatusTooManyRequests || attempt > 0 || len(imageResponseUsage(responseBody)) > 0 {
			break
		}
		if attempt == 0 {
			emitImageProgress(exec, "retrying", 25, "The image service is busy; retrying")
			timer := time.NewTimer(750 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					usageTracker.fail("TIMEOUT")
					return imageGenerationFailure("timeout", "Image generation timed out. Try again.", ""), nil
				}
				usageTracker.cancel()
				return imageGenerationFailure("cancelled", "Image generation was cancelled.", ""), nil
			case <-timer.C:
			}
		}
	}
	latency := time.Since(started).Milliseconds()
	if err != nil {
		category := "provider_unavailable"
		message := "The image service could not be reached. Try again."
		if errors.Is(err, context.Canceled) {
			category, message = "cancelled", "Image generation was cancelled."
			usageTracker.cancel()
		} else if errors.Is(err, context.DeadlineExceeded) {
			category, message = "timeout", "Image generation timed out. Try again."
			usageTracker.fail("TIMEOUT")
		} else {
			usageTracker.fail(category)
		}
		return imageGenerationFailure(category, message, ""), nil
	}
	requestID := firstNonEmptyImage(response.Header.Get("x-request-id"), response.Header.Get("apim-request-id"))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if rawUsage := imageResponseUsage(responseBody); len(rawUsage) > 0 {
			if observeErr := usageTracker.observe(rawUsage, requestID, false); observeErr != nil {
				log.Printf("usage: image generation error response snapshot failed id=%s code=ACCOUNTING_SNAPSHOT_FAILED", usageTracker.invocationID())
			}
		}
		failure := imageHTTPFailure(response.StatusCode, responseBody, requestID)
		usageTracker.fail(imageUsageString(failure.ResultMetadata["error_category"]))
		return failure, nil
	}
	emitImageProgress(exec, "generating", 65, "Rendering image")

	var decoded struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			RevisedPrompt string `json:"revised_prompt"`
			URL           string `json:"url"`
		} `json:"data"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		usageTracker.fail("INVALID_RESPONSE")
		return imageGenerationFailure("invalid_response", "The image service returned an invalid response.", requestID), nil
	}
	if err := usageTracker.observe(decoded.Usage, requestID, len(decoded.Data) > 0); err != nil {
		// The request has already been billed. Keep processing so the artifact's
		// retained raw usage offers a second recovery path, while deferred
		// finalization makes one more durable accounting attempt.
		log.Printf("usage: image generation response snapshot failed id=%s code=ACCOUNTING_SNAPSHOT_FAILED", usageTracker.invocationID())
	}
	if len(decoded.Data) == 0 || strings.TrimSpace(decoded.Data[0].B64JSON) == "" {
		usageTracker.fail("INVALID_RESPONSE")
		return imageGenerationFailure("invalid_response", "The image service returned no image data.", requestID), nil
	}
	imageBytes, err := base64.StdEncoding.DecodeString(decoded.Data[0].B64JSON)
	if err != nil || len(imageBytes) == 0 {
		usageTracker.fail("INVALID_RESPONSE")
		return imageGenerationFailure("invalid_response", "The image service returned invalid image data.", requestID), nil
	}
	mimeType := http.DetectContentType(imageBytes)
	if mimeType == "image/jpg" {
		mimeType = "image/jpeg"
	}
	if !strings.HasPrefix(mimeType, "image/") {
		usageTracker.fail("INVALID_RESPONSE")
		return imageGenerationFailure("invalid_response", "The image service returned data that is not an image.", requestID), nil
	}
	width, height := 0, 0
	if cfg, _, decodeErr := image.DecodeConfig(bytes.NewReader(imageBytes)); decodeErr == nil {
		width, height = cfg.Width, cfg.Height
	}
	emitImageProgress(exec, "saving", 90, "Saving generated image to Files")

	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = imageTitle(in.Prompt)
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)
	metadata := map[string]any{
		"source": "generated_image", "generated": true, "tool_name": generateImageToolName,
		"provider": providerCfg.DisplayName, "provider_id": providerCfg.ProviderID, "connection_id": providerCfg.ID, "model": model,
		"prompt": in.Prompt, "revised_prompt": decoded.Data[0].RevisedPrompt,
		"mime_type": mimeType, "mimeType": mimeType, "width": width, "height": height,
		"size_bytes": len(imageBytes), "sizeBytes": len(imageBytes), "quality": quality,
		"format": format, "created_at": createdAt, "createdAt": createdAt,
		"request_id": requestID, "run_id": exec.RunID, "usage_invocation_id": usageTracker.invocationID(),
	}
	resultMetadata := map[string]any{
		"operation": "image_generation", "provider": providerCfg.DisplayName, "provider_id": providerCfg.ProviderID, "connection_id": providerCfg.ID,
		"model": model, "request_id": requestID, "latency_ms": latency, "usage": decoded.Usage,
		"usage_invocation_id": usageTracker.invocationID(),
		"mime_type":           mimeType, "width": width, "height": height, "size_bytes": len(imageBytes),
	}
	language := format
	succeeded = true
	usageTracker.succeed()
	emitImageProgress(exec, "completed", 100, "Image ready")
	return Output{
		OK: true, ResultFormat: ResultFormatJSON, ResultMetadata: resultMetadata,
		Result:   map[string]any{"status": "completed", "title": title, "artifact_type": "image", "mime_type": mimeType, "width": width, "height": height, "size_bytes": len(imageBytes), "revised_prompt": decoded.Data[0].RevisedPrompt},
		Artifact: &ArtifactPayload{Type: "image", Title: title, Language: &language, Content: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(imageBytes), Metadata: metadata},
	}, nil
}

func (t *imageGenerationTool) credentials(provider config.ProviderConfig) (string, map[string]string, error) {
	if provider.ProviderID == "compatible" || provider.ID != "" {
		secret, err := t.secrets.GetCompatible(provider.ID)
		if err == nil {
			return secret.APIKey, secret.SecretHeaders, nil
		}
		if provider.ProviderID == "compatible" {
			return "", nil, err
		}
	}
	key, err := t.secrets.GetProviderKey(provider.ProviderID)
	return key, nil, err
}

func (t *imageGenerationTool) request(ctx context.Context, endpoint string, provider config.ProviderConfig, apiKey string, secretHeaders map[string]string, payload map[string]any, idempotencyKey string) (*http.Response, []byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey = strings.TrimSpace(idempotencyKey); idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	header := valueOr(provider.AuthHeader, "Authorization")
	scheme := valueOr(provider.AuthScheme, "Bearer")
	if apiKey != "" {
		if scheme != "" {
			req.Header.Set(header, scheme+" "+apiKey)
		} else {
			req.Header.Set(header, apiKey)
		}
	}
	for _, h := range provider.ExtraHeaders {
		if strings.TrimSpace(h.Key) != "" {
			req.Header.Set(h.Key, h.Value)
		}
	}
	for key, value := range secretHeaders {
		if strings.TrimSpace(key) != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, readErr := io.ReadAll(io.LimitReader(resp.Body, maxImageResponseBytes+1))
	if readErr != nil {
		return resp, nil, readErr
	}
	if len(b) > maxImageResponseBytes {
		return resp, nil, errors.New("image response exceeds size limit")
	}
	return resp, b, nil
}

func imageResponseUsage(body []byte) map[string]any {
	if len(body) == 0 {
		return nil
	}
	var envelope struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil
	}
	return envelope.Usage
}

func selectedImageProvider(app config.AppSettings, imageCfg *config.ImageGenerationSettings) (config.ProviderConfig, bool) {
	id := valueOr(imageCfg.ProviderID, "")
	if id == "" {
		return config.ProviderConfig{}, false
	}
	for _, provider := range app.ProviderConfigs {
		if strings.TrimSpace(provider.ID) == id {
			return provider, true
		}
	}
	return config.ProviderConfig{}, false
}

func imageGenerationEndpoint(base string, query map[string]string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/") + "/images/generations")
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid base URL")
	}
	q := u.Query()
	for key, value := range query {
		q.Set(key, value)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func imageHTTPFailure(status int, body []byte, requestID string) Output {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	code := strings.ToLower(strings.TrimSpace(envelope.Error.Code))
	category, message := "provider_error", "The image service rejected the request."
	switch {
	case status == 401 || status == 403:
		category, message = "auth", "The image connection was not authorized. Check its stored credential."
	case status == 429:
		category, message = "rate_limited", "The image service is rate limited. Try again shortly."
	case strings.Contains(code, "moderation") || strings.Contains(code, "safety"):
		category, message = "moderation_blocked", "The image request was blocked by the provider's safety checks. Try revising the prompt."
	case status >= 500:
		category, message = "provider_unavailable", "The image service is temporarily unavailable. Try again."
	case status == 400:
		category, message = "invalid_input", "The image service could not use that prompt or output setting."
	}
	return imageGenerationFailure(category, message, requestID)
}

func imageGenerationFailure(category, message, requestID string) Output {
	return Output{OK: false, ResultFormat: ResultFormatJSON, Result: map[string]any{"status": "failed", "error": message, "error_category": category}, ResultMetadata: map[string]any{"operation": "image_generation", "error_category": category, "request_id": requestID}}
}

func emitImageProgress(exec ExecContext, phase string, percent int, label string) {
	if exec.OnProgress != nil {
		exec.OnProgress(map[string]any{"phase": phase, "percent": percent, "label": label})
	}
}

func (t *imageGenerationTool) reserve(runID string, max int) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.perRun[runID] >= max {
		return false
	}
	t.perRun[runID]++
	return true
}
func (t *imageGenerationTool) release(runID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.perRun[runID] <= 1 {
		delete(t.perRun, runID)
	} else {
		t.perRun[runID]--
	}
}
func valueOr(value *string, fallback string) string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return fallback
	}
	return strings.TrimSpace(*value)
}
func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}
func firstNonEmptyImage(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
func imageTitle(prompt string) string {
	words := strings.Fields(prompt)
	if len(words) > 8 {
		words = words[:8]
	}
	title := strings.Trim(strings.Join(words, " "), " .,:;!?\"'")
	if title == "" {
		return "Generated image"
	}
	runes := []rune(title)
	if len(runes) > 80 {
		title = string(runes[:80])
	}
	return title
}
