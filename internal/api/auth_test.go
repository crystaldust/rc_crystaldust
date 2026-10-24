package api

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"messagehub/internal/apikey"
	"messagehub/internal/config"
)

func requestWithAuth(header string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	if header != "" {
		r.Header.Set("Authorization", header)
	}
	return r
}

func TestAuthenticate(t *testing.T) {
	e := newTestEnv(t)
	a := newAuthenticator(e.currentConfig, e.resolver(), e.log)
	tests := []struct {
		name   string
		header string
		wantID string // empty: rejected
	}{
		{"valid source key", "Bearer " + e.keys["order-service"], "order-service"},
		{"valid operator key", "Bearer " + e.keys["oncall-operator"], "oncall-operator"},
		{"scheme is case-insensitive", "bearer " + e.keys["support-viewer"], "support-viewer"},
		{"unknown key", "Bearer mhk_unknownkeyunknownkeyunknownkeyunknownkey0", ""},
		{"missing header", "", ""},
		{"basic scheme", "Basic " + e.keys["order-service"], ""},
		{"empty bearer", "Bearer ", ""},
		{"no space", "Bearer" + e.keys["order-service"], ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			p, ok := a.authenticate(requestWithAuth(tt.header))

			// Assert
			if tt.wantID == "" {
				if ok {
					t.Fatalf("authenticated as %s, want rejection", p.ID)
				}
				return
			}
			if !ok || p.ID != tt.wantID {
				t.Fatalf("principal = %+v (ok %v), want %s", p, ok, tt.wantID)
			}
		})
	}
	for _, key := range e.keys {
		if strings.Contains(e.logs.String(), key) {
			t.Fatal("logs contain an API key")
		}
	}
}

func TestAuthenticateDuringRotation(t *testing.T) {
	// Arrange: the variable holds the old and the new hash.
	e := newTestEnv(t)
	p, _ := e.cfg.Principal("order-service")
	oldKey := e.keys[p.ID]
	name := strings.TrimPrefix(p.APIKeySHA256Ref, config.EnvRefPrefix)
	oldHash := e.env[name]
	newKey, newHash, err := apikey.Generate(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e.env[name] = oldHash + "," + newHash
	a := newAuthenticator(e.currentConfig, e.resolver(), e.log)

	// Act / Assert
	for _, key := range []string{oldKey, newKey} {
		if got, ok := a.authenticate(requestWithAuth("Bearer " + key)); !ok || got.ID != p.ID {
			t.Errorf("key during rotation rejected: %+v %v", got, ok)
		}
	}
}

func TestAuthenticateFollowsConfigReload(t *testing.T) {
	// Arrange: the operator's hash moves to a new variable in a new config.
	e := newTestEnv(t)
	oldOperatorKey := e.keys["oncall-operator"]
	current := e.cfg
	a := newAuthenticator(func() *config.Config { return current }, e.resolver(), e.log)
	if _, ok := a.authenticate(requestWithAuth("Bearer " + oldOperatorKey)); !ok {
		t.Fatal("operator key rejected before reload")
	}
	reloaded, err := config.Parse([]byte(strings.Replace(readExample(t),
		"env:HUB_KEY_ONCALL_OPERATOR_SHA256", "env:HUB_KEY_ONCALL_OPERATOR_V2_SHA256", 1)))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reloaded.Principal("oncall-operator")
	newOperatorKey := e.addKey(t, p)

	// Act
	current = reloaded

	// Assert
	if got, ok := a.authenticate(requestWithAuth("Bearer " + newOperatorKey)); !ok || got.ID != "oncall-operator" {
		t.Errorf("new key rejected after reload: %+v %v", got, ok)
	}
	if _, ok := a.authenticate(requestWithAuth("Bearer " + oldOperatorKey)); ok {
		t.Error("old key still accepted after reload")
	}
}

func TestAuthenticateSkipsPrincipalWithBadHash(t *testing.T) {
	// Arrange
	e := newTestEnv(t)
	e.env["HUB_KEY_SUPPORT_VIEWER_SHA256"] = "not-a-hash"
	delete(e.env, "HUB_KEY_BILLING_SERVICE_SHA256")
	a := newAuthenticator(e.currentConfig, e.resolver(), e.log)

	// Act / Assert
	if _, ok := a.authenticate(requestWithAuth("Bearer " + e.keys["support-viewer"])); ok {
		t.Error("principal with a malformed hash authenticated")
	}
	if _, ok := a.authenticate(requestWithAuth("Bearer " + e.keys["order-service"])); !ok {
		t.Error("other principals stopped working")
	}
	logs := e.logs.String()
	if !strings.Contains(logs, "support-viewer") || !strings.Contains(logs, "billing-service") || strings.Contains(logs, "not-a-hash") {
		t.Errorf("error log should name both principals without values: %s", logs)
	}
}

func TestAuthenticateRejectsSharedHash(t *testing.T) {
	// Arrange: a source and the operator share one key hash by mistake.
	e := newTestEnv(t)
	e.env["HUB_KEY_ONCALL_OPERATOR_SHA256"] = e.env["HUB_KEY_ORDER_SERVICE_SHA256"]
	a := newAuthenticator(e.currentConfig, e.resolver(), e.log)

	// Act / Assert
	if p, ok := a.authenticate(requestWithAuth("Bearer " + e.keys["order-service"])); ok {
		t.Errorf("ambiguous key authenticated as %s", p.ID)
	}
	if _, ok := a.authenticate(requestWithAuth("Bearer " + e.keys["support-viewer"])); !ok {
		t.Error("unrelated principal stopped working")
	}
	if !strings.Contains(e.logs.String(), "shared with another principal") {
		t.Errorf("logs = %s", e.logs)
	}
}

func TestRejectedRequestsAreLogged(t *testing.T) {
	e := newTestEnv(t)
	a := newAuthenticator(e.currentConfig, e.resolver(), e.log)
	h := a.require([]config.Role{config.RoleOperator}, func(http.ResponseWriter, *http.Request, config.Principal) {})

	h(httptest.NewRecorder(), requestWithAuth("Bearer mhk_wrong"))
	h(httptest.NewRecorder(), requestWithAuth("Bearer "+e.keys["support-viewer"]))

	logs := e.logs.String()
	if strings.Count(logs, `"msg":"auth.rejected"`) != 2 || !strings.Contains(logs, `"reason":"forbidden"`) ||
		strings.Contains(logs, "mhk_wrong") {
		t.Errorf("logs = %s", logs)
	}
}

func TestRequireRole(t *testing.T) {
	e := newTestEnv(t)
	a := newAuthenticator(e.currentConfig, e.resolver(), e.log)
	called := false
	h := a.require([]config.Role{config.RoleOperator}, func(w http.ResponseWriter, _ *http.Request, p config.Principal) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	tests := []struct {
		name       string
		header     string
		wantStatus int
		wantCode   string
	}{
		{"allowed role", "Bearer " + e.keys["oncall-operator"], http.StatusNoContent, ""},
		{"other role", "Bearer " + e.keys["support-viewer"], http.StatusForbidden, codeForbidden},
		{"no key", "", http.StatusUnauthorized, codeUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = false
			rec := httptest.NewRecorder()

			h(rec, requestWithAuth(tt.header))

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantCode != "" {
				if called || !strings.Contains(rec.Body.String(), `"code":"`+tt.wantCode+`"`) {
					t.Errorf("body = %s, called %v", rec.Body, called)
				}
			}
		})
	}
}
