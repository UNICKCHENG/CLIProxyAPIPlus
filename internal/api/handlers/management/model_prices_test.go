package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelprice"
)

// modelPriceTestMap holds uniquely named models so assertions stay independent
// of whatever the process-wide table happens to contain.
const modelPriceTestMap = `{
  "sample_spec": {"input_cost_per_token": 0},
  "e2e-alpha-model": {
    "litellm_provider": "openai",
    "mode": "chat",
    "input_cost_per_token": 0.000001,
    "output_cost_per_token": 0.000002
  },
  "e2e-beta-model": {
    "litellm_provider": "anthropic",
    "mode": "chat",
    "input_cost_per_token": 0.000003,
    "output_cost_per_token": 0.000004
  },
  "e2e-gamma-embed": {
    "litellm_provider": "openai",
    "mode": "embedding",
    "input_cost_per_token": 0.0000001
  }
}`

func decodeModelPrices(t *testing.T, body []byte) modelPricesResponse {
	t.Helper()
	var payload modelPricesResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload
}

func TestFilterModelPriceEntries(t *testing.T) {
	entries := []modelprice.Entry{
		{Model: "e2e-alpha-model"},
		{Model: "e2e-beta-model"},
		{Model: "E2E-Gamma-Model"},
	}

	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"empty query keeps everything", "", []string{"e2e-alpha-model", "e2e-beta-model", "E2E-Gamma-Model"}},
		{"blank query keeps everything", "   ", []string{"e2e-alpha-model", "e2e-beta-model", "E2E-Gamma-Model"}},
		{"substring filter", "beta", []string{"e2e-beta-model"}},
		{"filter is case-insensitive", "ALPHA", []string{"e2e-alpha-model"}},
		{"query is trimmed", "  gamma  ", []string{"E2E-Gamma-Model"}},
		{"shared prefix keeps all", "e2e-", []string{"e2e-alpha-model", "e2e-beta-model", "E2E-Gamma-Model"}},
		{"no match yields an empty slice", "zzz", []string{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterModelPriceEntries(entries, tc.query)
			if len(got) != len(tc.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tc.want))
			}
			for i, wantModel := range tc.want {
				if got[i].Model != wantModel {
					t.Errorf("entry[%d].Model = %q, want %q", i, got[i].Model, wantModel)
				}
			}
		})
	}
}

func TestFilterModelPriceEntriesReturnsNonNilForNoMatch(t *testing.T) {
	// A nil slice would serialize as JSON null instead of [], which would break
	// the caller's length checks.
	if got := filterModelPriceEntries([]modelprice.Entry{{Model: "a"}}, "zzz"); got == nil {
		t.Error("filterModelPriceEntries() = nil, want an empty non-nil slice")
	}
}

