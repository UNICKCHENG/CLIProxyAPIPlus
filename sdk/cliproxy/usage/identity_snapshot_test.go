package usage

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCapturedIdentityAllowsExplicitEmptyOverrides(t *testing.T) {
	parent := WithCapturedIdentity(t.Context(), "parent-key", "config-inline", true)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set("userApiKey", "reused-key")
	ginCtx.Set("accessProvider", "reused-provider")
	for _, captured := range []bool{false, true} {
		ctx := parent
		if !captured {
			ctx = WithAPIKey(t.Context(), "parent-key")
			ctx = WithAccessProvider(ctx, "config-inline")
		}
		ctx = context.WithValue(ctx, "gin", ginCtx)
		ctx = WithAPIKey(ctx, " ")
		ctx = WithAccessProvider(ctx, " ")
		ctx = WithIsNativeKey(ctx, false)
		ctx = CaptureIdentity(ctx)
		if APIKeyFromContext(ctx) != "" || AccessProviderFromContext(ctx) != "" {
			t.Fatalf("captured=%v: explicit empty identity inherited parent values", captured)
		}
		if native, ok := IsNativeKeyFromContext(ctx); !ok || native {
			t.Fatalf("captured=%v: native key = (%v, %v), want (false, true)", captured, native, ok)
		}
	}
	if APIKeyFromContext(parent) != "parent-key" || AccessProviderFromContext(parent) != "config-inline" {
		t.Fatal("child overrides mutated parent identity")
	}
}
