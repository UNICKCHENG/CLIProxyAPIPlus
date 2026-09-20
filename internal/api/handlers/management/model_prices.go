package management

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelprice"
	log "github.com/sirupsen/logrus"
)

// Paging bounds for the model price catalog.
const (
	defaultModelPricePageSize = 50
	maxModelPricePageSize     = 200
)

// modelPriceSyncTimeout bounds one on-demand refresh so a stalled upstream
// cannot hold the request open indefinitely. It stays below the default client
// timeout used by the management UI, so an unreachable source surfaces as a
// readable error rather than a client-side abort.
const modelPriceSyncTimeout = 60 * time.Second

// modelPricesResponse is the payload served by GET /v0/management/model-prices.
type modelPricesResponse struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Pricing     modelprice.Status  `json:"pricing"`
	Query       string             `json:"query"`
	Page        int                `json:"page"`
	PageSize    int                `json:"page_size"`
	Total       int                `json:"total"`
	Entries     []modelprice.Entry `json:"entries"`
}

// GetModelPrices returns the read-only price catalog backing cost estimation.
//
// The catalog lists the raw lookup keys rather than prettified model names, so
// an operator can see exactly which name the estimator resolves and diagnose
// requests that were counted as unpriced.
//
// Query parameters:
//   - q: case-insensitive substring filter on the model key
//   - page: 1-based page number (default 1)
//   - page_size: entries per page (default 50, max 200)
func (h *Handler) GetModelPrices(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	page, errPage := parseModelPricePage(c.Query("page"))
	if errPage != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errPage.Error()})
		return
	}
	pageSize, errSize := parseModelPricePageSize(c.Query("page_size"))
	if errSize != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": errSize.Error()})
		return
	}

	query := strings.TrimSpace(c.Query("q"))
	matched := filterModelPriceEntries(modelprice.Default().Entries(), query)

	total := len(matched)
	start := min((page-1)*pageSize, total)
	end := min(start+pageSize, total)

	c.JSON(http.StatusOK, modelPricesResponse{
		GeneratedAt: time.Now().UTC(),
		Pricing:     modelprice.CurrentStatus(),
		Query:       query,
		Page:        page,
		PageSize:    pageSize,
		Total:       total,
		Entries:     matched[start:end],
	})
}

// modelPriceSyncResponse is the payload served by POST /v0/management/model-prices/sync.
type modelPriceSyncResponse struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Pricing     modelprice.Status `json:"pricing"`
}

// SyncModelPrices refreshes the price catalog from the upstream cost map on
// demand.
//
// The catalog is otherwise only fetched once per process start, so this is the
// only way to pick up upstream price changes without restarting the server. A
// failure leaves the previously loaded prices untouched — the sync installs the
// new table only after the payload has been fetched and parsed.
func (h *Handler) SyncModelPrices(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	// Derived from the request so a client that gives up also stops the download.
	ctx, cancel := context.WithTimeout(c.Request.Context(), modelPriceSyncTimeout)
	defer cancel()

	if _, errSync := modelprice.SyncNow(ctx); errSync != nil {
		log.WithError(errSync).Warn("management: model price sync failed")
		// 502 rather than 500: the request was well-formed, the upstream source is
		// what did not deliver a usable cost map.
		c.JSON(http.StatusBadGateway, gin.H{"error": errSync.Error()})
		return
	}

	c.JSON(http.StatusOK, modelPriceSyncResponse{
		GeneratedAt: time.Now().UTC(),
		Pricing:     modelprice.CurrentStatus(),
	})
}

// filterModelPriceEntries returns the entries whose model key contains query.
// An empty query returns the input unchanged.
func filterModelPriceEntries(entries []modelprice.Entry, query string) []modelprice.Entry {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return entries
	}
	out := make([]modelprice.Entry, 0, len(entries))
	for _, entry := range entries {
		if strings.Contains(strings.ToLower(entry.Model), needle) {
			out = append(out, entry)
		}
	}
	return out
}

func parseModelPricePage(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 1, nil
	}
	page, errPage := strconv.Atoi(value)
	if errPage != nil || page <= 0 {
		return 0, strconvErr("page must be a positive integer")
	}
	return page, nil
}

func parseModelPricePageSize(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultModelPricePageSize, nil
	}
	size, errSize := strconv.Atoi(value)
	if errSize != nil || size <= 0 {
		return 0, strconvErr("page_size must be a positive integer")
	}
	if size > maxModelPricePageSize {
		size = maxModelPricePageSize
	}
	return size, nil
}
