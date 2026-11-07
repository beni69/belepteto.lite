package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	defaultSSOLoginButtonText = "Bejelentkezés SSO-val"
	oidcStateCookieName       = "belepteto_oidc_state"
	oidcStateTTL              = 10 * time.Minute
	maxPendingOIDCLogins      = 4096
)

type ssoConfig struct {
	issuerURL       string
	wellKnownURL    string
	clientID        string
	clientSecret    string
	scopes          string
	redirectURL     string
	frontendURL     string
	loginButtonText string
}

func (c ssoConfig) anyCoreSetting() bool {
	return c.issuerURL != "" || c.wellKnownURL != "" || c.clientID != "" ||
		c.clientSecret != "" || c.scopes != "" || c.redirectURL != "" || c.frontendURL != ""
}

func (c ssoConfig) buttonText() string {
	if text := strings.TrimSpace(c.loginButtonText); text != "" {
		return text
	}
	return defaultSSOLoginButtonText
}

func (c ssoConfig) trimmed() ssoConfig {
	c.issuerURL = strings.TrimSpace(c.issuerURL)
	c.wellKnownURL = strings.TrimSpace(c.wellKnownURL)
	c.clientID = strings.TrimSpace(c.clientID)
	c.clientSecret = strings.TrimSpace(c.clientSecret)
	c.scopes = strings.TrimSpace(c.scopes)
	c.redirectURL = strings.TrimSpace(c.redirectURL)
	c.frontendURL = strings.TrimSpace(c.frontendURL)
	return c
}

type pendingOIDCLogin struct {
	nonce     string
	verifier  string
	expiresAt time.Time
}

type oidcAuthenticator struct {
	issuer        string
	oauth         oauth2.Config
	provider      *oidc.Provider
	verifier      *oidc.IDTokenVerifier
	httpClient    *http.Client
	callbackPath  string
	frontendRoot  string
	buttonText    string
	secureCookies bool

	mu      sync.Mutex
	pending map[string]pendingOIDCLogin
}

