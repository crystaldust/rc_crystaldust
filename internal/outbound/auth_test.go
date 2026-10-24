package outbound

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"messagehub/internal/config"
	"messagehub/internal/secrets"
)

type tokenServer struct {
	srv       *httptest.Server
	requests  atomic.Int32
	expiresIn int
	status    int
}

func newTokenServer(t *testing.T, expiresIn, status int) *tokenServer {
	t.Helper()
	ts := &tokenServer{expiresIn: expiresIn, status: status}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := ts.requests.Add(1)
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "client_credentials" {
			http.Error(w, "bad grant", http.StatusBadRequest)
			return
		}
		user, pass, _ := r.BasicAuth()
		if user != "client-1" || pass != "client-secret-abc123" {
			http.Error(w, "bad client", http.StatusUnauthorized)
			return
		}
		if ts.status != http.StatusOK {
			http.Error(w, "down", ts.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"at-%d","token_type":"Bearer","expires_in":%d}`, n, ts.expiresIn)
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

func (ts *tokenServer) auth() config.Auth {
	return config.Auth{
		Type: config.AuthOAuth2ClientCredentials, TokenURL: ts.srv.URL,
		ClientIDRef: "env:CRM_CLIENT_ID", ClientSecretRef: "env:CRM_CLIENT_SECRET", Scopes: []string{"contacts.write"},
	}
}

func testResolver(env map[string]string) secrets.Resolver {
	return secrets.NewEnvResolver(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
}

var clientEnv = map[string]string{"CRM_CLIENT_ID": "client-1", "CRM_CLIENT_SECRET": "client-secret-abc123"}

func TestAuthorizationCachesToken(t *testing.T) {
	// Arrange
	ts := newTokenServer(t, 3600, http.StatusOK)
	p := NewTokenProvider(testResolver(clientEnv), NewTransport())

	// Act
	first, err1 := p.Authorization("crm", "h1", ts.auth())
	second, err2 := p.Authorization("crm", "h1", ts.auth())

	// Assert
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if first != "Bearer at-1" || second != first {
		t.Fatalf("headers = %q, %q", first, second)
	}
	if n := ts.requests.Load(); n != 1 {
		t.Fatalf("token requests = %d, want 1", n)
	}
}

func TestAuthorizationRefreshesExpiredToken(t *testing.T) {
	// Arrange: a token that expires within oauth2's refresh margin.
	ts := newTokenServer(t, 1, http.StatusOK)
	p := NewTokenProvider(testResolver(clientEnv), NewTransport())

	// Act
	first, _ := p.Authorization("crm", "h1", ts.auth())
	second, err := p.Authorization("crm", "h1", ts.auth())

	// Assert
	if err != nil || first == second || ts.requests.Load() != 2 {
		t.Fatalf("headers = %q, %q (err %v), requests = %d", first, second, err, ts.requests.Load())
	}
}

func TestAuthorizationNewConfigHashReplacesSource(t *testing.T) {
	// Arrange
	ts := newTokenServer(t, 3600, http.StatusOK)
	p := NewTokenProvider(testResolver(clientEnv), NewTransport())

	// Act
	_, _ = p.Authorization("crm", "h1", ts.auth())
	_, err := p.Authorization("crm", "h2", ts.auth())

	// Assert
	if err != nil || ts.requests.Load() != 2 {
		t.Fatalf("err = %v, requests = %d, want a fresh token for the new config", err, ts.requests.Load())
	}
	if len(p.sources) != 1 {
		t.Fatalf("sources = %d, want the old one dropped", len(p.sources))
	}
}

func TestAuthorizationFailuresAreInfra(t *testing.T) {
	tests := []struct {
		name   string
		status int
		env    map[string]string
	}{
		{"token endpoint 500", http.StatusInternalServerError, clientEnv},
		{"client id not set", http.StatusOK, map[string]string{"CRM_CLIENT_SECRET": "x"}},
		{"client secret not set", http.StatusOK, map[string]string{"CRM_CLIENT_ID": "client-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			ts := newTokenServer(t, 3600, tt.status)
			p := NewTokenProvider(testResolver(tt.env), NewTransport())

			// Act
			_, err := p.Authorization("crm", "h1", ts.auth())

			// Assert
			if !errors.Is(err, ErrInfra) {
				t.Fatalf("err = %v, want ErrInfra", err)
			}
		})
	}
}

func TestAuthorizationErrorsOmitTokenResponses(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"error code", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_client","echo":"` + fakeSecret + `"}`))
		}, "token endpoint returned 401 (invalid_client)"},
		{"plain status", func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, fakeSecret, http.StatusBadGateway)
		}, "token endpoint returned 502"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()
			auth := config.Auth{Type: config.AuthOAuth2ClientCredentials, TokenURL: srv.URL,
				ClientIDRef: "env:CRM_CLIENT_ID", ClientSecretRef: "env:CRM_CLIENT_SECRET"}

			// Act
			_, err := NewTokenProvider(testResolver(clientEnv), NewTransport()).Authorization("crm", "h", auth)

			// Assert
			if err == nil || strings.Contains(err.Error(), fakeSecret) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q and no response body", err, tt.want)
			}
		})
	}
}

func TestAuthorizationMalformedTokenResponse(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":`))
	}))
	defer srv.Close()
	auth := config.Auth{Type: config.AuthOAuth2ClientCredentials, TokenURL: srv.URL,
		ClientIDRef: "env:CRM_CLIENT_ID", ClientSecretRef: "env:CRM_CLIENT_SECRET"}

	// Act
	_, err := NewTokenProvider(testResolver(clientEnv), NewTransport()).Authorization("crm", "h", auth)

	// Assert
	if !errors.Is(err, ErrInfra) {
		t.Fatalf("err = %v, want ErrInfra", err)
	}
}

func TestAuthorizationUnreachableEndpoint(t *testing.T) {
	// Arrange
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	auth := config.Auth{Type: config.AuthOAuth2ClientCredentials, TokenURL: srv.URL,
		ClientIDRef: "env:CRM_CLIENT_ID", ClientSecretRef: "env:CRM_CLIENT_SECRET"}

	// Act
	_, err := NewTokenProvider(testResolver(clientEnv), NewTransport()).Authorization("crm", "h", auth)

	// Assert
	if !errors.Is(err, ErrInfra) || !strings.Contains(err.Error(), "token endpoint unreachable") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthorizationNoneReturnsEmpty(t *testing.T) {
	// Act
	h, err := NewTokenProvider(testResolver(nil), NewTransport()).Authorization("x", "h", config.Auth{Type: config.AuthNone})

	// Assert
	if h != "" || err != nil {
		t.Fatalf("header = %q, err = %v", h, err)
	}
}
