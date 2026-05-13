package mantyx

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------- Refresh

func TestRefresh_EchoesInputRefreshToken(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID:     "mantyx_oa_test",
		ClientSecret: "mantyx_oas_secret",
		BaseURL:      srv.baseURL(),
	})
	tok, err := oauth.Refresh(context.Background(), RefreshOptions{RefreshToken: "mantyx_rt_alice"})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if tok.RefreshToken != "mantyx_rt_alice" {
		t.Fatalf("RefreshToken: %q", tok.RefreshToken)
	}
	if tok.TokenType != "Bearer" {
		t.Fatalf("TokenType: %q", tok.TokenType)
	}
	if tok.ExpiresIn != 3600 {
		t.Fatalf("ExpiresIn: %d", tok.ExpiresIn)
	}
	if tok.ExpiresAt.Before(time.Now()) {
		t.Fatal("ExpiresAt is in the past")
	}
	body := srv.oauthLastTokenRequest
	if body.Get("grant_type") != "refresh_token" ||
		body.Get("refresh_token") != "mantyx_rt_alice" ||
		body.Get("client_id") != "mantyx_oa_test" ||
		body.Get("client_secret") != "mantyx_oas_secret" {
		t.Fatalf("form body mismatch: %v", body)
	}
}

func TestRefresh_NeverDriftsAcrossManyCalls(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID:     "mantyx_oa_test",
		ClientSecret: "mantyx_oas_secret",
		BaseURL:      srv.baseURL(),
	})
	for i := 0; i < 10; i++ {
		tok, err := oauth.Refresh(context.Background(), RefreshOptions{RefreshToken: "mantyx_rt_alice"})
		if err != nil {
			t.Fatalf("Refresh #%d: %v", i, err)
		}
		if tok.RefreshToken != "mantyx_rt_alice" {
			t.Fatalf("RefreshToken drifted at #%d: %q", i, tok.RefreshToken)
		}
	}
	if srv.oauthTokenCallCount != 10 {
		t.Fatalf("token call count: %d", srv.oauthTokenCallCount)
	}
}

func TestRefresh_ForwardsScopeNarrowing(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID:     "mantyx_oa_test",
		ClientSecret: "mantyx_oas_secret",
		BaseURL:      srv.baseURL(),
	})
	if _, err := oauth.Refresh(context.Background(), RefreshOptions{
		RefreshToken: "mantyx_rt_alice",
		Scope:        []string{"runs:write", "models:read"},
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got := srv.oauthLastTokenRequest.Get("scope"); got != "runs:write models:read" {
		t.Fatalf("scope: %q", got)
	}
}

func TestRefresh_InvalidGrant(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID:     "mantyx_oa_test",
		ClientSecret: "mantyx_oas_secret",
		BaseURL:      srv.baseURL(),
	})
	srv.oauthNextError = &oauthMockError{Error: "invalid_grant", Description: "refresh revoked"}
	_, err := oauth.Refresh(context.Background(), RefreshOptions{RefreshToken: "mantyx_rt_revoked"})
	var oerr *OAuthError
	if !errors.As(err, &oerr) || oerr.OAuthErrorCode != "invalid_grant" {
		t.Fatalf("expected invalid_grant *OAuthError, got %v", err)
	}
	if oerr.OAuthErrorDescription != "refresh revoked" {
		t.Fatalf("OAuthErrorDescription: %q", oerr.OAuthErrorDescription)
	}
	if oerr.Inner.HTTPStatus != 400 {
		t.Fatalf("HTTPStatus: %d", oerr.Inner.HTTPStatus)
	}
}

// ------------------------------------------------------------------- Revoke

func TestRevoke_PostsFormVerbatim(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID:     "mantyx_oa_test",
		ClientSecret: "mantyx_oas_secret",
		BaseURL:      srv.baseURL(),
	})
	if err := oauth.Revoke(context.Background(), RevokeOptions{Token: "mantyx_rt_to_kill"}); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if srv.oauthRevokeCallCount != 1 {
		t.Fatalf("revoke call count: %d", srv.oauthRevokeCallCount)
	}
	body := srv.oauthLastRevokeRequest
	if body.Get("token") != "mantyx_rt_to_kill" ||
		body.Get("client_id") != "mantyx_oa_test" ||
		body.Get("client_secret") != "mantyx_oas_secret" {
		t.Fatalf("revoke form body: %v", body)
	}
}

// ----------------------------------------------- Client + RefreshTokenSource

func TestClient_TokenSource_MintsOnceAndReuses(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID: "mantyx_oa_test", ClientSecret: "mantyx_oas_secret", BaseURL: srv.baseURL(),
	})
	source := oauth.RefreshTokenSource(RefreshTokenSourceOptions{RefreshToken: "mantyx_rt_alice"})
	c := NewClient(Options{TokenSource: source, WorkspaceSlug: "demo", BaseURL: srv.baseURL()})
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels #1: %v", err)
	}
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels #2: %v", err)
	}
	if srv.oauthTokenCallCount != 1 {
		t.Fatalf("token call count: %d", srv.oauthTokenCallCount)
	}
	apiAuth := filterAPIAuth(srv.authHeaderHistory)
	if len(apiAuth) != 2 || apiAuth[0] != apiAuth[1] {
		t.Fatalf("auth headers: %v", apiAuth)
	}
}

