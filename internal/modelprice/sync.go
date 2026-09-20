package modelprice

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxCostMapBytes caps how much of a remote cost map we are willing to read.
const maxCostMapBytes = 32 << 20

// Override is a single operator-provided price override. Rates are expressed
// per one million tokens, which is how vendor price pages are published.
type Override struct {
	Model      string  `yaml:"model" json:"model"`
	Input      float64 `yaml:"input" json:"input"`
	Output     float64 `yaml:"output" json:"output"`
	CacheRead  float64 `yaml:"cache-read" json:"cache-read"`
	CacheWrite float64 `yaml:"cache-write" json:"cache-write"`
	Reasoning  float64 `yaml:"reasoning" json:"reasoning"`
}

// ToPrice converts per-million rates into a per-token Price.
func (o Override) ToPrice() Price {
	return Price{
		InputPerToken:      ParsePerMillion(o.Input),
		OutputPerToken:     ParsePerMillion(o.Output),
		CacheReadPerToken:  ParsePerMillion(o.CacheRead),
		CacheWritePerToken: ParsePerMillion(o.CacheWrite),
		ReasoningPerToken:  ParsePerMillion(o.Reasoning),
		Source:             "override",
	}
}

// ApplyOverrides installs per-million overrides on the default table.
func ApplyOverrides(overrides []Override) {
	if len(overrides) == 0 {
		defaultTable.SetOverrides(nil)
		return
	}
	prices := make(map[string]Price, len(overrides))
	for _, override := range overrides {
		model := strings.TrimSpace(override.Model)
		if model == "" {
			continue
		}
		prices[model] = override.ToPrice()
	}
	defaultTable.SetOverrides(prices)
}

// LoadCostMapFile reads a LiteLLM cost map from disk into the default table.
func LoadCostMapFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return defaultTable.ApplyLiteLLM(data)
}

// SaveCostMapFile stores a downloaded cost map so the next start is offline-safe.
func SaveCostMapFile(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("cost map cache path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// FetchCostMap downloads the LiteLLM cost map.
func FetchCostMap(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	if strings.TrimSpace(url) == "" {
		url = DefaultLiteLLMURL
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CLIProxyAPI-modelprice")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cost map request failed: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCostMapBytes))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("cost map response was empty")
	}
	return data, nil
}
