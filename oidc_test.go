package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestOIDCConfigurationDisabledAndValidated(t *testing.T) {
	got, err := newOIDCAuthenticator(context.Background(), ssoConfig{}, nil)
	if err != nil || got != nil {
		t.Fatalf("empty SSO configuration = (%v, %v), want (nil, nil)", got, err)
	}

	partial := ssoConfig{clientID: "client"}
	if _, err := newOIDCAuthenticator(context.Background(), partial, nil); err == nil {
		t.Fatal("partial SSO configuration should be rejected")
	}
	missingClaimsScope := ssoConfig{
		wellKnownURL: "http://localhost/discovery",
		clientID:     "client",
		clientSecret: "secret",
		scopes:       "openid profile",
		redirectURL:  "http://localhost/callback",
		frontendURL:  "http://localhost",
	}
	if _, err := newOIDCAuthenticator(context.Background(), missingClaimsScope, nil); err == nil || !strings.Contains(err.Error(), "openid and email") {
		t.Fatalf("missing email scope error = %v, want an openid/email scope validation error", err)
	}

	if got := (ssoConfig{}).buttonText(); got != defaultSSOLoginButtonText {
		t.Errorf("default button text = %q, want %q", got, defaultSSOLoginButtonText)
	}
	if got := (ssoConfig{loginButtonText: "Sign in"}).buttonText(); got != "Sign in" {
		t.Errorf("configured button text = %q, want %q", got, "Sign in")
	}
}

func TestOIDCIssuerURLDiscoveryAndIssuerValidation(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	config := ssoConfig{
		issuerURL:    provider.server.URL,
		clientID:     "test-client",
		clientSecret: "test-secret",
		scopes:       "openid email",
		redirectURL:  provider.server.URL + "/callback",
		frontendURL:  provider.server.URL,
	}
	got, err := newOIDCAuthenticator(context.Background(), config, provider.server.Client())
	if err != nil {
		t.Fatalf("issuer discovery: %v", err)
	}
	if got.issuer != provider.server.URL {
		t.Errorf("discovered issuer = %q, want %q", got.issuer, provider.server.URL)
	}

	config.wellKnownURL = provider.server.URL + "/.well-known/openid-configuration"
	config.issuerURL += "/wrong"
	if _, err := newOIDCAuthenticator(context.Background(), config, provider.server.Client()); err == nil {
		t.Fatal("expected configured and discovered issuers to be compared")
	}
}

