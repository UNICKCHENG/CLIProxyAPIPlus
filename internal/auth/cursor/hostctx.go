package cursor

import "context"

// requestContextKey carries request-scoped metadata that the plugin used to receive from the
// host through its callback id: cancellation ownership and log correlation.
type requestContextKey struct{}

// withRequestContext stores the executor's per-request metadata on the context.
func withRequestContext(ctx context.Context, meta *requestMeta) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if meta == nil {
		return ctx
	}
	return context.WithValue(ctx, requestContextKey{}, meta)
}

func requestMetaFrom(ctx context.Context) *requestMeta {
	if ctx == nil {
		return nil
	}
	meta, _ := ctx.Value(requestContextKey{}).(*requestMeta)
	return meta
}

// requestMeta is the per-request identity the native executor forwards into the runtime.
type requestMeta struct {
	RequestID string
}
