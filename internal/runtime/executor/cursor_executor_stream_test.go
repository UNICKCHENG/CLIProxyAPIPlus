package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	sdkv1 "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/sdk/v1"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/sdk/v1/sdkv1connect"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// stubCursorBridge implements just enough of the sdk.v1 agent service for the executor's
// chat paths: CreateAgent, Send (streaming deltas + terminal result with usage) and Ping.
type stubCursorBridge struct {
	sdkv1connect.UnimplementedSdkAgentServiceHandler
	sdkv1connect.UnimplementedSdkCursorServiceHandler
	sdkv1connect.UnimplementedSdkBridgeControlServiceHandler
}

func (b *stubCursorBridge) Ping(context.Context, *connect.Request[sdkv1.PingRequest]) (*connect.Response[sdkv1.PingResponse], error) {
	return connect.NewResponse(&sdkv1.PingResponse{}), nil
}

func (b *stubCursorBridge) SetToolCallback(_ context.Context, _ *connect.Request[sdkv1.SetToolCallbackRequest]) (*connect.Response[sdkv1.SetToolCallbackResponse], error) {
	return connect.NewResponse(&sdkv1.SetToolCallbackResponse{}), nil
}

func (b *stubCursorBridge) ListModels(context.Context, *connect.Request[sdkv1.ListModelsRequest]) (*connect.Response[sdkv1.ListModelsResponse], error) {
	return connect.NewResponse(&sdkv1.ListModelsResponse{}), nil
}

func (b *stubCursorBridge) CreateAgent(context.Context, *connect.Request[sdkv1.CreateAgentRequest]) (*connect.Response[sdkv1.CreateAgentResponse], error) {
	return connect.NewResponse(&sdkv1.CreateAgentResponse{AgentId: "agent-1"}), nil
}

func (b *stubCursorBridge) DeleteAgent(context.Context, *connect.Request[sdkv1.DeleteAgentRequest]) (*connect.Response[sdkv1.DeleteAgentResponse], error) {
	return connect.NewResponse(&sdkv1.DeleteAgentResponse{}), nil
}

func (b *stubCursorBridge) Send(_ context.Context, _ *connect.Request[sdkv1.SendRequest], stream *connect.ServerStream[sdkv1.RunStreamMessage]) error {
	// Two text deltas then a terminal result with usage, mirroring the fake bridge's
	// healthy run shape: prompt 10 (7 input + 3 cache read), completion 2, total 12.
	for _, delta := range []string{"Hello", " world"} {
		if errSend := stream.Send(&sdkv1.RunStreamMessage{
			Envelope: &sdkv1.RunStreamMessage_InteractionUpdate{InteractionUpdate: &sdkv1.InteractionUpdate{
				Type:   "text-delta",
				Update: mustCursorStruct(map[string]any{"type": "text-delta", "text": delta}),
			}},
		}); errSend != nil {
			return errSend
		}
	}
	if errSend := stream.Send(&sdkv1.RunStreamMessage{
		Envelope: &sdkv1.RunStreamMessage_Result{Result: &sdkv1.RunStreamResult{
			AgentId: "agent-1",
			RunId:   "run-1",
			Status:  sdkv1.RunLifecycleStatus_RUN_LIFECYCLE_STATUS_FINISHED,
			Result: &sdkv1.RunResult{
				RunId:  "run-1",
				Result: "Hello world",
				Usage: &sdkv1.TokenUsage{
					InputTokens:     7,
					OutputTokens:    2,
					CacheReadTokens: 3,
					TotalTokens:     12,
				},
			},
		}},
	}); errSend != nil {
		return errSend
	}
	return stream.Send(&sdkv1.RunStreamMessage{
		Envelope: &sdkv1.RunStreamMessage_Done{Done: &sdkv1.RunStreamDone{AgentId: "agent-1", RunId: "run-1"}},
	})
}

func mustCursorStruct(fields map[string]any) *structpb.Struct {
	value, errStruct := structpb.NewStruct(fields)
	if errStruct != nil {
		panic(errStruct)
	}
	return value
}