// newOIDCAuthenticator returns nil when SSO has not been configured. A partial
// configuration is an error so a typo cannot silently disable the login button.
func newOIDCAuthenticator(ctx context.Context, cfg ssoConfig, client *http.Client) (*oidcAuthenticator, error) {
	cfg = cfg.trimmed()
	if !cfg.anyCoreSetting() {
		return nil, nil
	}

	missing := make([]string, 0)
	if cfg.issuerURL == "" && cfg.wellKnownURL == "" {
		missing = append(missing, "ISSUER_URL or WELL_KNOWN_URL")
	}
	if cfg.clientID == "" {
		missing = append(missing, "CLIENT_ID")
	}
	if cfg.clientSecret == "" {
		missing = append(missing, "CLIENT_SECRET")
	}
	if strings.TrimSpace(cfg.scopes) == "" {
		missing = append(missing, "SCOPES")
	}
	if cfg.redirectURL == "" {
		missing = append(missing, "SSO_REDIRECT_URL")
	}
	if cfg.frontendURL == "" {
		missing = append(missing, "SSO_FRONTEND_URL")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("incomplete configuration; missing %s", strings.Join(missing, ", "))
	}

	scopes := strings.Fields(cfg.scopes)
	if !containsString(scopes, "openid") || !containsString(scopes, "email") {
		return nil, fmt.Errorf("SCOPES must include both openid and email")
	}

	redirectURL, err := parsePublicURL(cfg.redirectURL, "SSO_REDIRECT_URL")
	if err != nil {
		return nil, err
	}
	if redirectURL.RawQuery != "" || redirectURL.Fragment != "" || redirectURL.Path == "" || redirectURL.Path == "/" {
		return nil, fmt.Errorf("SSO_REDIRECT_URL must have a callback path and no query or fragment")
	}
	callbackPath := redirectURL.Path
	if strings.ContainsAny(callbackPath, "{}*") || reservedCallbackPath(callbackPath) {
		return nil, fmt.Errorf("SSO_REDIRECT_URL has a path that conflicts with an application route")
	}

	frontendURL, err := parsePublicURL(cfg.frontendURL, "SSO_FRONTEND_URL")
	if err != nil {
		return nil, err
	}
	if frontendURL.RawQuery != "" || frontendURL.Fragment != "" {
		return nil, fmt.Errorf("SSO_FRONTEND_URL must not contain a query or fragment")
	}
	if frontendURL.Path != "" && frontendURL.Path != "/" {
		return nil, fmt.Errorf("SSO_FRONTEND_URL must be an origin, without a path")
	}
	if origin(redirectURL) != origin(frontendURL) {
		return nil, fmt.Errorf("SSO_REDIRECT_URL and SSO_FRONTEND_URL must use the same origin for the session cookie")
	}

	wellKnownURL := cfg.wellKnownURL
	if wellKnownURL == "" {
		issuerURL, err := parsePublicURL(cfg.issuerURL, "ISSUER_URL")
		if err != nil {
			return nil, err
		}
		if issuerURL.RawQuery != "" || issuerURL.Fragment != "" {
			return nil, fmt.Errorf("ISSUER_URL must not contain a query or fragment")
		}
		wellKnownURL = strings.TrimRight(cfg.issuerURL, "/") + "/.well-known/openid-configuration"
	}
	discoveryURL, err := parsePublicURL(wellKnownURL, "WELL_KNOWN_URL")
	if err != nil {
		return nil, err
	}

	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	ctx = oidc.ClientContext(ctx, client)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create OIDC discovery request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch OIDC discovery document: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch OIDC discovery document: unexpected status %s", response.Status)
	}
	var metadata oidc.ProviderConfig
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&metadata); err != nil {
		return nil, fmt.Errorf("decode OIDC discovery document: %w", err)
	}
	if metadata.IssuerURL == "" {
		return nil, fmt.Errorf("OIDC discovery document has no issuer")
	}
	if cfg.issuerURL != "" && cfg.issuerURL != metadata.IssuerURL {
		return nil, fmt.Errorf("OIDC discovery issuer does not match ISSUER_URL")
	}
	issuerURL, err := parsePublicURL(metadata.IssuerURL, "discovered issuer")
	if err != nil {
		return nil, err
	}
	if len(metadata.Algorithms) > 0 && !containsString(metadata.Algorithms, "RS256") {
		return nil, fmt.Errorf("OIDC provider does not advertise RS256 ID-token signing")
	}
	allowLoopbackHTTP := strings.EqualFold(issuerURL.Scheme, "http") && isLoopbackHost(issuerURL.Hostname())
	if strings.EqualFold(discoveryURL.Scheme, "http") && !allowLoopbackHTTP {
		return nil, fmt.Errorf("WELL_KNOWN_URL must use HTTPS for this OIDC issuer")
	}
	for label, endpoint := range map[string]string{
		"authorization_endpoint": metadata.AuthURL,
		"token_endpoint":         metadata.TokenURL,
		"jwks_uri":               metadata.JWKSURL,
	} {
		if endpoint == "" {
			return nil, fmt.Errorf("OIDC discovery document has no %s", label)
		}
		parsed, err := parsePublicURL(endpoint, label)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(parsed.Scheme, "http") && !allowLoopbackHTTP {
			return nil, fmt.Errorf("%s must use HTTPS for this OIDC issuer", label)
		}
	}
	if metadata.UserInfoURL != "" {
		parsed, err := parsePublicURL(metadata.UserInfoURL, "userinfo_endpoint")
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(parsed.Scheme, "http") && !allowLoopbackHTTP {
			return nil, fmt.Errorf("userinfo_endpoint must use HTTPS for this OIDC issuer")
		}
	}

	provider := metadata.NewProvider(ctx)
	verifier := provider.VerifierContext(ctx, &oidc.Config{
		ClientID:             cfg.clientID,
		SupportedSigningAlgs: []string{"RS256"},
	})

	frontendRoot := frontendURL.Scheme + "://" + frontendURL.Host + "/"
	return &oidcAuthenticator{
		issuer:        metadata.IssuerURL,
		oauth:         oauth2.Config{ClientID: cfg.clientID, ClientSecret: cfg.clientSecret, RedirectURL: cfg.redirectURL, Scopes: scopes, Endpoint: provider.Endpoint()},
		provider:      provider,
		verifier:      verifier,
		httpClient:    client,
		callbackPath:  callbackPath,
		frontendRoot:  frontendRoot,
		buttonText:    cfg.buttonText(),
		secureCookies: strings.EqualFold(redirectURL.Scheme, "https"),
		pending:       make(map[string]pendingOIDCLogin),
	}, nil
}

func parsePublicURL(raw, name string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("%s must be an absolute URL without user information", name)
	}
	if !strings.EqualFold(u.Scheme, "https") && !(strings.EqualFold(u.Scheme, "http") && isLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("%s must use HTTPS (HTTP is allowed only for loopback hosts)", name)
	}
	return u, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func origin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

func reservedCallbackPath(path string) bool {
	if path == "/" || path == "/healthz" || path == "/login" || path == "/logout" ||
		path == "/admin" || path == "/sso/login" || path == "/htmx" || path == "/static" ||
		path == "/manifest.webmanifest" {
		return true
	}
	return strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/htmx/")
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func ssoButtonText(sso *oidcAuthenticator) string {
	if sso == nil {
		return defaultSSOLoginButtonText
	}
	return sso.buttonText
}

func randomOIDCString(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (a *oidcAuthenticator) beginLogin(w http.ResponseWriter) (string, error) {
	state, err := randomOIDCString(32)
	if err != nil {
		return "", fmt.Errorf("generate OIDC state: %w", err)
	}
	nonce, err := randomOIDCString(32)
	if err != nil {
		return "", fmt.Errorf("generate OIDC nonce: %w", err)
	}
	verifier := oauth2.GenerateVerifier()
	now := time.Now()

	a.mu.Lock()
	for oldState, pending := range a.pending {
		if now.After(pending.expiresAt) {
			delete(a.pending, oldState)
		}
	}
	if len(a.pending) >= maxPendingOIDCLogins {
		a.mu.Unlock()
		return "", errors.New("too many pending OIDC logins")
	}
	a.pending[state] = pendingOIDCLogin{nonce: nonce, verifier: verifier, expiresAt: now.Add(oidcStateTTL)}
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  now.Add(oidcStateTTL),
		MaxAge:   int(oidcStateTTL.Seconds()),
	})
	return a.oauth.AuthCodeURL(
		state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	), nil
}

