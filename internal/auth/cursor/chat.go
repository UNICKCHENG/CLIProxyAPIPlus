package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	sdkv1 "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cursor/sdk/v1"
)

// ChatRunRequest is one inference request resolved from the executor: the persisted credential
// plus the parsed chat-completions payload.
type ChatRunRequest struct {
	APIKey      string
	ProxyURL    string
	Model       string
	OptimizeFor string
	// Payload is the OpenAI chat-completions request body.
	Payload []byte
}

// ChatUsage is Cursor's token accounting mapped onto the OpenAI prompt/completion split.
type ChatUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
}

// chatUsageFromSdk maps SDK usage; cache reads are prompt tokens that were served from cache.
func chatUsageFromSdk(usage *sdkv1.TokenUsage) *ChatUsage {
	if usage == nil {
		return nil
	}
	prompt := usage.GetInputTokens() + usage.GetCacheReadTokens()
	completion := usage.GetOutputTokens()
	return &ChatUsage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion}
}

// RunChat drives one non-streaming completion and returns a full chat.completion body.
func (r *Runtime) RunChat(ctx context.Context, req ChatRunRequest) ([]byte, *ChatUsage, error) {
	genReq, errBuild := r.buildChatRunRequest(req)
	if errBuild != nil {
		return nil, nil, errBuild
	}
	result, errRun := r.runGenerate(ctx, genReq, nil)
	if errRun != nil {
		return nil, nil, errRun
	}
	var payload []byte
	var errBody error
	if len(result.toolCalls) > 0 {
		payload, errBody = buildToolCallCompletion(newCompletionID(), req.Model, result.text, result.toolCalls)
	} else {
		payload, errBody = buildCompletion(newCompletionID(), req.Model, result.text, result.usage)
	}
	if errBody != nil {
		return nil, nil, errBody
	}
	return payload, chatUsageFromSdk(result.usage), nil
}

// RunChatStream drives one streaming completion, emitting every OpenAI chat-completion chunk
// as raw JSON through emit. It applies no deadline: once the upstream Cursor run is live the
// caller must not time it out. The returned usage is the terminal accounting, nil on failure.
func (r *Runtime) RunChatStream(ctx context.Context, req ChatRunRequest, emit func(payload []byte) error) (*ChatUsage, error) {
	if emit == nil {
		return nil, fmt.Errorf("cursor stream requires an emit callback")
	}
	genReq, errBuild := r.buildChatRunRequest(req)
	if errBuild != nil {
		return nil, errBuild
	}

	completionID := newCompletionID()
	roleSent := false
	onDelta := func(text string) error {
		// The role rides along with the first content delta. Emitting it in a chunk of its
		// own makes the Gemini translator report a finished turn before any text.
		delta := chatCompletionDelta{Content: text}
		if !roleSent {
			delta.Role = "assistant"
			roleSent = true
		}
		chunk := buildStreamChunk(completionID, req.Model, delta, nil, nil)
		if chunk == nil {
			return fmt.Errorf("cursor stream chunk could not be encoded")
		}
		return emit(chunk)
	}

	result, errRun := r.runGenerate(ctx, genReq, onDelta)
	if errRun != nil {
		return nil, errRun
	}
	if len(result.toolCalls) > 0 {
		for i, call := range result.toolCalls {
			idx := i
			deltaCall := call
			deltaCall.Index = &idx
			delta := chatCompletionDelta{ToolCalls: []chatToolCall{deltaCall}}
			if !roleSent {
				delta.Role = "assistant"
				roleSent = true
			}
			chunk := buildStreamChunk(completionID, req.Model, delta, nil, nil)
			if chunk == nil {
				return nil, fmt.Errorf("cursor stream chunk could not be encoded")
			}
			if errEmit := emit(chunk); errEmit != nil {
				return nil, errEmit
			}
		}
		finish := "tool_calls"
		final := buildStreamChunk(completionID, req.Model, chatCompletionDelta{}, &finish, nil)
		if final == nil {
			return nil, fmt.Errorf("cursor stream chunk could not be encoded")
		}
		if errEmit := emit(final); errEmit != nil {
			return nil, errEmit
		}
		return nil, nil
	}
	finish := "stop"
	final := buildStreamChunk(completionID, req.Model, chatCompletionDelta{}, &finish, result.usage)
	if final == nil {
		return nil, fmt.Errorf("cursor stream chunk could not be encoded")
	}
	if errEmit := emit(final); errEmit != nil {
		return nil, errEmit
	}
	return chatUsageFromSdk(result.usage), nil
}

// buildChatRunRequest converts the chat-completions payload into a bridge run request.
func (r *Runtime) buildChatRunRequest(req ChatRunRequest) (generateRequest, error) {
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		return generateRequest{}, &upstreamError{failure: upstreamFailure{
			Message:    upstreamErrorText(401, "cursor auth does not contain an api_key"),
			HTTPStatus: 401,
		}}
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(gjson.GetBytes(req.Payload, "model").String())
	}
	if model == "" {
		return generateRequest{}, fmt.Errorf("request does not specify a model")
	}
	chat, errParse := parseChatRequest(req.Payload)
	if errParse != nil {
		return generateRequest{}, errParse
	}
	optimizeFor := strings.TrimSpace(req.OptimizeFor)
	if optimizeFor == "" {
		optimizeFor = defaultOptimizeFor
	}
	return generateRequest{
		runtime:          r,
		apiKey:           apiKey,
		proxyURL:         req.ProxyURL,
		model:            model,
		params:           chat.Params,
		optimizeFor:      optimizeFor,
		prompt:           chat.Prompt,
		images:           chat.Images,
		tools:            chat.Tools,
		toolResults:      chat.ToolResults,
		sessionPrefix:    chat.SessionPrefix,
		sessionIncrement: chat.SessionIncrement,
		incrementImages:  chat.IncrementImages,
	}, nil
}

// EstimateTokens keeps the source character estimate: the Agent SDK reports token counts only
// after a run, so there is no upstream endpoint to ask ahead of time.
func (r *Runtime) EstimateTokens(payload []byte) (map[string]any, error) {
	estimate := 0
	if chat, errParse := parseChatRequest(payload); errParse == nil {
		// Rough 4-characters-per-token heuristic; Cursor bills on its own measured counts.
		estimate = (len(chat.Prompt) + 3) / 4
	}
	return map[string]any{
		"input_tokens": estimate,
		"total_tokens": estimate,
	}, nil
}

// Failure is the classification of one upstream failure in the terms the executor maps onto
// the manager's error contracts.
type Failure struct {
	Message    string
	HTTPStatus int
	Retryable  bool
}

// FailureFrom recovers the classification from any error the bridge path can produce.
func FailureFrom(err error) Failure {
	f := failureFrom(err)
	return Failure{Message: f.Message, HTTPStatus: f.HTTPStatus, Retryable: f.Retryable}
}

// NewFailureError wraps a classified failure as an error the executor's mapping consumes,
// mirroring what the bridge path produces. Exported for tests and the management import.
func NewFailureError(f Failure) error {
	return &upstreamError{failure: upstreamFailure{Message: f.Message, HTTPStatus: f.HTTPStatus, Retryable: f.Retryable}}
}

// MarshalChatUsage renders usage for count-tokens style responses.
func MarshalChatUsage(input, total int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"input_tokens": input,
		"total_tokens": total,
	})
}