// serveStubCursorBridge starts the stub service and returns its base URL.
func serveStubCursorBridge(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(sdkv1connect.NewSdkAgentServiceHandler(&stubCursorBridge{}))
	mux.Handle(sdkv1connect.NewSdkCursorServiceHandler(&stubCursorBridge{}))
	mux.Handle(sdkv1connect.NewSdkBridgeControlServiceHandler(&stubCursorBridge{}))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server.URL
}

// newCursorExecutorWithStub builds a CursorExecutor whose runtime is bound to the stub
// bridge, plus a usage recorder capturing what the executor publishes.
func newCursorExecutorWithStub(t *testing.T) (*CursorExecutor, *cursorUsageRecorder) {
	t.Helper()
	exec := NewCursorExecutor(minimalCursorConfig("balanced", nil))
	baseURL := serveStubCursorBridge(t)
	if errBind := exec.runtime.BindBridgeForTesting(baseURL, "stub-token", t.TempDir()); errBind != nil {
		t.Fatalf("bind stub bridge: %v", errBind)
	}
	t.Cleanup(exec.Close)
	recorder := &cursorUsageRecorder{}
	coreusage.RegisterNamedPlugin("cursor-executor-test-recorder", recorder)
	return exec, recorder
}

// cursorUsageRecorder captures usage records published through the default manager so
// tests can assert what landed in the usage statistics pipeline.
type cursorUsageRecorder struct {
	records []coreusage.Record
}

func (r *cursorUsageRecorder) HandleUsage(_ context.Context, record coreusage.Record) {
	r.records = append(r.records, record)
}

