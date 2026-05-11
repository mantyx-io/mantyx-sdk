package mantyx

import "fmt"

// Error is the base error type returned by the SDK. Callers can use
// `errors.As(err, &mantyx.Error{})` to inspect Code / HTTPStatus / Hint.
type Error struct {
	Message    string
	Code       string
	HTTPStatus int
	Hint       string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("mantyx: %s (%s)", e.Message, e.Code)
	}
	return "mantyx: " + e.Message
}

// AuthError is returned when the API rejects the request as unauthenticated
// (HTTP 401).
type AuthError struct {
	Inner *Error
}

func (e *AuthError) Error() string { return e.Inner.Error() }
func (e *AuthError) Unwrap() error { return e.Inner }

// NetworkError is returned when an HTTP request fails before reaching the
// server (DNS, TCP, TLS, etc.) or the response body times out.
type NetworkError struct {
	Inner *Error
	Cause error
}

func (e *NetworkError) Error() string { return e.Inner.Error() }
func (e *NetworkError) Unwrap() error { return e.Cause }

// ToolError is returned when a local tool's `Execute` callback returns an
// error.
type ToolError struct {
	ToolName string
	Cause    error
}

func (e *ToolError) Error() string {
	return fmt.Sprintf("mantyx: local tool %q failed: %v", e.ToolName, e.Cause)
}

func (e *ToolError) Unwrap() error { return e.Cause }

// RunError is returned when the server emits a terminal `error` event for
// the run, when a non-success `result` arrives, or when the SDK aborts the
// run client-side.
//
// When the run ended via a terminal `error` event (e.g. the model
// truncated mid-reply), the optional triage attributes carry the
// structured fields documented in `docs/agent-runs-protocol.md` §7 so
// callers can render UI banners ("truncated reply — JSON likely
// incomplete") and drive retry policy without re-parsing the
// human-readable Message:
//
//   - ErrorClass — canonical category ("rate_limit", "overloaded",
//     "server", "context_window", "truncation", "invalid_request",
//     "auth", "timeout", "local_timeout", "upstream_deadline",
//     "unknown"). New categories may land additively.
//   - FinishReason — canonical lowercase provider stop reason
//     ("max_tokens", "refusal", "malformed_function_call", …). Mirrors
//     the last `assistant_message` event's finishReason.
//   - PartialText — best-effort raw bytes the model emitted before
//     the failure. For OutputSchema runs this is likely incomplete JSON
//     that will fail `json.Unmarshal` — treat it as diagnostic data,
//     never as a schema-conformant reply.
//   - Retryable — coarse retry hint from the pipeline's classifier.
//     Use `RunErrorRetryable(err)` to read it as a tri-state value.
type RunError struct {
	RunID   string
	Code    string
	Message string

	// ErrorClass is the canonical failure category. Empty when the
	// server did not classify the failure (older runners, non-terminal
	// `error` paths).
	ErrorClass string
	// FinishReason is the canonical provider stop reason. Empty when
	// the failure was not produced by a model turn (e.g. local timeout).
	FinishReason string
	// PartialText carries the model's best-effort raw output bytes when
	// the run terminated mid-reply (typically `ErrorClass == "truncation"`).
	// May be empty.
	PartialText string
	// Retryable is the coarse retry hint inherited from the pipeline's
	// error classifier. nil means "the server did not say"; non-nil
	// callers can `*r.Retryable` to read the bool.
	Retryable *bool
}

func (e *RunError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("mantyx: run %s failed (%s): %s", e.RunID, e.Code, e.Message)
	}
	return fmt.Sprintf("mantyx: run %s failed: %s", e.RunID, e.Message)
}

// ParseError is returned by ParseRunOutput when the run's terminal text
// cannot be JSON-parsed (or fails the user-supplied json.Unmarshal target).
// The original assistant text is preserved on the Text field so callers can
// log the raw model output for debugging.
type ParseError struct {
	RunID string
	Text  string
	Cause error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("mantyx: run %s output failed to parse: %v", e.RunID, e.Cause)
}

func (e *ParseError) Unwrap() error { return e.Cause }
