// MANTYX OAuth 2.0 refresh client: trade a stored refresh token for
// short-lived access tokens, revoke tokens at sign-out, and expose a
// TokenSource the Client HTTP layer calls before every request (and
// again on 401).
//
// The library is intentionally refresh-only. It assumes the caller
// already obtained the refresh token through their own sign-in flow
// (Authorization Code + PKCE in a browser, native redirect, server-
// side exchange — whatever fits the host application). The SDK does
// not drive consent, does not initiate auth-code exchanges, and does
// not bundle PKCE helpers.
//
// Wire contract (`docs/oauth.md`):
//
//   - Token endpoint: `POST <base>/api/oauth/token`, form-encoded,
//     `grant_type=refresh_token`. Echoes back the same `refresh_token`
//     the client sent (refresh tokens are persistent and non-rotating).
//   - Revoke endpoint: `POST <base>/api/oauth/revoke`, form-encoded.
//   - Access tokens (`mantyx_at_…`) live 1 hour.
//   - Refresh tokens (`mantyx_rt_…`) are long-lived; the caller persists
//     them once at first sign-in (encrypted at rest) and the SDK
//     re-mints access tokens from the same value on demand.

package mantyx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultOAuthBaseURL is the default origin for the MANTYX deployment. The
// OAuth endpoints are mounted at `<base>/api/oauth/...`.
const DefaultOAuthBaseURL = "https://app.mantyx.io"

// DefaultRefreshSkew is how long before Token.ExpiresAt a TokenSource will
// pre-emptively refresh.
const DefaultRefreshSkew = 60 * time.Second

// Token is the decoded `POST /api/oauth/token` response, augmented with an
// absolute ExpiresAt timestamp the SDK uses to decide when to refresh.
//
// On the `refresh_token` grant RefreshToken is identical to the value the
// client just sent — refresh tokens are persistent and non-rotating per
// `docs/oauth.md`. The field is surfaced for symmetry with whatever
// the calling app's sign-in flow already does.
type Token struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int
	// ExpiresAt is the absolute time at which AccessToken stops being
	// valid (computed at fetch time).
	ExpiresAt time.Time
	Scope     string
}

// TokenSource produces the current access token on demand.
//
// Client calls Token before every request and again with
// reason=ReasonUnauthorized after a 401 so the source can force a refresh
// rather than handing back its (now-invalid) cached value.
//
// Implementations must be safe for concurrent use; the SDK calls Token
// from arbitrary goroutines.
type TokenSource interface {
	Token(ctx context.Context, reason TokenRequestReason) (string, error)
}

// TokenRequestReason is why the SDK asked the TokenSource for the
// current access token.
type TokenRequestReason int

const (
	ReasonInitial TokenRequestReason = iota
	ReasonExpired
	ReasonUnauthorized
)

// OAuthClientOptions configures an OAuthClient.
type OAuthClientOptions struct {
	// ClientID is the OAuth `client_id` issued at app registration
	// (token prefix `mantyx_oa_`). Required.
	ClientID string
	// ClientSecret is the OAuth `client_secret` (token prefix
	// `mantyx_oas_`). Every MANTYX OAuth app is confidential, so this
	// is always required. Treat as a deployment secret. Required.
	ClientSecret string
	// BaseURL defaults to DefaultOAuthBaseURL when empty.
	BaseURL string
	// HTTPClient defaults to `&http.Client{Timeout: 30s}` when nil.
	HTTPClient *http.Client
}

// OAuthClient is a refresh-only wrapper around the MANTYX OAuth 2.0
// authorization-server endpoints (`POST /api/oauth/token` and
// `POST /api/oauth/revoke`). It is app-scoped (one per
// ClientID/ClientSecret pair); construct independently of Client,
// then either call its grant helpers directly or hand a TokenSource
// produced by RefreshTokenSource to NewClient via Options.TokenSource
// for fully transparent refresh on every request.
//
// The client deliberately does not drive the authorization-code
// exchange or any other "initiate sign-in" grant. The caller is
// expected to obtain the refresh token through their own consent
// flow and persist it before constructing this client.
type OAuthClient struct {
	ClientID     string
	clientSecret string
	BaseURL      string
	httpClient   *http.Client
}

