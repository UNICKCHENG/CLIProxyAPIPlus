package executor

import (
	"time"
)

// coreauthStatusError carries an HTTP-like status through the manager's StatusError contract.
// Request-scoped faults implement cliproxyexecutor.RequestScopedError so credential cooldown
// is skipped; plain status errors cool the credential normally.
type coreauthStatusError struct {
	status        int
	message       string
	retryable     bool
	requestScoped bool
}

func (e *coreauthStatusError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

// StatusCode implements the manager's status extraction.
func (e *coreauthStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.status
}

// Retryable reports whether a retry might fix the issue automatically.
func (e *coreauthStatusError) Retryable() bool {
	if e == nil {
		return false
	}
	return e.retryable
}

// IsRequestScoped marks failures tied to the request rather than the credential.
func (e *coreauthStatusError) IsRequestScoped() bool {
	if e == nil {
		return false
	}
	return e.requestScoped
}

// timeNowUTC is swappable in tests that exercise expiry paths.
var timeNowUTC = time.Now().UTC
