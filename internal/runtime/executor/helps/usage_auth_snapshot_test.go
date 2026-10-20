package helps

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type authSnapshotUsageSink struct{ records chan usage.Record }

func (s authSnapshotUsageSink) HandleUsage(_ context.Context, record usage.Record) {
	select {
	case s.records <- record:
	default:
	}
}

func TestUsageReporterRetainsAuthAfterGinContextReuse(t *testing.T) {
	for _, authenticated := range []bool{false, true} {
		name := "anonymous"
		if authenticated {
			name = "authenticated"
		}
		t.Run(name, func(t *testing.T) {
			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			wantKey, wantProvider := "", ""
			if authenticated {
				wantKey, wantProvider = "original-key", "config-inline"
				ginCtx.Set("userApiKey", wantKey)
				ginCtx.Set("accessProvider", wantProvider)
			}
			ctx := context.WithValue(context.Background(), "gin", ginCtx)
			reporter := NewUsageReporter(ctx, "antigravity", "model", nil)
			// Simulate Gin returning the context to its pool and assigning another request.
			ginCtx.Keys = map[string]any{"userApiKey": "next-key", "accessProvider": "next-provider"}
			records := make(chan usage.Record, 1)
			usage.RegisterNamedPlugin(t.Name(), authSnapshotUsageSink{records: records})
			t.Cleanup(func() { usage.RegisterNamedPlugin(t.Name(), authSnapshotUsageSink{}) })
			reporter.Publish(ctx, usage.Detail{InputTokens: 3, OutputTokens: 4, TotalTokens: 7})
			select {
			case got := <-records:
				if got.APIKey != wantKey || got.AccessProvider != wantProvider || got.IsNativeKey != authenticated {
					t.Fatalf("usage auth = (%q, %q, %v), want (%q, %q, %v)", got.APIKey, got.AccessProvider, got.IsNativeKey, wantKey, wantProvider, authenticated)
				}
				if got.Detail.TotalTokens != 7 {
					t.Fatalf("total tokens = %d, want 7", got.Detail.TotalTokens)
				}
			case <-time.After(time.Second):
				t.Fatal("usage record was not delivered")
			}
		})
	}
}