// NewOAuthClient returns a configured OAuthClient.
func NewOAuthClient(opts OAuthClientOptions) *OAuthClient {
	if opts.ClientID == "" {
		panic("mantyx: OAuthClientOptions.ClientID is required")
	}
	if opts.ClientSecret == "" {
		panic("mantyx: OAuthClientOptions.ClientSecret is required")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultOAuthBaseURL
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &OAuthClient{
		ClientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		BaseURL:      strings.TrimRight(opts.BaseURL, "/"),
		httpClient:   opts.HTTPClient,
	}
}

// RefreshOptions are the arguments for (*OAuthClient).Refresh.
type RefreshOptions struct {
	RefreshToken string
	// Scope optionally narrows the access token's scope. Must be a
	// subset of the scopes already granted to the refresh token; the
	// server enforces this.
	Scope []string
}

// Refresh mints a fresh access token from a stored refresh token.
//
// The returned RefreshToken is identical to the input — refresh
// tokens are persistent and non-rotating, so the field is surfaced
// only for symmetry with the response shape. On `400 invalid_grant`
// the refresh token has been revoked (or its grant / app was
// deleted); the returned *OAuthError carries
// OAuthErrorCode == "invalid_grant" and callers must drive a fresh
// sign-in.
func (c *OAuthClient) Refresh(ctx context.Context, opts RefreshOptions) (*Token, error) {
	if opts.RefreshToken == "" {
		return nil, &Error{Message: "RefreshOptions.RefreshToken is required", Code: "invalid_request"}
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {opts.RefreshToken},
	}
	if scope := strings.TrimSpace(strings.Join(opts.Scope, " ")); scope != "" {
		form.Set("scope", scope)
	}
	return c.token(ctx, form)
}

// RevokeOptions are the arguments for (*OAuthClient).Revoke.
type RevokeOptions struct {
	Token string
}

// Revoke kills an access or refresh token (RFC 7009).
//
// The server always returns 200, even for unknown tokens. Revoking the
// refresh token kills the refresh and every live access token tied to
// its grant; revoking an access token kills only that one.
func (c *OAuthClient) Revoke(ctx context.Context, opts RevokeOptions) error {
	if opts.Token == "" {
		return &Error{Message: "RevokeOptions.Token is required", Code: "invalid_request"}
	}
	_, err := c.formPost(ctx, "/api/oauth/revoke", url.Values{"token": {opts.Token}})
	return err
}

// RefreshTokenSourceOptions configures a RefreshTokenSource.
type RefreshTokenSourceOptions struct {
	RefreshToken string
	// Scope is an optional scope narrowing applied on every refresh.
	Scope []string
	// RefreshSkew is how long before ExpiresAt the source proactively
	// refreshes. Defaults to DefaultRefreshSkew (60s).
	RefreshSkew time.Duration
	// InitialToken optionally seeds the source's cache with the access
	// token already in hand (e.g. straight out of the host
	// application's sign-in flow) to avoid an extra round-trip on the
	// first request.
	InitialToken *Token
}

// RefreshTokenSource builds a long-lived TokenSource that re-mints
// access tokens from the supplied refresh token. Pass the returned
// source to NewClient via Options.TokenSource.
//
// The source caches the access token in-memory and refreshes
// proactively when within RefreshSkew of ExpiresAt, or eagerly when
// Client reports a 401.
func (c *OAuthClient) RefreshTokenSource(opts RefreshTokenSourceOptions) TokenSource {
	if opts.RefreshToken == "" {
		panic("mantyx: RefreshTokenSourceOptions.RefreshToken is required")
	}
	skew := opts.RefreshSkew
	if skew <= 0 {
		skew = DefaultRefreshSkew
	}
	src := &cachingTokenSource{skew: skew, cached: opts.InitialToken}
	refreshToken := opts.RefreshToken
	scope := opts.Scope
	src.mint = func(ctx context.Context) (*Token, error) {
		return c.Refresh(ctx, RefreshOptions{RefreshToken: refreshToken, Scope: scope})
	}
	return src
}

// ----- internals ------------------------------------------------------------

func (c *OAuthClient) token(ctx context.Context, body url.Values) (*Token, error) {
	resp, err := c.formPost(ctx, "/api/oauth/token", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &NetworkError{Inner: &Error{Message: err.Error(), Code: "network"}, Cause: err}
	}
	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &OAuthError{
			Inner:                 &Error{Message: "OAuth token endpoint returned a non-JSON response", Code: "invalid_response", HTTPStatus: resp.StatusCode},
			OAuthErrorCode:        "invalid_response",
			OAuthErrorDescription: "Token endpoint returned a non-JSON response",
		}
	}
	if parsed.AccessToken == "" {
		return nil, &OAuthError{
			Inner:                 &Error{Message: "OAuth token endpoint response is missing `access_token`", Code: "invalid_response", HTTPStatus: resp.StatusCode},
			OAuthErrorCode:        "invalid_response",
			OAuthErrorDescription: "Token endpoint response is missing `access_token`",
		}
	}
	expiresIn := parsed.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	tokenType := parsed.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	return &Token{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		TokenType:    tokenType,
		ExpiresIn:    expiresIn,
		ExpiresAt:    time.Now().Add(time.Duration(expiresIn) * time.Second),
		Scope:        parsed.Scope,
	}, nil
}

