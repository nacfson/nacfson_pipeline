package tests

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"nacfson_pipeline/gateway/internal/keycloak"
)

// Regression for the 2026-11-02 pn.nacfson.cloud sign-out at 12:01:58 UTC.
// Keycloak introspection reports a token active while now <= exp. The project
// backend rejects it when now >= exp. A request landing in the token's last
// second was confirmed by the gateway and then rejected by the project, which
// signed the visitor out. The gateway must refresh a token that is close to expiry
// instead of handing it out.

func jwtWithExp(exp int64) string {
	enc := base64.RawURLEncoding
	header := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := enc.EncodeToString([]byte(fmt.Sprintf(`{"sub":"user-1","aud":"project-pn","exp":%d}`, exp)))
	return header + "." + payload + ".sig"
}

type fakeKeycloak struct {
	srv            *httptest.Server
	introspections atomic.Int32
	refreshes      atomic.Int32
}

func newFakeKeycloak(t *testing.T, introspectActive bool, refreshOK bool) *fakeKeycloak {
	t.Helper()
	f := &fakeKeycloak{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/platform/protocol/openid-connect/token/introspect":
			f.introspections.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"active":%t}`, introspectActive)
		case "/realms/platform/protocol/openid-connect/token":
			f.refreshes.Add(1)
			if !refreshOK {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"fresh-access","refresh_token":"fresh-refresh","expires_in":300}`))
		default:
			http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeKeycloak) client() *keycloak.Client {
	return keycloak.NewClient(f.srv.URL+"/realms/platform", f.srv.URL, "gateway-client", "secret")
}

func TestValidateAndRefreshRefreshesTokenNearExpiry(t *testing.T) {
	now := time.Now().Unix()
	cases := map[string]int64{
		"exp equals now (introspection still says active)": now,
		"exp in 10s":   now + 10,
		"exp in 29s":   now + 29,
		"already past": now - 5,
	}
	for name, exp := range cases {
		t.Run(name, func(t *testing.T) {
			kc := newFakeKeycloak(t, true, true)
			active, refreshed, err := kc.client().ValidateAndRefresh("sid", jwtWithExp(exp), "old-refresh", "project-pn")
			if err != nil || !active {
				t.Fatalf("active=%v err=%v, want active", active, err)
			}
			if refreshed == nil || refreshed.AccessToken != "fresh-access" {
				t.Fatalf("expected a refreshed token, got %+v", refreshed)
			}
			if got := kc.refreshes.Load(); got != 1 {
				t.Errorf("refreshes = %d, want 1", got)
			}
			if got := kc.introspections.Load(); got != 0 {
				t.Errorf("introspections = %d, want 0 (a near-expiry token must not be confirmed as-is)", got)
			}
		})
	}
}

func TestValidateAndRefreshKeepsTokenWithLifeLeft(t *testing.T) {
	kc := newFakeKeycloak(t, true, true)
	exp := time.Now().Add(2 * time.Minute).Unix()
	active, refreshed, err := kc.client().ValidateAndRefresh("sid", jwtWithExp(exp), "old-refresh", "project-pn")
	if err != nil || !active || refreshed != nil {
		t.Fatalf("active=%v refreshed=%+v err=%v, want active without refresh", active, refreshed, err)
	}
	if kc.introspections.Load() != 1 || kc.refreshes.Load() != 0 {
		t.Errorf("introspections=%d refreshes=%d, want 1/0", kc.introspections.Load(), kc.refreshes.Load())
	}
}

func TestValidateAndRefreshEarlyRefreshFailureKeepsValidToken(t *testing.T) {
	kc := newFakeKeycloak(t, true, false)
	exp := time.Now().Add(10 * time.Second).Unix()
	active, refreshed, err := kc.client().ValidateAndRefresh("sid", jwtWithExp(exp), "old-refresh", "project-pn")
	if err != nil || !active || refreshed != nil {
		t.Fatalf("active=%v refreshed=%+v err=%v, want the still-valid token kept", active, refreshed, err)
	}
	if kc.refreshes.Load() != 1 || kc.introspections.Load() != 1 {
		t.Errorf("refreshes=%d introspections=%d, want 1/1", kc.refreshes.Load(), kc.introspections.Load())
	}
}

func TestValidateAndRefreshRevokedSessionStillRejected(t *testing.T) {
	kc := newFakeKeycloak(t, false, false)
	exp := time.Now().Add(10 * time.Second).Unix()
	active, refreshed, err := kc.client().ValidateAndRefresh("sid", jwtWithExp(exp), "old-refresh", "project-pn")
	if err != nil || active || refreshed != nil {
		t.Fatalf("active=%v refreshed=%+v err=%v, want rejected", active, refreshed, err)
	}
	if got := kc.refreshes.Load(); got != 1 {
		t.Errorf("refreshes = %d, want exactly 1 (no second refresh after the early one failed)", got)
	}
}

func TestValidateAndRefreshNonJWTKeepsIntrospectionFirst(t *testing.T) {
	kc := newFakeKeycloak(t, true, true)
	active, refreshed, err := kc.client().ValidateAndRefresh("sid", "opaque-token", "old-refresh", "project-pn")
	if err != nil || !active || refreshed != nil {
		t.Fatalf("active=%v refreshed=%+v err=%v", active, refreshed, err)
	}
	if kc.refreshes.Load() != 0 {
		t.Errorf("refreshes = %d, want 0", kc.refreshes.Load())
	}
}
