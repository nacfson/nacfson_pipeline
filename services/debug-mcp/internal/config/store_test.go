package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testCABase64(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return base64.StdEncoding.EncodeToString(pemBytes)
}

func TestParseYAMLAndKubectlClusterFirstOrder(t *testing.T) {
	caB64 := testCABase64(t)
	raw := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: ro
clusters:
- cluster:
    certificate-authority-data: %s
    server: https://127.0.0.1:6443
  name: target
contexts:
- context:
    cluster: target
    user: ro-user
  name: ro
users:
- name: ro-user
  user:
    token: tok-initial
`, caB64)

	res, err := ParseBytes([]byte(raw))
	if err != nil {
		t.Fatalf("ParseBytes failed: %v", err)
	}
	defer res.Transport.CloseIdleConnections()
	if res.Server != "https://127.0.0.1:6443" {
		t.Fatalf("server = %q", res.Server)
	}
	if res.Token != "tok-initial" {
		t.Fatalf("token = %q", res.Token)
	}
}

func TestRejectForbiddenKubeconfigFields(t *testing.T) {
	caB64 := testCABase64(t)
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "exec",
			raw: fmt.Sprintf(`clusters:
- name: c
  cluster:
    certificate-authority-data: %s
    server: https://127.0.0.1:6443
users:
- name: u
  user:
    token: tok
    exec:
      command: /bin/sh
`, caB64),
			want: "exec",
		},
		{
			name: "auth-provider",
			raw: fmt.Sprintf(`clusters:
- name: c
  cluster:
    certificate-authority-data: %s
    server: https://127.0.0.1:6443
users:
- name: u
  user:
    token: tok
    auth-provider:
      name: gcp
`, caB64),
			want: "auth-provider",
		},
		{
			name: "client-key-data",
			raw: fmt.Sprintf(`clusters:
- name: c
  cluster:
    certificate-authority-data: %s
    server: https://127.0.0.1:6443
users:
- name: u
  user:
    client-key-data: Zm9v
    token: tok
`, caB64),
			want: "client-key-data",
		},
		{
			name: "insecure-skip-tls-verify",
			raw: fmt.Sprintf(`clusters:
- name: c
  cluster:
    certificate-authority-data: %s
    server: https://127.0.0.1:6443
    insecure-skip-tls-verify: true
users:
- name: u
  user:
    token: tok
`, caB64),
			want: "insecure-skip-tls-verify",
		},
		{
			name: "http server",
			raw: fmt.Sprintf(`clusters:
- name: c
  cluster:
    certificate-authority-data: %s
    server: http://127.0.0.1:8080
users:
- name: u
  user:
    token: tok
`, caB64),
			want: "https://",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseBytes([]byte(tc.raw))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ParseBytes err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestStoreHotReloadOnMtimeAndExplicitReload(t *testing.T) {
	caB64 := testCABase64(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "kubeconfig")

	writeCfg := func(tok string, mtime time.Time) {
		raw := fmt.Sprintf(`{"apiVersion":"v1","kind":"Config","clusters":[{"name":"c","cluster":{"server":"https://127.0.0.1:6443","certificate-authority-data":%q}}],"users":[{"name":"u","user":{"token":%q}}]}`, caB64, tok)
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatalf("write kubeconfig: %v", err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	t1 := time.Now().Add(-2 * time.Minute)
	writeCfg("tok-1", t1)
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	res1, err := store.Get()
	if err != nil || res1.Token != "tok-1" {
		t.Fatalf("initial token = %q, err = %v", res1.Token, err)
	}

	writeCfg("tok-2", t1.Add(time.Minute))
	res2, err := store.Get()
	if err != nil || res2.Token != "tok-2" {
		t.Fatalf("mtime reloaded token = %q, err = %v", res2.Token, err)
	}

	writeCfg("tok-3", t1.Add(time.Minute))
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	res3, err := store.Get()
	if err != nil || res3.Token != "tok-3" {
		t.Fatalf("explicit reloaded token = %q, err = %v", res3.Token, err)
	}
}