func TestClient_TokenSource_RefreshesProactively(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID: "mantyx_oa_test", ClientSecret: "mantyx_oas_secret", BaseURL: srv.baseURL(),
	})
	source := oauth.RefreshTokenSource(RefreshTokenSourceOptions{
		RefreshToken: "mantyx_rt_alice",
		RefreshSkew:  10_000_000 * time.Second, // huge skew → every check expiring
	})
	c := NewClient(Options{TokenSource: source, WorkspaceSlug: "demo", BaseURL: srv.baseURL()})
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels #1: %v", err)
	}
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels #2: %v", err)
	}
	if srv.oauthTokenCallCount != 2 {
		t.Fatalf("token call count: %d", srv.oauthTokenCallCount)
	}
}

func TestClient_TokenSource_401TriggersRefreshAndRetry(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID: "mantyx_oa_test", ClientSecret: "mantyx_oas_secret", BaseURL: srv.baseURL(),
	})
	source := oauth.RefreshTokenSource(RefreshTokenSourceOptions{RefreshToken: "mantyx_rt_alice"})
	c := NewClient(Options{TokenSource: source, WorkspaceSlug: "demo", BaseURL: srv.baseURL()})
	srv.failAuthCount = 1
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if srv.oauthTokenCallCount != 2 {
		t.Fatalf("token call count: %d (want 2)", srv.oauthTokenCallCount)
	}
	apiAuth := filterAPIAuth(srv.authHeaderHistory)
	if len(apiAuth) != 2 || apiAuth[0] == apiAuth[1] {
		t.Fatalf("expected two distinct bearers, got %v", apiAuth)
	}
}

func TestClient_TokenSource_Second401IsAuthError(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID: "mantyx_oa_test", ClientSecret: "mantyx_oas_secret", BaseURL: srv.baseURL(),
	})
	source := oauth.RefreshTokenSource(RefreshTokenSourceOptions{RefreshToken: "mantyx_rt_alice"})
	c := NewClient(Options{TokenSource: source, WorkspaceSlug: "demo", BaseURL: srv.baseURL()})
	srv.failAuthCount = 5
	_, err := c.ListModels(context.Background())
	var aerr *AuthError
	if !errors.As(err, &aerr) {
		t.Fatalf("expected *AuthError, got %T (%v)", err, err)
	}
}

func TestClient_TokenSource_ScopeErrorIsNotRetried(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID: "mantyx_oa_test", ClientSecret: "mantyx_oas_secret", BaseURL: srv.baseURL(),
	})
	source := oauth.RefreshTokenSource(RefreshTokenSourceOptions{RefreshToken: "mantyx_rt_alice"})
	c := NewClient(Options{TokenSource: source, WorkspaceSlug: "demo", BaseURL: srv.baseURL()})
	srv.failScope = []string{"runs:write"}
	_, err := c.ListModels(context.Background())
	var serr *ScopeError
	if !errors.As(err, &serr) {
		t.Fatalf("expected *ScopeError, got %T (%v)", err, err)
	}
	// Only the initial mint — no extra refresh after the scope failure.
	if srv.oauthTokenCallCount != 1 {
		t.Fatalf("token call count: %d (want 1)", srv.oauthTokenCallCount)
	}
}

func TestClient_TokenSource_SingleFlight(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID: "mantyx_oa_test", ClientSecret: "mantyx_oas_secret", BaseURL: srv.baseURL(),
	})
	source := oauth.RefreshTokenSource(RefreshTokenSourceOptions{
		RefreshToken: "mantyx_rt_alice",
		RefreshSkew:  10_000_000 * time.Second,
	})
	c := NewClient(Options{TokenSource: source, WorkspaceSlug: "demo", BaseURL: srv.baseURL()})
	// Gate the first /token call until all goroutines are queued up.
	gate := make(chan struct{})
	var hookCount atomic.Int32
	srv.oauthTokenHook = func() {
		if hookCount.Add(1) == 1 {
			<-gate
		}
	}
	var wg sync.WaitGroup
	wg.Add(8)
	for i := 0; i < 8; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.ListModels(context.Background())
		}()
	}
	// Give the 8 goroutines a chance to all hit the source.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if got := srv.oauthTokenCallCount; got != 1 {
		t.Fatalf("token call count: %d (want 1 — single-flight collapse)", got)
	}
	if got := len(filterAPIAuth(srv.authHeaderHistory)); got != 8 {
		t.Fatalf("api auth headers: %d (want 8)", got)
	}
}

func TestClient_TokenSource_SeedsCacheFromInitialToken(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID: "mantyx_oa_test", ClientSecret: "mantyx_oas_secret", BaseURL: srv.baseURL(),
	})
	seed, err := oauth.Refresh(context.Background(), RefreshOptions{RefreshToken: "mantyx_rt_alice"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	baseline := srv.oauthTokenCallCount
	source := oauth.RefreshTokenSource(RefreshTokenSourceOptions{
		RefreshToken: seed.RefreshToken,
		InitialToken: seed,
	})
	c := NewClient(Options{TokenSource: source, WorkspaceSlug: "demo", BaseURL: srv.baseURL()})
	if _, err := c.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if srv.oauthTokenCallCount != baseline {
		t.Fatalf("token call count: %d (want %d — seed should skip refresh)", srv.oauthTokenCallCount, baseline)
	}
}

// ---------------------------------------------------------------- helpers

// filterAPIAuth drops Authorization headers that don't belong to /api/v1
// requests (i.e. drops headers from /api/oauth/* exchanges, which use
// only the form-encoded client_id/client_secret).
func filterAPIAuth(history []string) []string {
	out := make([]string, 0, len(history))
	for _, h := range history {
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}
