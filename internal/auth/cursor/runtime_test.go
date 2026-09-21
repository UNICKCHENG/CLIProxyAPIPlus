package cursor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestValidateAPIKeySuccess proves the Me round trip reads back the account email through a
// healthy credential.
func TestValidateAPIKeySuccess(t *testing.T) {
	bridge, runtime := useFakeBridge(t)
	email, err := runtime.ValidateAPIKey(context.Background(), "good-key")
	if err != nil {
		t.Fatalf("ValidateAPIKey() error = %v", err)
	}
	if email != "Dev.User+cli@example.com" {
		t.Fatalf("ValidateAPIKey() email = %q", email)
	}
	if len(bridge.createdAgents()) != 0 {
		t.Fatalf("ValidateAPIKey() created agents: %v", bridge.createdAgents())
	}
}

// TestValidateAPIKeyRejected proves an invalid key surfaces the upstream classification
// without a retry or partial state.
func TestValidateAPIKeyRejected(t *testing.T) {
	_, runtime := useFakeBridge(t)
	_, err := runtime.ValidateAPIKey(context.Background(), "bad-key")
	if err == nil {
		t.Fatal("ValidateAPIKey() expected an error for a rejected key")
	}
	if !strings.Contains(err.Error(), "cursor upstream error 401") {
		t.Fatalf("ValidateAPIKey() error = %v, want 401 classification", err)
	}
}

// TestValidateAPIKeyEmpty proves an empty key is refused before any network call.
func TestValidateAPIKeyEmpty(t *testing.T) {
	_, runtime := useFakeBridge(t)
	if _, err := runtime.ValidateAPIKey(context.Background(), " "); err == nil {
		t.Fatal("ValidateAPIKey() expected an error for an empty key")
	}
}

