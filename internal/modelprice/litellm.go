package modelprice

import (
	"encoding/json"
	"sort"
	"strings"
)

// DefaultLiteLLMURL points at the LiteLLM public cost map on GitHub.
const DefaultLiteLLMURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// litellmEntry mirrors the subset of the LiteLLM cost map schema we consume.
// https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json
type litellmEntry struct {
	LitellmProvider             string   `json:"litellm_provider"`
	Mode                        string   `json:"mode"`
	InputCostPerToken           float64  `json:"input_cost_per_token"`
	OutputCostPerToken          float64  `json:"output_cost_per_token"`
	CacheReadInputTokenCost     float64  `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost float64  `json:"cache_creation_input_token_cost"`
	OutputCostPerReasoningToken float64  `json:"output_cost_per_reasoning_token"`
	Aliases                     []string `json:"aliases"`
}

// providerPriority ranks providers when several publish the same bare model
// name. Lower values win. First-party providers outrank aggregators so that a
// reseller's markup does not replace the reference price.
var providerPriority = map[string]int{
	"anthropic":                  0,
	"openai":                     1,
	"gemini":                     2,
	"google":                     2,
	"vertex_ai-language-models":  3,
	"vertex_ai-anthropic_models": 4,
	"vertex_ai":                  5,
	"xai":                        6,
	"mistral":                    7,
	"deepseek":                   8,
	"moonshot":                   9,
	"moonshot_ai":                9,
	"meta":                       10,
	"meta_llama":                 10,
	"qwen":                       11,
	"dashscope":                  11,
	"azure":                      20,
	"bedrock":                    21,
	"bedrock_converse":           22,
	"azure_ai":                   23,
}

const defaultProviderPriority = 30

// aggregatorPriority is applied to resellers and gateways whose prices may
// include a markup over the first-party reference price.
const aggregatorPriority = 60

var aggregatorProviders = map[string]struct{}{
	"openrouter":        {},
	"fireworks_ai":      {},
	"deepinfra":         {},
	"novita":            {},
	"together_ai":       {},
	"vercel_ai_gateway": {},
	"perplexity":        {},
	"replicate":         {},
	"aihubmix":          {},
	"cloudflare":        {},
	"nebius":            {},
	"databricks":        {},
	"snowflake":         {},
	"oci":               {},
	"wandb":             {},
	"hyperbolic":        {},
	"lambda_ai":         {},
	"anyscale":          {},
	"sagemaker":         {},
	"watsonx":           {},
	"gigachat":          {},
	"nscale":            {},
	"featherless_ai":    {},
	"clarifai":          {},
	"petals":            {},
	"voyage":            {},
	"sambanova":         {},
	"cerebras":          {},
	"groq":              {},
	"xiaomi_mimo":       {},
	"dashscope_intl":    {},
	"aleph_alpha":       {},
	"jina_ai":           {},
	"volcengine":        {},
	"minimax":           {},
	"zhipu":             {},
	"cohere":            {},
	"ai21":              {},
	"nlpcloud":          {},
	"triton":            {},
	"empower":           {},
	"xinference":        {},
}

// billableModes lists the LiteLLM modes that consume token pricing.
var billableModes = map[string]struct{}{
	"chat":       {},
	"completion": {},
	"responses":  {},
	"realtime":   {},
}

// ApplyLiteLLM parses a LiteLLM cost-map JSON document, replaces the base
// table, and reports how many model keys were indexed.
func (t *Table) ApplyLiteLLM(data []byte) (int, error) {
	if t == nil {
		return 0, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0, err
	}

	entries := make(map[string]Price, len(raw))
	priorities := make(map[string]int, len(raw))

	// Deterministic iteration keeps the winner stable across runs.
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if key == "sample_spec" {
			continue
		}
		if !isUsableCostMapKey(key) {
			continue
		}
		var entry litellmEntry
		if err := json.Unmarshal(raw[key], &entry); err != nil {
			continue
		}
		if entry.Mode != "" {
			if _, ok := billableModes[strings.ToLower(entry.Mode)]; !ok {
				continue
			}
		}
		price := Price{
			InputPerToken:      entry.InputCostPerToken,
			OutputPerToken:     entry.OutputCostPerToken,
			CacheReadPerToken:  entry.CacheReadInputTokenCost,
			CacheWritePerToken: entry.CacheCreationInputTokenCost,
			ReasoningPerToken:  entry.OutputCostPerReasoningToken,
		}
		price.Source = "litellm"
		if !price.Known() {
			continue
		}
		priority := priorityForProvider(entry.LitellmProvider)
		for _, indexKey := range indexKeys(key) {
			setEntry(entries, priorities, indexKey, price, priority)
		}
		for _, alias := range entry.Aliases {
			for _, indexKey := range indexKeys(alias) {
				setEntry(entries, priorities, indexKey, price, priority+1)
			}
		}
	}

	t.replaceBase(entries, priorities, "litellm")
	return len(entries), nil
}

// isUsableCostMapKey filters LiteLLM keys that carry variants our lookup cannot
// disambiguate, such as batch or region-suffixed duplicates.
func isUsableCostMapKey(key string) bool {
	if key == "" {
		return false
	}
	if strings.ContainsAny(key, ":+") {
		return false
	}
	lower := strings.ToLower(key)
	for _, marker := range []string{"/batch", "-batch", "-global", "-regional", "gpt-4o-realtime"} {
		if strings.HasSuffix(lower, marker) {
			return false
		}
	}
	return true
}

func priorityForProvider(provider string) int {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return defaultProviderPriority
	}
	if priority, ok := providerPriority[provider]; ok {
		return priority
	}
	if _, ok := aggregatorProviders[provider]; ok {
		return aggregatorPriority
	}
	return defaultProviderPriority
}