func TestOIDCLinksExistingUserAndReusesSubject(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	srv, store := newOIDCTestServer(t, provider, "")
	seedUser(t, store, "person@example.com", "local-password", false)
	provider.setIdentity(fakeOIDCIdentity{subject: "stable-subject", email: "person@example.com", verified: true})

	client := oidcLoginClient(t, srv)
	resp, err := client.Get(srv.URL + "/sso/login")
	if err != nil {
		t.Fatalf("OIDC login: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "My cards") {
		t.Fatalf("OIDC login ended with status %d and body %q", resp.StatusCode, body)
	}

	user, err := store.UserByEmail("person@example.com")
	if err != nil {
		t.Fatalf("load linked user: %v", err)
	}
	if user.OIDCIssuer == nil || *user.OIDCIssuer != provider.server.URL {
		t.Errorf("stored issuer = %v, want %q", user.OIDCIssuer, provider.server.URL)
	}
	if user.OIDCSubject == nil || *user.OIDCSubject != "stable-subject" {
		t.Errorf("stored sub = %v, want %q", user.OIDCSubject, "stable-subject")
	}

	// A separate browser proves the next login resolves by issuer+sub rather than
	// requiring another email-based link.
	client = oidcLoginClient(t, srv)
	resp, err = client.Get(srv.URL + "/sso/login")
	if err != nil {
		t.Fatalf("repeat OIDC login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("repeat OIDC login status = %d, want 200", resp.StatusCode)
	}
}

func TestOIDCDoesNotAllowLinkedInactiveUser(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	srv, store := newOIDCTestServer(t, provider, "")
	user := seedUser(t, store, "person@example.com", "pw", false)
	provider.setIdentity(fakeOIDCIdentity{subject: "stable-subject", email: "person@example.com", verified: true})

	resp, err := oidcLoginClient(t, srv).Get(srv.URL + "/sso/login")
	if err != nil {
		t.Fatalf("first OIDC login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first login status = %d, want 200", resp.StatusCode)
	}
	if err := store.SetUserActive(user.ID, false); err != nil {
		t.Fatalf("deactivate linked user: %v", err)
	}

	resp, err = oidcLoginClient(t, srv).Get(srv.URL + "/sso/login")
	if err != nil {
		t.Fatalf("second OIDC login: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "SSO sign-in failed") {
		t.Fatalf("inactive linked user should be rejected; got %d %q", resp.StatusCode, body)
	}
}

func TestOIDCButtonUsesFallbackAndCanBeCustomized(t *testing.T) {
	for _, tc := range []struct {
		name       string
		buttonText string
		want       string
	}{
		{name: "fallback", want: defaultSSOLoginButtonText},
		{name: "custom", buttonText: "Continue with SSO", want: "Continue with SSO"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newFakeOIDCProvider(t)
			srv, _ := newOIDCTestServer(t, provider, tc.buttonText)
			resp, err := http.Get(srv.URL + "/login")
			if err != nil {
				t.Fatalf("GET /login: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), tc.want) {
				t.Fatalf("login page status/body missing button text %q: %d %s", tc.want, resp.StatusCode, body)
			}
			if !strings.Contains(string(body), `href="/sso/login"`) {
				t.Errorf("login page has no SSO link: %s", body)
			}
		})
	}
}

func TestOIDCUsesVerifiedUserInfoEmailWhenIDTokenOmitsIt(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	srv, store := newOIDCTestServer(t, provider, "")
	seedUser(t, store, "person@example.com", "pw", false)
	provider.setIdentity(fakeOIDCIdentity{
		subject:   "userinfo-subject",
		email:     "person@example.com",
		verified:  true,
		omitEmail: true,
	})

	resp, err := oidcLoginClient(t, srv).Get(srv.URL + "/sso/login")
	if err != nil {
		t.Fatalf("OIDC login: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "My cards") {
		t.Fatalf("userinfo email login failed: status=%d body=%s", resp.StatusCode, body)
	}
	user, err := store.UserByEmail("person@example.com")
	if err != nil || user.OIDCSubject == nil || *user.OIDCSubject != "userinfo-subject" {
		t.Fatalf("userinfo identity was not linked: user=%+v error=%v", user, err)
	}
}

func TestOIDCRejectsStateMismatch(t *testing.T) {
	provider := newFakeOIDCProvider(t)
	srv, _ := newOIDCTestServer(t, provider, "")
	client := oidcLoginClient(t, srv)
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(srv.URL + "/sso/login")
	if err != nil {
		t.Fatalf("start OIDC login: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("start login status = %d, want 302", resp.StatusCode)
	}

	resp, err = client.Get(srv.URL + "/oidc/callback?state=wrong-state&code=unused")
	if err != nil {
		t.Fatalf("callback with bad state: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login?error=sso" {
		t.Fatalf("bad state response = %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestOIDCRejectsUnknownInactiveAndUnverifiedUsers(t *testing.T) {
	tests := []struct {
		name     string
		identity fakeOIDCIdentity
		seed     bool
		active   bool
	}{
		{
			name:     "unknown local email",
			identity: fakeOIDCIdentity{subject: "unknown", email: "unknown@example.com", verified: true},
		},
		{
			name:     "inactive local user",
			identity: fakeOIDCIdentity{subject: "inactive", email: "person@example.com", verified: true},
			seed:     true,
		},
		{
			name:     "email is not verified",
			identity: fakeOIDCIdentity{subject: "unverified", email: "person@example.com", verified: false},
			seed:     true,
			active:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider := newFakeOIDCProvider(t)
			srv, store := newOIDCTestServer(t, provider, "")
			var user *User
			if tc.seed {
				user = seedUser(t, store, "person@example.com", "pw", false)
				if !tc.active {
					if err := store.SetUserActive(user.ID, false); err != nil {
						t.Fatalf("deactivate: %v", err)
					}
				}
			}
			provider.setIdentity(tc.identity)

			client := oidcLoginClient(t, srv)
			resp, err := client.Get(srv.URL + "/sso/login")
			if err != nil {
				t.Fatalf("OIDC login: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "SSO sign-in failed") {
				t.Fatalf("login should be rejected; got %d %q", resp.StatusCode, body)
			}
			if tc.seed {
				stored, err := store.UserByEmail("person@example.com")
				if err != nil {
					t.Fatalf("reload user: %v", err)
				}
				if stored.OIDCSubject != nil {
					t.Errorf("rejected user was linked to subject %q", *stored.OIDCSubject)
				}
			}
		})
	}
}

func TestOIDCRejectsBadNonceAndNonRS256(t *testing.T) {
	for _, tc := range []struct {
		name     string
		identity fakeOIDCIdentity
	}{
		{name: "wrong nonce", identity: fakeOIDCIdentity{subject: "sub", email: "person@example.com", verified: true, wrongNonce: true}},
		{name: "wrong algorithm", identity: fakeOIDCIdentity{subject: "sub", email: "person@example.com", verified: true, algorithm: "HS256"}},
		{name: "wrong issuer", identity: fakeOIDCIdentity{subject: "sub", email: "person@example.com", verified: true, wrongIssuer: true}},
		{name: "wrong audience", identity: fakeOIDCIdentity{subject: "sub", email: "person@example.com", verified: true, wrongAudience: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := newFakeOIDCProvider(t)
			srv, store := newOIDCTestServer(t, provider, "")
			seedUser(t, store, "person@example.com", "pw", false)
			provider.setIdentity(tc.identity)

			resp, err := oidcLoginClient(t, srv).Get(srv.URL + "/sso/login")
			if err != nil {
				t.Fatalf("OIDC login: %v", err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "SSO sign-in failed") {
				t.Fatalf("invalid token should be rejected; got %d %q", resp.StatusCode, body)
			}
		})
	}
}

func oidcLoginClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	return &http.Client{Jar: jar}
}

type fakeOIDCIdentity struct {
	subject       string
	email         string
	verified      bool
	wrongNonce    bool
	algorithm     string
	omitEmail     bool
	wrongIssuer   bool
	wrongAudience bool
}

type fakeOIDCLogin struct {
	nonce     string
	challenge string
	identity  fakeOIDCIdentity
}

type fakeOIDCProvider struct {
	server     *httptest.Server
	privateKey *rsa.PrivateKey
	mu         sync.Mutex
	identity   fakeOIDCIdentity
	codes      map[string]fakeOIDCLogin
	tokens     map[string]fakeOIDCIdentity
}

func newFakeOIDCProvider(t *testing.T) *fakeOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test RSA key: %v", err)
	}
	p := &fakeOIDCProvider{
		privateKey: key,
		codes:      make(map[string]fakeOIDCLogin),
		tokens:     make(map[string]fakeOIDCIdentity),
	}
	p.server = httptest.NewServer(http.HandlerFunc(p.serveHTTP))
	t.Cleanup(p.server.Close)
	return p
}

func (p *fakeOIDCProvider) setIdentity(identity fakeOIDCIdentity) {
	p.mu.Lock()
	p.identity = identity
	p.mu.Unlock()
}

func (p *fakeOIDCProvider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		writeJSON(w, map[string]any{
			"issuer":                                p.server.URL,
			"authorization_endpoint":                p.server.URL + "/authorize",
			"token_endpoint":                        p.server.URL + "/token",
			"userinfo_endpoint":                     p.server.URL + "/userinfo",
			"jwks_uri":                              p.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	case "/authorize":
		p.authorize(w, r)
	case "/token":
		p.token(w, r)
	case "/jwks":
		writeJSON(w, map[string]any{"keys": []any{map[string]any{
			"kty": "RSA",
			"kid": "test-key",
			"use": "sig",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(p.privateKey.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.privateKey.E)).Bytes()),
		}}})
	case "/userinfo":
		p.userinfo(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (p *fakeOIDCProvider) authorize(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("response_type") != "code" || query.Get("client_id") != "test-client" ||
		query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		http.Error(w, "invalid authorization request", http.StatusBadRequest)
		return
	}
	code, err := randomOIDCString(16)
	if err != nil {
		http.Error(w, "random generation failed", http.StatusInternalServerError)
		return
	}
	p.mu.Lock()
	p.codes[code] = fakeOIDCLogin{
		nonce:     query.Get("nonce"),
		challenge: query.Get("code_challenge"),
		identity:  p.identity,
	}
	p.mu.Unlock()

	callback, err := url.Parse(query.Get("redirect_uri"))
	if err != nil || callback.Scheme == "" || callback.Host == "" {
		http.Error(w, "invalid callback", http.StatusBadRequest)
		return
	}
	params := callback.Query()
	params.Set("code", code)
	params.Set("state", query.Get("state"))
	callback.RawQuery = params.Encode()
	http.Redirect(w, r, callback.String(), http.StatusFound)
}

func (p *fakeOIDCProvider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	code := r.Form.Get("code")
	verifier := r.Form.Get("code_verifier")
	p.mu.Lock()
	login, ok := p.codes[code]
	delete(p.codes, code)
	p.mu.Unlock()
	if !ok || oauth2.S256ChallengeFromVerifier(verifier) != login.challenge {
		http.Error(w, "invalid code or PKCE verifier", http.StatusBadRequest)
		return
	}
	accessToken := "access-" + code
	p.mu.Lock()
	p.tokens[accessToken] = login.identity
	p.mu.Unlock()
	idToken := p.signIDToken(login.nonce, login.identity)
	writeJSON(w, map[string]any{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   3600,
		"id_token":     idToken,
	})
}

func (p *fakeOIDCProvider) userinfo(w http.ResponseWriter, r *http.Request) {
	accessToken := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	identity, ok := p.tokens[accessToken]
	p.mu.Unlock()
	if !ok {
		http.Error(w, "invalid access token", http.StatusUnauthorized)
		return
	}
	writeJSON(w, map[string]any{
		"sub":            identity.subject,
		"email":          identity.email,
		"email_verified": identity.verified,
	})
}

func (p *fakeOIDCProvider) signIDToken(nonce string, identity fakeOIDCIdentity) string {
	algorithm := identity.algorithm
	if algorithm == "" {
		algorithm = "RS256"
	}
	if identity.wrongNonce {
		nonce += "-wrong"
	}
	header, _ := json.Marshal(map[string]string{"alg": algorithm, "kid": "test-key", "typ": "JWT"})
	issuer := p.server.URL
	if identity.wrongIssuer {
		issuer += "/wrong"
	}
	audience := "test-client"
	if identity.wrongAudience {
		audience = "another-client"
	}
	claims := map[string]any{
		"iss":   issuer,
		"sub":   identity.subject,
		"aud":   audience,
		"exp":   time.Now().Add(5 * time.Minute).Unix(),
		"iat":   time.Now().Unix(),
		"nonce": nonce,
	}
	if !identity.omitEmail {
		claims["email"] = identity.email
		claims["email_verified"] = identity.verified
	}
	payload, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, p.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		panic(fmt.Sprintf("sign test ID token: %v", err))
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		http.Error(w, "encode response", http.StatusInternalServerError)
	}
}

func newOIDCTestServer(t *testing.T, provider *fakeOIDCProvider, buttonText string) (*httptest.Server, *Store) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tmpl, err := LoadTemplates()
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	store := newTestStore(t)
	access := newTestAccess(t, store)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for app: %v", err)
	}
	appURL := "http://" + listener.Addr().String()
	sso, err := newOIDCAuthenticator(context.Background(), ssoConfig{
		wellKnownURL:    provider.server.URL + "/.well-known/openid-configuration",
		clientID:        "test-client",
		clientSecret:    "test-secret",
		scopes:          "openid email profile",
		redirectURL:     appURL + "/oidc/callback",
		frontendURL:     appURL,
		loginButtonText: buttonText,
	}, provider.server.Client())
	if err != nil {
		listener.Close()
		t.Fatalf("configure test OIDC: %v", err)
	}
	srv := httptest.NewUnstartedServer(newRouter(logger, tmpl, store, access, sso))
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, store
}