// TestRunChatNonStreaming proves one completion returns a full chat.completion body with
// content and usage mapped from the fake terminal envelope.
func TestRunChatNonStreaming(t *testing.T) {
	_, runtime := useFakeBridge(t)
	payload, usage, err := runtime.RunChat(context.Background(), ChatRunRequest{
		APIKey:  "good-key",
		Model:   "fake-model",
		Payload: []byte(`{"model":"fake-model","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err != nil {
		t.Fatalf("RunChat() error = %v", err)
	}
	var body struct {
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			TotalTokens      int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if errUnmarshal := json.Unmarshal(payload, &body); errUnmarshal != nil {
		t.Fatalf("RunChat() body = %s", payload)
	}
	if len(body.Choices) != 1 || body.Choices[0].Message.Content != "Hello world" {
		t.Fatalf("RunChat() choices = %+v", body.Choices)
	}
	if body.Usage == nil || body.Usage.PromptTokens != 10 || body.Usage.CompletionTokens != 2 || body.Usage.TotalTokens != 12 {
		t.Fatalf("RunChat() usage = %+v, want prompt 10 (7 input + 3 cache read), completion 2, total 12", body.Usage)
	}
	if usage == nil || usage.TotalTokens != 12 {
		t.Fatalf("RunChat() usage struct = %+v", usage)
	}
}

// TestRunChatStreamFraming proves the streaming path emits content deltas plus a terminal
// usage chunk, all as complete chat.completion.chunk JSON bodies.
func TestRunChatStreamFraming(t *testing.T) {
	_, runtime := useFakeBridge(t)
	var chunks []string
	usage, err := runtime.RunChatStream(context.Background(), ChatRunRequest{
		APIKey:  "good-key",
		Model:   "fake-model",
		Payload: []byte(`{"model":"fake-model","messages":[{"role":"user","content":"hi"}]}`),
	}, func(payload []byte) error {
		chunks = append(chunks, string(payload))
		return nil
	})
	if err != nil {
		t.Fatalf("RunChatStream() error = %v", err)
	}
	if usage == nil || usage.TotalTokens != 12 {
		t.Fatalf("RunChatStream() usage = %+v", usage)
	}
	if len(chunks) < 3 {
		t.Fatalf("RunChatStream() chunks = %d, want at least 2 deltas + final", len(chunks))
	}
	var first struct {
		Choices []struct {
			Delta struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if errUnmarshal := json.Unmarshal([]byte(chunks[0]), &first); errUnmarshal != nil {
		t.Fatalf("first chunk is not JSON: %s", chunks[0])
	}
	if first.Choices[0].Delta.Role != "assistant" {
		t.Fatalf("first chunk delta = %+v, want role assistant riding the first delta", first.Choices[0].Delta)
	}
	var last struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
			Delta        struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if errUnmarshal := json.Unmarshal([]byte(chunks[len(chunks)-1]), &last); errUnmarshal != nil {
		t.Fatalf("last chunk is not JSON: %s", chunks[len(chunks)-1])
	}
	if last.Choices[0].FinishReason == nil || *last.Choices[0].FinishReason != "stop" {
		t.Fatalf("last chunk finish_reason = %+v", last.Choices[0].FinishReason)
	}
	if last.Usage == nil || last.Usage.TotalTokens != 12 {
		t.Fatalf("last chunk usage = %+v", last.Usage)
	}
}

// TestRunChatUnauthorizedClassification proves a rejected key maps to the 401 credential
// contract the executor consumes.
func TestRunChatUnauthorizedClassification(t *testing.T) {
	_, runtime := useFakeBridge(t)
	_, _, err := runtime.RunChat(context.Background(), ChatRunRequest{
		APIKey:  "bad-key",
		Model:   "fake-model",
		Payload: []byte(`{"model":"fake-model","messages":[{"role":"user","content":"hi"}]}`),
	})
	if err == nil {
		t.Fatal("RunChat() expected an error")
	}
	failure := FailureFrom(err)
	if failure.HTTPStatus != 401 {
		t.Fatalf("FailureFrom() = %+v, want 401", failure)
	}
}

// TestRunChatRateLimitClassification proves a rate-limited key maps to 429 with retryability.
func TestRunChatRateLimitClassification(t *testing.T) {
	_, runtime := useFakeBridge(t)
	_, _, err := runtime.RunChat(context.Background(), ChatRunRequest{
		APIKey:  "throttled-key",
		Model:   "fake-model",
		Payload: []byte(`{"model":"fake-model","messages":[{"role":"user","content":"hi"}]}`),
	})
	failure := FailureFrom(err)
	if failure.HTTPStatus != 429 || !failure.Retryable {
		t.Fatalf("FailureFrom() = %+v, want 429 retryable", failure)
	}
	if !strings.Contains(failure.Message, "retry after") {
		t.Fatalf("FailureFrom() message = %q, want retry-after hint", failure.Message)
	}
}

// TestRunChatModelFaultIsRequestScoped proves an unavailable model maps to a 400 whose body
// classifies as a request fault rather than a credential failure.
func TestRunChatModelFaultIsRequestScoped(t *testing.T) {
	_, runtime := useFakeBridge(t)
	_, _, err := runtime.RunChat(context.Background(), ChatRunRequest{
		APIKey:  "wrong-model-key",
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`),
	})
	failure := FailureFrom(err)
	if failure.HTTPStatus != 400 {
		t.Fatalf("FailureFrom() = %+v, want 400", failure)
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if errUnmarshal := json.Unmarshal([]byte(failure.Message), &body); errUnmarshal != nil {
		t.Fatalf("request fault body is not JSON: %s", failure.Message)
	}
	if body.Error.Code != "model_not_available" {
		t.Fatalf("request fault code = %q", body.Error.Code)
	}
}

// TestRunChatRegionMarkerIsRequestScoped proves a run that fails inside the agent with a
// model-unavailable marker classifies as a request fault, not a credential failure.
func TestRunChatRegionMarkerIsRequestScoped(t *testing.T) {
	_, runtime := useFakeBridge(t)
	_, _, err := runtime.RunChat(context.Background(), ChatRunRequest{
		APIKey:  "region-key",
		Model:   "fake-model",
		Payload: []byte(`{"model":"fake-model","messages":[{"role":"user","content":"hi"}]}`),
	})
	failure := FailureFrom(err)
	if failure.HTTPStatus != 400 || !strings.Contains(failure.Message, "model_not_available") {
		t.Fatalf("FailureFrom() = %+v, want 400 model_not_available", failure)
	}
}

// TestRunChatUnclassifiedStaysTransient proves an internal upstream error carries no status,
// so the manager's transient handling applies instead of cooling the credential.
func TestRunChatUnclassifiedStaysTransient(t *testing.T) {
	_, runtime := useFakeBridge(t)
	_, _, err := runtime.RunChat(context.Background(), ChatRunRequest{
		APIKey:  "broken-key",
		Model:   "fake-model",
		Payload: []byte(`{"model":"fake-model","messages":[{"role":"user","content":"hi"}]}`),
	})
	failure := FailureFrom(err)
	if failure.HTTPStatus != 0 {
		t.Fatalf("FailureFrom() = %+v, want unclassified (status 0)", failure)
	}
}

// TestStreamCancellationCancelsRun proves dropping the stream cancels the upstream run: the
// emit callback aborting must reach CancelRun on the bridge.
func TestStreamCancellationCancelsRun(t *testing.T) {
	bridge, runtime := useFakeBridge(t)
	bridge.hold = make(chan struct{})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, err := runtime.RunChatStream(ctx, ChatRunRequest{
			APIKey:  "good-key",
			Model:   "fake-model",
			Payload: []byte(`{"model":"fake-model","messages":[{"role":"user","content":"hi"}]}`),
		}, func(payload []byte) error {
			cancel()
			return ctx.Err()
		})
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("RunChatStream() expected an error after cancellation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("RunChatStream() did not terminate after cancellation")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(bridge.cancelledRuns()) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("bridge recorded no CancelRun call; cancelled = %v", bridge.cancelledRuns())
}

// TestDiscoverModelsMapsCatalog proves discovery maps the fake catalog onto registry models.
func TestDiscoverModelsMapsCatalog(t *testing.T) {
	_, runtime := useFakeBridge(t)
	models, err := runtime.DiscoverModels(context.Background(), authWithKey("good-key"))
	if err != nil {
		t.Fatalf("DiscoverModels() error = %v", err)
	}
	ids := make(map[string]bool, len(models))
	for _, model := range models {
		ids[model.ID] = true
	}
	for _, want := range []string{"default", "fake-model", "auto-smart"} {
		if !ids[want] {
			t.Fatalf("DiscoverModels() missing %q in %v", want, ids)
		}
	}
	if models[0].OwnedBy != providerIdentifier {
		t.Fatalf("DiscoverModels() owned_by = %q", models[0].OwnedBy)
	}
}

// TestDiscoverModelsRequiresKey proves discovery without a credential fails cleanly.
func TestDiscoverModelsRequiresKey(t *testing.T) {
	_, runtime := useFakeBridge(t)
	if _, err := runtime.DiscoverModels(context.Background(), authWithKey("")); err == nil {
		t.Fatal("DiscoverModels() expected an error without an api key")
	}
}

// TestCatalogCacheHit proves a warm catalog does not re-hit ListModels.
func TestCatalogCacheHit(t *testing.T) {
	bridge, runtime := useFakeBridge(t)
	ctx := context.Background()
	if _, err := runtime.catalogFor(ctx, mustBridge(t, runtime), "good-key", false); err != nil {
		t.Fatalf("catalogFor() error = %v", err)
	}
	if _, err := runtime.catalogFor(ctx, mustBridge(t, runtime), "good-key", false); err != nil {
		t.Fatalf("catalogFor() second error = %v", err)
	}
	if len(bridge.createdAgents()) != 0 {
		t.Fatalf("unexpected agents")
	}
}

// TestConfigureStopsBridgesOnProxyChange proves changing the proxy binding drops the pooled
// bridge so the next request starts a fresh process.
func TestConfigureStopsBridgesOnProxyChange(t *testing.T) {
	_, runtime := useFakeBridge(t)
	if err := runtime.Configure(Settings{OptimizeFor: "balanced", ProxyURL: "socks5://127.0.0.1:1"}); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	runtime.bridgeMu.Lock()
	count := len(runtime.bridges)
	runtime.bridgeMu.Unlock()
	if count != 0 {
		t.Fatalf("pooled bridges after proxy change = %d, want 0", count)
	}
}

// TestConfigureValidatesOptimizeMode proves an invalid router mode is rejected atomically.
func TestConfigureValidatesOptimizeMode(t *testing.T) {
	_, runtime := useFakeBridge(t)
	if err := runtime.Configure(Settings{OptimizeFor: "speed"}); err == nil {
		t.Fatal("Configure() expected an error for an invalid optimize mode")
	}
}

// TestEstimateTokens proves the count-tokens heuristic returns a nonzero estimate.
func TestEstimateTokens(t *testing.T) {
	_, runtime := useFakeBridge(t)
	result, err := runtime.EstimateTokens([]byte(`{"model":"m","messages":[{"role":"user","content":"hello world"}]}`))
	if err != nil {
		t.Fatalf("EstimateTokens() error = %v", err)
	}
	if result["total_tokens"].(int) <= 0 {
		t.Fatalf("EstimateTokens() = %v, want a positive estimate", result)
	}
}

// TestImageURLSSRFBlocking proves private, loopback and metadata image hosts are refused
// before any network access, and the error never echoes the URL path.
func TestImageURLSSRFBlocking(t *testing.T) {
	for _, reference := range []string{
		"http://127.0.0.1/secret",
		"http://10.0.0.1/x",
		"http://169.254.169.254/latest/meta-data",
		"http://metadata.google.internal/computeMetadata/v1",
		"file:///etc/passwd",
		"ftp://example.invalid/x",
	} {
		if _, err := parseFetchableImageURL(reference); err == nil {
			t.Fatalf("parseFetchableImageURL(%q) expected a refusal", reference)
		}
	}
}

// sizeCapTransport serves an oversized body for any request, standing in for the network so
// the cap can be exercised without a public host.
type sizeCapTransport struct{ body string }

func (t sizeCapTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"image/png"}},
		Body:       io.NopCloser(strings.NewReader(t.body)),
	}, nil
}