func TestParseModelPricePage(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{"default", "", 1, false},
		{"explicit page", "7", 7, false},
		{"padded value", "  3  ", 3, false},
		{"zero is rejected", "0", 0, true},
		{"negative is rejected", "-2", 0, true},
		{"non-numeric is rejected", "abc", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseModelPricePage(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseModelPricePage(%q) = %d, want an error", tc.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseModelPricePage(%q) error = %v", tc.value, err)
			}
			if got != tc.want {
				t.Errorf("parseModelPricePage(%q) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestParseModelPricePageSize(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    int
		wantErr bool
	}{
		{"default", "", defaultModelPricePageSize, false},
		{"explicit size", "25", 25, false},
		{"capped at the maximum", "1000", maxModelPricePageSize, false},
		{"exactly the maximum", "200", maxModelPricePageSize, false},
		{"zero is rejected", "0", 0, true},
		{"negative is rejected", "-5", 0, true},
		{"non-numeric is rejected", "lots", 0, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseModelPricePageSize(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseModelPricePageSize(%q) = %d, want an error", tc.value, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseModelPricePageSize(%q) error = %v", tc.value, err)
			}
			if got != tc.want {
				t.Errorf("parseModelPricePageSize(%q) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestGetModelPricesPaginationAndFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if _, err := modelprice.Default().ApplyLiteLLM([]byte(modelPriceTestMap)); err != nil {
		t.Fatalf("ApplyLiteLLM() error = %v", err)
	}

	cases := []struct {
		name      string
		target    string
		wantTotal int
		wantCount int
		wantPage  int
	}{
		{"filter narrows to both e2e chat models", "/v0/management/model-prices?q=e2e-&page_size=100", 2, 2, 1},
		{"filter narrows to one model", "/v0/management/model-prices?q=alpha", 1, 1, 1},
		{"non-billable modes are not catalogued", "/v0/management/model-prices?q=gamma", 0, 0, 1},
		{"page 2 of a one-per-page listing is empty", "/v0/management/model-prices?q=alpha&page_size=1&page=2", 1, 0, 2},
		{"unknown query yields no rows", "/v0/management/model-prices?q=zzz-nope", 0, 0, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(rec)
			ginCtx.Request = httptest.NewRequest(http.MethodGet, tc.target, nil)

			h := &Handler{}
			h.GetModelPrices(ginCtx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
			}
			payload := decodeModelPrices(t, rec.Body.Bytes())
			if payload.Total != tc.wantTotal {
				t.Errorf("Total = %d, want %d", payload.Total, tc.wantTotal)
			}
			if len(payload.Entries) != tc.wantCount {
				t.Errorf("len(Entries) = %d, want %d", len(payload.Entries), tc.wantCount)
			}
			if payload.Page != tc.wantPage {
				t.Errorf("Page = %d, want %d", payload.Page, tc.wantPage)
			}
			if payload.Pricing.EntryCount == 0 {
				t.Error("Pricing.EntryCount = 0, want the indexed table size")
			}
		})
	}
}

func TestGetModelPricesRejectsInvalidPaging(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, target := range []string{
		"/v0/management/model-prices?page=0",
		"/v0/management/model-prices?page=-1",
		"/v0/management/model-prices?page=abc",
		"/v0/management/model-prices?page_size=0",
		"/v0/management/model-prices?page_size=-3",
		"/v0/management/model-prices?page_size=many",
	} {
		t.Run(target, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(rec)
			ginCtx.Request = httptest.NewRequest(http.MethodGet, target, nil)

			h := &Handler{}
			h.GetModelPrices(ginCtx)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

func TestGetModelPricesEntriesIsAlwaysAnArray(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/model-prices?q=definitely-absent", nil)

	h := &Handler{}
	h.GetModelPrices(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	// The dashboard renders `entries.length` directly, so null would crash it.
	if !strings.Contains(rec.Body.String(), `"entries":[]`) {
		t.Errorf("body does not contain an empty entries array: %s", rec.Body.String())
	}
}

func TestGetModelPricesNilHandlerIsRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/model-prices", nil)

	var h *Handler
	h.GetModelPrices(ginCtx)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestSyncModelPricesNilHandlerIsRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/model-prices/sync", nil)

	var h *Handler
	h.SyncModelPrices(ginCtx)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// A sync that cannot reach the upstream reports a gateway failure rather than a
// success, and must not disturb the prices already loaded. The request context is
// cancelled up front so the case is deterministic and never touches the network.
func TestSyncModelPricesReportsUnreachableUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	if _, err := modelprice.Default().ApplyLiteLLM([]byte(modelPriceTestMap)); err != nil {
		t.Fatalf("ApplyLiteLLM() error = %v", err)
	}
	before, okBefore := modelprice.Default().Lookup("e2e-alpha-model", "")
	if !okBefore {
		t.Fatal("fixture model is not indexed before the sync")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(
		http.MethodPost, "/v0/management/model-prices/sync", nil,
	).WithContext(ctx)

	h := &Handler{}
	h.SyncModelPrices(ginCtx)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("body does not carry an error message: %s", rec.Body.String())
	}

	after, okAfter := modelprice.Default().Lookup("e2e-alpha-model", "")
	if !okAfter {
		t.Fatal("a failed sync dropped the previously loaded prices")
	}
	if after.InputPerToken != before.InputPerToken {
		t.Errorf("InputPerToken changed from %v to %v after a failed sync", before.InputPerToken, after.InputPerToken)
	}
}

// The response mirrors the GET payload, so the catalog header can be refreshed
// from a sync without a second round trip. The frontend reads both keys, so a
// rename here would silently blank the header.
func TestModelPriceSyncResponseCarriesPricingStatus(t *testing.T) {
	payload, errMarshal := json.Marshal(modelPriceSyncResponse{
		GeneratedAt: time.Now().UTC(),
		Pricing:     modelprice.CurrentStatus(),
	})
	if errMarshal != nil {
		t.Fatalf("marshal payload: %v", errMarshal)
	}

	body := string(payload)
	for _, key := range []string{`"generated_at"`, `"pricing"`, `"sync_enabled"`, `"updated_at"`} {
		if !strings.Contains(body, key) {
			t.Errorf("marshalled payload is missing %s: %s", key, body)
		}
	}
}