func (a *oidcAuthenticator) consumeLogin(state, cookieState string) (pendingOIDCLogin, bool) {
	if state == "" || cookieState == "" || subtle.ConstantTimeCompare([]byte(state), []byte(cookieState)) != 1 {
		return pendingOIDCLogin{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	pending, ok := a.pending[state]
	delete(a.pending, state)
	if !ok || time.Now().After(pending.expiresAt) {
		return pendingOIDCLogin{}, false
	}
	return pending, true
}

func (a *oidcAuthenticator) verifiedEmail(ctx context.Context, token *oauth2.Token, idToken *oidc.IDToken) (string, error) {
	var idClaims struct {
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
	}
	if err := idToken.Claims(&idClaims); err != nil {
		return "", fmt.Errorf("read ID token claims: %w", err)
	}
	idEmail := strings.TrimSpace(idClaims.Email)
	if idClaims.EmailVerified != nil && !*idClaims.EmailVerified {
		return "", errors.New("provider did not verify the email claim")
	}
	if idEmail != "" && idClaims.EmailVerified != nil && *idClaims.EmailVerified {
		return idEmail, nil
	}
	if a.provider.UserInfoEndpoint() == "" {
		return "", errors.New("provider did not return a verified email claim")
	}

	userinfo, err := a.provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err != nil {
		return "", fmt.Errorf("fetch OIDC userinfo: %w", err)
	}
	if userinfo.Subject != idToken.Subject {
		return "", errors.New("userinfo subject does not match ID token subject")
	}
	email := strings.TrimSpace(userinfo.Email)
	if email == "" || !userinfo.EmailVerified {
		return "", errors.New("provider did not return a verified email claim")
	}
	if idEmail != "" && !strings.EqualFold(idEmail, email) {
		return "", errors.New("ID token and userinfo email claims do not match")
	}
	return email, nil
}

func (s *server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.currentUser(r) != nil {
		http.Redirect(w, r, s.sso.frontendRoot, http.StatusSeeOther)
		return
	}
	authorizationURL, err := s.sso.beginLogin(w)
	if err != nil {
		s.logger.Error("start OIDC login", "err", err)
		http.Error(w, "could not start SSO login", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, authorizationURL, http.StatusFound)
}

func (s *server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, cookieErr := r.Cookie(oidcStateCookieName)
	clearOIDCStateCookie(w, s.sso.secureCookies)
	if cookieErr != nil {
		s.rejectOIDCLogin(w, r, errors.New("missing state cookie"))
		return
	}
	state := r.URL.Query().Get("state")
	pending, ok := s.sso.consumeLogin(state, cookie.Value)
	if !ok {
		s.rejectOIDCLogin(w, r, errors.New("invalid or expired state"))
		return
	}
	if r.URL.Query().Get("error") != "" {
		s.rejectOIDCLogin(w, r, errors.New("provider returned an authorization error"))
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		s.rejectOIDCLogin(w, r, errors.New("authorization response has no code"))
		return
	}

	ctx := oidc.ClientContext(r.Context(), s.sso.httpClient)
	token, err := s.sso.oauth.Exchange(ctx, code, oauth2.VerifierOption(pending.verifier))
	if err != nil {
		s.rejectOIDCLogin(w, r, fmt.Errorf("exchange authorization code: %w", err))
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		s.rejectOIDCLogin(w, r, errors.New("token response has no ID token"))
		return
	}
	idToken, err := s.sso.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		s.rejectOIDCLogin(w, r, fmt.Errorf("verify ID token: %w", err))
		return
	}
	if idToken.Nonce != pending.nonce {
		s.rejectOIDCLogin(w, r, errors.New("ID token nonce mismatch"))
		return
	}
	if idToken.Subject == "" {
		s.rejectOIDCLogin(w, r, errors.New("ID token has no subject"))
		return
	}

	user, err := s.store.UserByOIDCIdentity(s.sso.issuer, idToken.Subject)
	if errors.Is(err, ErrNotFound) {
		var email string
		email, err = s.sso.verifiedEmail(ctx, token, idToken)
		if err == nil {
			user, err = s.store.LinkOIDCIdentity(s.sso.issuer, idToken.Subject, email)
		}
	}
	if err != nil || user == nil || !user.Active {
		if err == nil {
			err = ErrNotFound
		}
		s.rejectOIDCLogin(w, r, fmt.Errorf("local account lookup: %w", err))
		return
	}
	if err := s.startSession(w, r, user.ID); err != nil {
		s.logger.Error("start SSO session", "err", err)
		http.Error(w, "could not complete SSO login", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, s.sso.frontendRoot, http.StatusSeeOther)
}

func clearOIDCStateCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *server) rejectOIDCLogin(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Warn("OIDC login rejected", "err", err)
	http.Redirect(w, r, "/login?error=sso", http.StatusSeeOther)
}