func (c *OAuthClient) formPost(ctx context.Context, path string, body url.Values) (*http.Response, error) {
	// Always inject client_id + client_secret. RFC 6749 allows either
	// HTTP Basic auth or form-encoded credentials; MANTYX accepts both
	// but we ship the simpler form variant for consistency with the
	// other SDKs.
	body.Set("client_id", c.ClientID)
	body.Set("client_secret", c.clientSecret)
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+path, strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &NetworkError{Inner: &Error{Message: err.Error(), Code: "network"}, Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var errBody struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &errBody)
		oauthErr := errBody.Error
		if oauthErr == "" {
			oauthErr = fmt.Sprintf("http_%d", resp.StatusCode)
		}
		msg := "OAuth " + oauthErr
		if errBody.ErrorDescription != "" {
			msg = msg + ": " + errBody.ErrorDescription
		}
		return nil, &OAuthError{
			Inner:                 &Error{Message: msg, Code: oauthErr, HTTPStatus: resp.StatusCode},
			OAuthErrorCode:        oauthErr,
			OAuthErrorDescription: errBody.ErrorDescription,
		}
	}
	return resp, nil
}

// cachingTokenSource is the in-memory single-flight implementation
// behind RefreshTokenSource.
//
// A single goroutine wins the mu lock and mints; concurrent callers
// observe inflight != nil and wait on the same channel so only one
// `mint()` runs at a time. Single-flight is an efficiency, not a
// correctness requirement — `docs/oauth.md` explicitly allows
// concurrent refreshes against the same refresh token — but it keeps
// token-endpoint QPS reasonable under fan-out.
type cachingTokenSource struct {
	mu       sync.Mutex
	cached   *Token
	skew     time.Duration
	inflight chan struct{} // closed when current mint finishes
	result   *Token
	resErr   error
	mint     func(ctx context.Context) (*Token, error)
}

func (s *cachingTokenSource) Token(ctx context.Context, reason TokenRequestReason) (string, error) {
	s.mu.Lock()
	if reason != ReasonUnauthorized && s.cached != nil && !isExpiring(s.cached, s.skew) {
		access := s.cached.AccessToken
		s.mu.Unlock()
		return access, nil
	}
	if s.inflight != nil {
		ch := s.inflight
		s.mu.Unlock()
		select {
		case <-ch:
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.resErr != nil {
				return "", s.resErr
			}
			return s.result.AccessToken, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	ch := make(chan struct{})
	s.inflight = ch
	s.mu.Unlock()
	token, err := s.mint(ctx)
	s.mu.Lock()
	s.result = token
	s.resErr = err
	if err == nil {
		s.cached = token
	}
	close(ch)
	s.inflight = nil
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

func isExpiring(t *Token, skew time.Duration) bool {
	return time.Until(t.ExpiresAt) <= skew
}
