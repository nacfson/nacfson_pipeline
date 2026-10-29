package openbao

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func newClient(t *testing.T, addr string) *Client {
	t.Helper()
	jwt := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(jwt, []byte("fake-sa-jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Client{Addr: addr + "/", Role: "vault-notify-proxy", JWTPath: jwt}
}

func TestLoginReadAndWriteKV(t *testing.T) {
	var written map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/kubernetes/login":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["role"] != "vault-notify-proxy" || body["jwt"] != "fake-sa-jwt" {
				t.Errorf("unexpected login body %+v", body)
			}
			_, _ = w.Write([]byte(`{"auth":{"client_token":"tok-1"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/kv/data/notify/discord/errors":
			if r.Header.Get("X-Vault-Token") != "tok-1" {
				t.Errorf("missing token on read")
			}
			_, _ = w.Write([]byte(`{"data":{"data":{"url":"https://discord.com/api/webhooks/1/x"}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/kv/data/notify/discord/errors":
			if r.Header.Get("X-Vault-Token") != "tok-1" {
				t.Errorf("missing token on write")
			}
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &written)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	c := newClient(t, srv.URL)
	ctx := context.Background()
	token, err := c.Login(ctx)
	if err != nil || token != "tok-1" {
		t.Fatalf("Login = %q, %v", token, err)
	}
	data, err := c.ReadKV(ctx, token, "notify/discord/errors")
	if err != nil {
		t.Fatal(err)
	}
	if data["url"] != "https://discord.com/api/webhooks/1/x" {
		t.Fatalf("ReadKV = %+v", data)
	}
	if err := c.WriteKV(ctx, token, "notify/discord/errors", map[string]any{"url": "u"}); err != nil {
		t.Fatal(err)
	}
	inner, _ := written["data"].(map[string]any)
	if len(written) != 1 || inner["url"] != "u" {
		t.Fatalf("WriteKV body = %+v", written)
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/kubernetes/login":
			_, _ = w.Write([]byte(`{"auth":{}}`))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	c := newClient(t, srv.URL)
	ctx := context.Background()

	if _, err := c.Login(ctx); err == nil {
		t.Error("expected error for missing client_token")
	}
	if _, err := c.ReadKV(ctx, "t", "a/b"); err == nil {
		t.Error("expected ReadKV status error")
	}
	if err := c.WriteKV(ctx, "t", "a/b", map[string]any{"k": "v"}); err == nil {
		t.Error("expected WriteKV status error")
	}
	c.JWTPath = "/nonexistent/token"
	if _, err := c.Login(ctx); err == nil {
		t.Error("expected error for missing jwt file")
	}
}

func TestLoginRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	if _, err := newClient(t, srv.URL).Login(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
