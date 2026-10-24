package tests

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"nacfson_pipeline/gateway/internal/keycloak"
)

func TestSessionClientUsesRevocationIdentity(t *testing.T) {
	var tokenForm url.Values
	var deleteAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/realms/platform/protocol/openid-connect/token":
			if err := r.ParseForm(); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			tokenForm = r.PostForm
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/admin/realms/platform/sessions/sess-1":
			deleteAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer srv.Close()

	err := keycloak.NewSessionClient(srv.URL, keycloak.SessionRevocationClientID, "rev-secret").RevokeSession("sess-1")
	if err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if tokenForm.Get("client_id") != "session-revocation-client" {
		t.Errorf("client_id = %q, want session-revocation-client", tokenForm.Get("client_id"))
	}
	if tokenForm.Get("client_secret") != "rev-secret" {
		t.Errorf("client_secret = %q, want rev-secret", tokenForm.Get("client_secret"))
	}
	if tokenForm.Get("client_id") == "gateway-client" {
		t.Error("client_id must not be gateway-client")
	}
	if deleteAuth != "Bearer t" {
		t.Errorf("Authorization = %q, want Bearer t", deleteAuth)
	}
}

func TestSessionClientFailsWhenDeleteRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"t"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	err := keycloak.NewSessionClient(srv.URL, keycloak.SessionRevocationClientID, "rev-secret").RevokeSession("sess-1")
	if err == nil {
		t.Fatal("expected RevokeSession error")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error %q does not contain 403", err.Error())
	}
}