func (r *cursorUsageRecorder) waitForCursor(t *testing.T, model string) coreusage.Record {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, record := range r.records {
			if record.Provider == "cursor" && record.Model == model {
				return record
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no cursor usage record published for model %q (records: %d)", model, len(r.records))
	return coreusage.Record{}
}

// TestCursorExecuteStreamEmitsSingleFramedChunks is the regression test for the doubled
// "data: " SSE prefix: the executor must emit chunks that, once the OpenAI protocol
// handler writes its own "data: %s\n\n" framing, decode as chat.completion.chunk JSON on
// an OpenAI client. Before the fix every chunk carried a stray "data: " prefix and clients
// failed with AI_JSONParseError.
func TestCursorExecuteStreamEmitsSingleFramedChunks(t *testing.T) {
	exec, _ := newCursorExecutorWithStub(t)
	auth := newCursorTestAuth("key_abc")
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	}
	result, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.6",
		Payload: []byte(`{"model":"grok-4.6","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var texts []string
	var sawTerminal bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		payload := strings.TrimSpace(string(chunk.Payload))
		if payload == "" {
			continue
		}
		// The OpenAI handler wraps each chunk as "data: %s\n\n"; the executor payload must
		// be bare JSON or a bare [DONE], never "data: data: {...}".
		if strings.HasPrefix(payload, "data:") {
			t.Fatalf("chunk carries SSE prefix meant for the protocol handler: %q", payload)
		}
		if payload == "[DONE]" {
			sawTerminal = true
			continue
		}
		var decoded struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				TotalTokens int64 `json:"total_tokens"`
			} `json:"usage"`
		}
		if errUnmarshal := json.Unmarshal([]byte(payload), &decoded); errUnmarshal != nil {
			t.Fatalf("chunk is not valid chat.completion.chunk JSON (client would see AI_JSONParseError): %q: %v", payload, errUnmarshal)
		}
		if decoded.Object != "chat.completion.chunk" {
			t.Fatalf("chunk object = %q", decoded.Object)
		}
		if len(decoded.Choices) > 0 && decoded.Choices[0].Delta.Content != "" {
			texts = append(texts, decoded.Choices[0].Delta.Content)
		}
		if decoded.Usage != nil && decoded.Usage.TotalTokens != 12 {
			t.Fatalf("terminal usage total = %d, want 12 (7 input + 3 cache read + 2 output)", decoded.Usage.TotalTokens)
		}
	}
	// The OpenAI→OpenAI passthrough translator drops the [DONE] marker: the protocol
	// handler writes "data: [DONE]\n\n" itself, so the executor must not emit one.
	if sawTerminal {
		t.Fatal("executor emitted its own [DONE] marker; the protocol handler owns terminal framing")
	}
	if got := strings.Join(texts, ""); got != "Hello world" {
		t.Fatalf("streamed text = %q, want %q", got, "Hello world")
	}
}

// TestCursorExecuteStreamPublishesUsage is the regression test for missing usage records:
// a completed streaming request must publish its token accounting (prompt 10, completion
// 2, total 12) into the usage pipeline.
func TestCursorExecuteStreamPublishesUsage(t *testing.T) {
	exec, recorder := newCursorExecutorWithStub(t)
	auth := newCursorTestAuth("key_abc")
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	}
	result, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.6",
		Payload: []byte(`{"model":"grok-4.6","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}
	record := recorder.waitForCursor(t, "grok-4.6")
	if record.Failed {
		t.Fatal("usage record marked failed for a successful stream")
	}
	if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 2 || record.Detail.TotalTokens != 12 {
		t.Fatalf("usage detail = input %d / output %d / total %d, want 10 / 2 / 12", record.Detail.InputTokens, record.Detail.OutputTokens, record.Detail.TotalTokens)
	}
}

// TestCursorExecutePublishesUsage proves the non-streaming path also lands in the usage
// statistics with the mapped token split.
func TestCursorExecutePublishesUsage(t *testing.T) {
	exec, recorder := newCursorExecutorWithStub(t)
	auth := newCursorTestAuth("key_abc")
	resp, errExec := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.6",
		Payload: []byte(`{"model":"grok-4.6","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	var body struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if errUnmarshal := json.Unmarshal(resp.Payload, &body); errUnmarshal != nil {
		t.Fatalf("Execute() payload = %s", resp.Payload)
	}
	if len(body.Choices) != 1 || body.Choices[0].Message.Content != "Hello world" {
		t.Fatalf("Execute() choices = %+v", body.Choices)
	}
	record := recorder.waitForCursor(t, "grok-4.6")
	if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 2 || record.Detail.TotalTokens != 12 {
		t.Fatalf("usage detail = input %d / output %d / total %d, want 10 / 2 / 12", record.Detail.InputTokens, record.Detail.OutputTokens, record.Detail.TotalTokens)
	}
}

// TestCursorExecuteStreamTranslatesClaudeProtocol proves a Claude-protocol client's stream
// is translated: the executor feeds OpenAI chunks through the response translator, so the
// emitted chunks are Anthropic SSE events rather than raw chat.completion JSON.
func TestCursorExecuteStreamTranslatesClaudeProtocol(t *testing.T) {
	exec, _ := newCursorExecutorWithStub(t)
	auth := newCursorTestAuth("key_abc")
	payload := []byte(`{"model":"grok-4.6","stream":true,"max_tokens":64,` +
		`"messages":[{"role":"user","content":"hi"}]}`)
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		Stream:          true,
		OriginalRequest: payload,
	}
	result, errStream := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "grok-4.6",
		Payload: payload,
	}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	var sawMessageStart, sawMessageStop bool
	var text string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		payloadStr := string(chunk.Payload)
		if strings.Contains(payloadStr, `"type":"message_start"`) {
			sawMessageStart = true
		}
		if strings.Contains(payloadStr, `"type":"message_stop"`) {
			sawMessageStop = true
		}
		// Anthropic events arrive as "event: <type>\ndata: {json}" frames; extract the
		// data line before decoding.
		for _, line := range strings.Split(payloadStr, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var decoded struct {
				Type  string `json:"type"`
				Delta struct {
					Text string `json:"text"`
				} `json:"delta"`
			}
			if errUnmarshal := json.Unmarshal([]byte(data), &decoded); errUnmarshal == nil && decoded.Type == "content_block_delta" {
				text += decoded.Delta.Text
			}
		}
	}
	if !sawMessageStart || !sawMessageStop {
		t.Fatalf("claude stream events: message_start=%v message_stop=%v", sawMessageStart, sawMessageStop)
	}
	if text != "Hello world" {
		t.Fatalf("claude streamed text = %q, want %q", text, "Hello world")
	}
}
