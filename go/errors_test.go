package mantyx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// ----- OAuth / credential resolution --------------------------------------

func TestNewClient_AcceptsAPIKey(t *testing.T) {
	c := NewClient(Options{APIKey: "mantyx_test", WorkspaceSlug: "demo"})
	if c.apiKey != "mantyx_test" {
		t.Fatalf("expected apiKey to be set, got %q", c.apiKey)
	}
}

func TestNewClient_AcceptsAccessToken(t *testing.T) {
	c := NewClient(Options{AccessToken: "mantyx_at_test", WorkspaceSlug: "demo"})
	if c.apiKey != "mantyx_at_test" {
		t.Fatalf("expected apiKey to be set from AccessToken, got %q", c.apiKey)
	}
}

func TestNewClient_PanicsWhenBothCredentialsSet(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when both APIKey and AccessToken are set")
		}
	}()
	NewClient(Options{APIKey: "mantyx_x", AccessToken: "mantyx_at_y", WorkspaceSlug: "demo"})
}

func TestNewClient_PanicsWhenAPIKeyAndTokenSourceBothSet(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID:     "mantyx_oa_test",
		ClientSecret: "mantyx_oas_secret",
		BaseURL:      srv.baseURL(),
	})
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when both APIKey and TokenSource are set")
		}
	}()
	NewClient(Options{
		APIKey:        "mantyx_x",
		TokenSource:   oauth.RefreshTokenSource(RefreshTokenSourceOptions{RefreshToken: "mantyx_rt_a"}),
		WorkspaceSlug: "demo",
	})
}

func TestNewClient_PanicsWhenNoCredential(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when neither APIKey nor AccessToken is set")
		}
	}()
	NewClient(Options{WorkspaceSlug: "demo"})
}

func TestClient_ShipsAccessTokenAsBearer(t *testing.T) {
	m := newMockServer()
	defer m.close()
	c := NewClient(Options{
		AccessToken:   "mantyx_at_oauth",
		WorkspaceSlug: "x",
		BaseURL:       m.baseURL(),
	})
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	m.mu.Lock()
	got := m.lastAuthHeader
	m.mu.Unlock()
	if got != "Bearer mantyx_at_oauth" {
		t.Fatalf("expected Bearer header carrying the OAuth token, got %q", got)
	}
}

func TestClient_SurfacesInsufficientScopeAsScopeError(t *testing.T) {
	m := newMockServer()
	defer m.close()
	m.mu.Lock()
	m.failScope = []string{"runs:write"}
	m.mu.Unlock()
	c := NewClient(Options{
		AccessToken:   "mantyx_at_oauth",
		WorkspaceSlug: "x",
		BaseURL:       m.baseURL(),
	})
	_, err := c.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected ScopeError, got nil")
	}
	var scopeErr *ScopeError
	if !errors.As(err, &scopeErr) {
		t.Fatalf("expected *ScopeError, got %T: %v", err, err)
	}
	if scopeErr.Inner.HTTPStatus != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", scopeErr.Inner.HTTPStatus)
	}
	if len(scopeErr.RequiredScopes) != 1 || scopeErr.RequiredScopes[0] != "runs:write" {
		t.Fatalf("expected RequiredScopes=[runs:write], got %v", scopeErr.RequiredScopes)
	}
}

func TestClient_ScopeError_MultiScope(t *testing.T) {
	m := newMockServer()
	defer m.close()
	m.mu.Lock()
	m.failScope = []string{"runs:read", "runs:write"}
	m.mu.Unlock()
	c := NewClient(Options{
		AccessToken:   "mantyx_at_oauth",
		WorkspaceSlug: "x",
		BaseURL:       m.baseURL(),
	})
	_, err := c.ListModels(context.Background())
	var scopeErr *ScopeError
	if !errors.As(err, &scopeErr) {
		t.Fatalf("expected *ScopeError, got %T: %v", err, err)
	}
	if len(scopeErr.RequiredScopes) != 2 ||
		scopeErr.RequiredScopes[0] != "runs:read" ||
		scopeErr.RequiredScopes[1] != "runs:write" {
		t.Fatalf("expected RequiredScopes=[runs:read runs:write], got %v", scopeErr.RequiredScopes)
	}
}
