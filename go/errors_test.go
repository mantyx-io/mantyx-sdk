package mantyx

import (
	"errors"
	"fmt"
	"testing"
)

func TestError_Error(t *testing.T) {
	e := &Error{Message: "bad", Code: "x"}
	if got := e.Error(); got != "mantyx: bad (x)" {
		t.Fatalf("got %q", got)
	}
	e2 := &Error{Message: "no code"}
	if got := e2.Error(); got != "mantyx: no code" {
		t.Fatalf("got %q", got)
	}
}

func TestAuthError_Unwrap(t *testing.T) {
	inner := &Error{Message: "nope", Code: "unauthorized"}
	wrapped := &AuthError{Inner: inner}
	if !errors.Is(wrapped, inner) {
		t.Fatal("expected errors.Is to find inner")
	}
	if got := wrapped.Error(); got != inner.Error() {
		t.Fatalf("got %q want %q", got, inner.Error())
	}
}

func TestNetworkError_Unwrap(t *testing.T) {
	cause := fmt.Errorf("econnrefused")
	inner := &Error{Message: "down", Code: "network"}
	wrapped := &NetworkError{Inner: inner, Cause: cause}
	if !errors.Is(wrapped, cause) {
		t.Fatal("expected errors.Is to find cause")
	}
}

func TestToolError_Error(t *testing.T) {
	e := &ToolError{ToolName: "read", Cause: fmt.Errorf("boom")}
	want := `mantyx: local tool "read" failed: boom`
	if e.Error() != want {
		t.Fatalf("got %q want %q", e.Error(), want)
	}
	if !errors.Is(e, e.Cause) {
		t.Fatal("unwrap cause")
	}
}

func TestRunError_Error(t *testing.T) {
	e := &RunError{RunID: "run_1", Code: "cancelled", Message: "user"}
	want := "mantyx: run run_1 failed (cancelled): user"
	if e.Error() != want {
		t.Fatalf("got %q", e.Error())
	}
	e2 := &RunError{RunID: "run_2", Message: "oops"}
	if e2.Error() != "mantyx: run run_2 failed: oops" {
		t.Fatalf("got %q", e2.Error())
	}
}

func TestRunError_CarriesOptionalTriageAttributes(t *testing.T) {
	retry := false
	e := &RunError{
		RunID:        "run_1",
		Code:         "truncation",
		Message:      "Model output was truncated.",
		ErrorClass:   "truncation",
		FinishReason: "max_tokens",
		PartialText:  `{"answer":"hi`,
		Retryable:    &retry,
	}
	if e.ErrorClass != "truncation" || e.FinishReason != "max_tokens" {
		t.Fatalf("expected triage attrs set: %+v", e)
	}
	if e.PartialText != `{"answer":"hi` {
		t.Fatalf("expected partial text: %q", e.PartialText)
	}
	if e.Retryable == nil || *e.Retryable != false {
		t.Fatalf("expected Retryable=&false, got %v", e.Retryable)
	}
}

func TestRunError_DefaultsTriageAttributesEmpty(t *testing.T) {
	e := &RunError{RunID: "run_2", Code: "error", Message: "boom"}
	if e.ErrorClass != "" || e.FinishReason != "" || e.PartialText != "" || e.Retryable != nil {
		t.Fatalf("expected triage attrs to default to zero values: %+v", e)
	}
}
