package management

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelprice"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

// maxUsageStatsDays bounds the requested reporting window.
const maxUsageStatsDays = 3650

// maxUsageStatsHours bounds the trailing-hours window.
const maxUsageStatsHours = 168

// usageStatsResponse is the payload served by GET /v0/management/usage-stats.
type usageStatsResponse struct {
	usagestats.Snapshot
	// Pricing describes the model price table used for cost estimation.
	Pricing modelprice.Status `json:"pricing"`
}

// GetUsageStats returns aggregated token consumption and estimated cost per
// model, per provider, and per upstream channel.
//
// Query parameters:
//   - days: trailing window length in days (default 7, max 3650)
//   - hours: trailing window length in hours, reported hourly (max 168).
//     Takes precedence over days; from/to still win over both.
//   - from / to: explicit YYYY-MM-DD range; overrides days when both are set
func (h *Handler) GetUsageStats(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	from := strings.TrimSpace(c.Query("from"))
	to := strings.TrimSpace(c.Query("to"))

	var snapshot usagestats.Snapshot
	switch {
	case from != "" && to != "":
		if !isISODate(from) || !isISODate(to) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from and to must be YYYY-MM-DD"})
			return
		}
		if from > to {
			c.JSON(http.StatusBadRequest, gin.H{"error": "from must not be after to"})
			return
		}
		snapshot = usagestats.DefaultStore().SnapshotRange(from, to)
		snapshot.Days = daysBetween(from, to)
	case strings.TrimSpace(c.Query("hours")) != "":
		hours, errHours := parseUsageStatsHours(c.Query("hours"))
		if errHours != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errHours.Error()})
			return
		}
		snapshot = usagestats.DefaultStore().SnapshotHours(hours)
	default:
		days, errDays := parseUsageStatsDays(c.Query("days"))
		if errDays != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": errDays.Error()})
			return
		}
		snapshot = usagestats.DefaultStore().Snapshot(days)
		snapshot.Days = days
	}

	c.JSON(http.StatusOK, usageStatsResponse{
		Snapshot: snapshot,
		Pricing:  modelprice.CurrentStatus(),
	})
}

func parseUsageStatsHours(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 24, nil
	}
	hours, errHours := strconv.Atoi(value)
	if errHours != nil || hours <= 0 {
		return 0, strconvErr("hours must be a positive integer")
	}
	if hours > maxUsageStatsHours {
		hours = maxUsageStatsHours
	}
	return hours, nil
}

func parseUsageStatsDays(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 7, nil
	}
	days, errDays := strconv.Atoi(value)
	if errDays != nil || days <= 0 {
		return 0, strconvErr("days must be a positive integer")
	}
	if days > maxUsageStatsDays {
		days = maxUsageStatsDays
	}
	return days, nil
}

func isISODate(value string) bool {
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func daysBetween(from, to string) int {
	start, errStart := time.Parse("2006-01-02", from)
	end, errEnd := time.Parse("2006-01-02", to)
	if errStart != nil || errEnd != nil {
		return 0
	}
	return int(end.Sub(start).Hours()/24) + 1
}

type strconvErr string

func (e strconvErr) Error() string { return string(e) }
