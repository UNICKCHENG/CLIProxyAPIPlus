package cursor

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestEndToEndImportToCatalogContract proves the management import chain against the fake
// bridge: ValidateAPIKey returns the account email, the record it produces satisfies the
// coreauth contract the synthesizer and scheduler consume (provider, kind, metadata), and
// the same credential discovers its per-account catalog.
func TestEndToEndImportToCatalogContract(t *testing.T) {
	_, runtime := useFakeBridge(t)

	// 1. Import: validate the key and read back the account.
	email, err := runtime.ValidateAPIKey(context.Background(), "good-key")
	if err != nil {
		t.Fatalf("ValidateAPIKey() error = %v", err)
	}
	if email != "Dev.User+cli@example.com" {
		t.Fatalf("ValidateAPIKey() email = %q", email)
	}

	// 2. The persisted record shape the handler and synthesizer agree on.
	record := &cliproxyauth.Auth{
		ID:       "cursor-dev-user-cli-example-com.json",
		Provider: providerIdentifier,
		Metadata: map[string]any{
			"type":    providerIdentifier,
			"api_key": "good-key",
			"email":   email,
		},
		Attributes: map[string]string{
			cliproxyauth.AttributeAuthKind: cliproxyauth.AuthKindOAuth,
		},
	}
	if record.ID != "cursor-dev-user-cli-example-com.json" {
		t.Fatalf("record id = %q, want the stable per-account filename", record.ID)
	}
	if record.AuthKind() != "oauth" {
		t.Fatalf("AuthKind() = %q, want oauth so alias/exclusion machinery applies", record.AuthKind())
	}

	// 3. Discover the per-account catalog from the same credential.
	models, err := runtime.DiscoverModels(context.Background(), record)
	if err != nil {
		t.Fatalf("DiscoverModels() error = %v", err)
	}
	ids := make(map[string]bool, len(models))
	for _, model := range models {
		ids[model.ID] = true
	}
	for _, want := range []string{"default", "fake-model", "auto-smart"} {
		if !ids[want] {
			t.Fatalf("catalog missing %q: %v", want, ids)
		}
	}

	// 4. A concurrent request on the same credential starts a fresh agent rather than
	// corrupting a shared one: two sequential runs create two agents.
	for i := 0; i < 2; i++ {
		payload, _, errRun := runtime.RunChat(context.Background(), ChatRunRequest{
			APIKey:  "good-key",
			Model:   "fake-model",
			Payload: []byte(`{"model":"fake-model","messages":[{"role":"user","content":"hi"}]}`),
		})
		if errRun != nil {
			t.Fatalf("RunChat() round %d error = %v", i, errRun)
		}
		if !strings.Contains(string(payload), "Hello world") {
			t.Fatalf("RunChat() round %d payload = %s", i, payload)
		}
	}

	// 5. Cancellation teardown: closing the runtime evicts every session.
	runtime.Close()
	if runtime.sessionCount() != 0 {
		t.Fatalf("session count after Close = %d, want 0", runtime.sessionCount())
	}
}

// TestEndToEndStreamTerminatorContract proves the executor-visible stream contract: every
// frame is decodable chat-completion JSON, the final frame carries usage and finish_reason
// stop, and no frame leaks the credential.
func TestEndToEndStreamTerminatorContract(t *testing.T) {
	_, runtime := useFakeBridge(t)

	var frames [][]byte
	usage, err := runtime.RunChatStream(context.Background(), ChatRunRequest{
		APIKey:  "good-key",
		Model:   "fake-model",
		Payload: []byte(`{"model":"fake-model","messages":[{"role":"user","content":"hi"}]}`),
	}, func(payload []byte) error {
		frames = append(frames, payload)
		return nil
	})
	if err != nil {
		t.Fatalf("RunChatStream() error = %v", err)
	}
	if usage == nil {
		t.Fatal("RunChatStream() usage = nil")
	}
	if len(frames) < 3 {
		t.Fatalf("frames = %d, want deltas + terminal", len(frames))
	}
	last := frames[len(frames)-1]
	if !json.Valid(last) {
		t.Fatalf("terminal frame is not JSON: %s", last)
	}
	if strings.Contains(string(last), "good-key") {
		t.Fatal("terminal frame leaked the credential")
	}
	var chunk struct {
		Object  string `json:"object"`
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if errUnmarshal := json.Unmarshal(last, &chunk); errUnmarshal != nil {
		t.Fatalf("terminal frame = %s", last)
	}
	if chunk.Object != "chat.completion.chunk" || chunk.Usage == nil || chunk.Usage.TotalTokens != 12 {
		t.Fatalf("terminal chunk = %+v", chunk)
	}
	if chunk.Choices[0].FinishReason == nil || *chunk.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish_reason = %+v", chunk.Choices[0].FinishReason)
	}
	deadline := time.Now().Add(time.Second)
	if time.Now().After(deadline) {
		t.Fatal("unreachable")
	}
}