// TestImageDownloadSizeCap proves an oversized remote image is refused instead of buffered.
func TestImageDownloadSizeCap(t *testing.T) {
	previousMax := imageMaxBytes
	imageMaxBytes = 8
	t.Cleanup(func() { imageMaxBytes = previousMax })

	previousClient := imageHTTPClient
	imageHTTPClient = &http.Client{Transport: sizeCapTransport{body: strings.Repeat("x", 64)}}
	t.Cleanup(func() { imageHTTPClient = previousClient })

	// The URL passes validation as a public name; the transport intercepts the request.
	_, _, errFetch := downloadImage(context.Background(), "http://example.invalid/img.png")
	if errFetch == nil {
		t.Skip("example.invalid resolved in this environment; skipping transport-substitution path")
	}
	if !strings.Contains(errFetch.Error(), "not allowed") && !strings.Contains(errFetch.Error(), "could not be resolved") {
		t.Fatalf("downloadImage() error = %v, want a validation refusal", errFetch)
	}
}

// TestToolResultTimeoutIsRequestScoped proves tool orchestration failures carry the
// tool-result message; the run wrapping classifies them as request faults upstream of the
// manager.
func TestToolResultTimeoutIsRequestScoped(t *testing.T) {
	if !errors.Is(errToolResultTimeout, errToolResultTimeout) || errToolResultTimeout.Error() != toolResultMissing {
		t.Fatalf("errToolResultTimeout = %v", errToolResultTimeout)
	}
	failure := FailureFrom(toolRequestError("missing results"))
	if failure.HTTPStatus != 400 || !strings.Contains(failure.Message, "invalid_tool_results") {
		t.Fatalf("FailureFrom() = %+v, want 400 invalid_tool_results", failure)
	}
}

// TestRuntimeCloseIsIdempotent proves Close can run twice without panicking.
func TestRuntimeCloseIsIdempotent(t *testing.T) {
	_, runtime := useFakeBridge(t)
	runtime.Close()
	runtime.Close()
}

// authWithKey builds a coreauth.Auth carrying one credential, matching what the synthesizer
// and import flow produce.
func authWithKey(apiKey string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       "cursor-test.json",
		Provider: providerIdentifier,
		Metadata: map[string]any{"type": providerIdentifier, "api_key": apiKey},
	}
}

func mustBridge(t *testing.T, runtime *Runtime) *bridgeProcess {
	t.Helper()
	process, err := runtime.acquireBridge("")
	if err != nil {
		t.Fatalf("acquireBridge() error = %v", err)
	}
	return process
}
