package mantyx

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------- PKCE

func TestGeneratePKCEVerifier_LengthAndAlphabet(t *testing.T) {
	v, err := GeneratePKCEVerifier(64)
	if err != nil {
		t.Fatalf("GeneratePKCEVerifier: %v", err)
	}
	if l := len(v); l < 43 || l > 128 {
		t.Fatalf("length out of [43,128]: %d", l)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9\-._~]+$`).MatchString(v) {
		t.Fatalf("verifier uses non-unreserved chars: %q", v)
	}
}

func TestGeneratePKCEVerifier_RejectsOutOfRange(t *testing.T) {
	if _, err := GeneratePKCEVerifier(10); err == nil {
		t.Fatal("expected error for length=10")
	}
	if _, err := GeneratePKCEVerifier(200); err == nil {
		t.Fatal("expected error for length=200")
	}
}

func TestPKCEChallenge_RFC7636TestVector(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := PKCEChallenge(verifier); got != want {
		t.Fatalf("PKCEChallenge: got %q want %q", got, want)
	}
}

// ----------------------------------------------- ExchangeAuthorizationCode

func TestExchangeAuthorizationCode_HappyPath(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID:     "mantyx_oa_test",
		ClientSecret: "mantyx_oas_secret",
		BaseURL:      srv.baseURL(),
	})
	tok, err := oauth.ExchangeAuthorizationCode(context.Background(), ExchangeAuthorizationCodeOptions{
		Code:         "auth_code_123",
		RedirectURI:  "https://app.example.com/cb",
		CodeVerifier: "verifier_value",
	})
	if err != nil {
		t.Fatalf("ExchangeAuthorizationCode: %v", err)
	}
	if want := "mantyx_at_mock_initial_v1"; tok.AccessToken != want {
		t.Fatalf("AccessToken: got %q want %q", tok.AccessToken, want)
	}
	if tok.RefreshToken != "mantyx_rt_mock_initial" {
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
	if body.Get("grant_type") != "authorization_code" ||
		body.Get("code") != "auth_code_123" ||
		body.Get("redirect_uri") != "https://app.example.com/cb" ||
		body.Get("code_verifier") != "verifier_value" ||
		body.Get("client_id") != "mantyx_oa_test" ||
		body.Get("client_secret") != "mantyx_oas_secret" {
		t.Fatalf("form body mismatch: %v", body)
	}
}

func TestExchangeAuthorizationCode_InvalidGrant(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID:     "mantyx_oa_test",
		ClientSecret: "mantyx_oas_secret",
		BaseURL:      srv.baseURL(),
	})
	srv.oauthNextError = &oauthMockError{Error: "invalid_grant", Description: "code expired"}
	_, err := oauth.ExchangeAuthorizationCode(context.Background(), ExchangeAuthorizationCodeOptions{
		Code:         "bad",
		RedirectURI:  "https://app.example.com/cb",
		CodeVerifier: "v",
	})
	var oerr *OAuthError
	if !errors.As(err, &oerr) {
		t.Fatalf("expected *OAuthError, got %T (%v)", err, err)
	}
	if oerr.OAuthErrorCode != "invalid_grant" {
		t.Fatalf("OAuthErrorCode: %q", oerr.OAuthErrorCode)
	}
	if oerr.OAuthErrorDescription != "code expired" {
		t.Fatalf("OAuthErrorDescription: %q", oerr.OAuthErrorDescription)
	}
	if oerr.Inner.HTTPStatus != 400 {
		t.Fatalf("HTTPStatus: %d", oerr.Inner.HTTPStatus)
	}
}

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
	if srv.oauthLastTokenRequest.Get("refresh_token") != "mantyx_rt_alice" {
		t.Fatalf("server-side refresh_token: %q", srv.oauthLastTokenRequest.Get("refresh_token"))
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
	srv.oauthNextError = &oauthMockError{Error: "invalid_grant"}
	_, err := oauth.Refresh(context.Background(), RefreshOptions{RefreshToken: "mantyx_rt_revoked"})
	var oerr *OAuthError
	if !errors.As(err, &oerr) || oerr.OAuthErrorCode != "invalid_grant" {
		t.Fatalf("expected invalid_grant *OAuthError, got %v", err)
	}
}

// -------------------------------------------------------- ClientCredentials

func TestClientCredentials_NoRefreshIssued(t *testing.T) {
	srv := newMockServer()
	defer srv.close()
	oauth := NewOAuthClient(OAuthClientOptions{
		ClientID:     "mantyx_oa_test",
		ClientSecret: "mantyx_oas_secret",
		BaseURL:      srv.baseURL(),
	})
	tok, err := oauth.ClientCredentials(context.Background(), ClientCredentialsOptions{Scope: []string{"agents:invoke"}})
	if err != nil {
		t.Fatalf("ClientCredentials: %v", err)
	}
	if tok.RefreshToken != "" {
		t.Fatalf("expected empty RefreshToken, got %q", tok.RefreshToken)
	}
	if srv.oauthLastTokenRequest.Get("grant_type") != "client_credentials" {
		t.Fatalf("grant_type: %q", srv.oauthLastTokenRequest.Get("grant_type"))
	}
	if srv.oauthLastTokenRequest.Get("scope") != "agents:invoke" {
		t.Fatalf("scope: %q", srv.oauthLastTokenRequest.Get("scope"))
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
	seed, err := oauth.ExchangeAuthorizationCode(context.Background(), ExchangeAuthorizationCodeOptions{
		Code: "ac", RedirectURI: "https://app.example.com/cb", CodeVerifier: "v",
	})
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
