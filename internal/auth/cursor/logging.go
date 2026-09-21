package cursor

import (
	"context"
	"strings"

	log "github.com/sirupsen/logrus"
)

// providerIdentifier is the provider key shared by the executor, authenticator and model
// registration. Auth records must carry this provider for the host scheduler to bind them to
// the executor.
const providerIdentifier = "cursor"

// requestIDKey carries an optional request-scoped identifier on contexts so log lines can be
// correlated with the host's request log.
type requestIDKey struct{}

// withRequestID stores the host request id on the context.
func withRequestID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

func requestIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return strings.TrimSpace(id)
}

// hostLog emits a structured record through the shared logger. The native runtime shares the
// server process, so logrus fields replace the plugin host log callback.
func hostLog(level, message string, fields map[string]any) {
	hostLogContext(context.Background(), level, message, fields)
}

func hostLogContext(ctx context.Context, level, message string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["provider"] = providerIdentifier
	if id := requestIDFrom(ctx); id != "" {
		fields["request_id"] = id
	}
	entry := log.WithFields(log.Fields(fields))
	switch strings.ToLower(level) {
	case "debug":
		entry.Debug(message)
	case "warn":
		entry.Warn(message)
	case "error":
		entry.Error(message)
	default:
		entry.Info(message)
	}
}
