// Package openbao is a minimal OpenBao client: Kubernetes-auth login and
// KV v2 read/write under the "kv" mount.
package openbao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client talks to OpenBao using a ServiceAccount JWT.
type Client struct {
	Addr    string
	Role    string
	JWTPath string
	// HTTP is the client used for all requests; a 30s-timeout client is used when nil.
	HTTP *http.Client
}

var defaultHTTP = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

func (c *Client) base() string { return strings.TrimRight(c.Addr, "/") }

// Login authenticates with the Kubernetes auth method and returns a client token.
func (c *Client) Login(ctx context.Context) (string, error) {
	jwtBytes, err := os.ReadFile(c.JWTPath)
	if err != nil {
		return "", fmt.Errorf("read serviceaccount jwt: %w", err)
	}
	loginBody, _ := json.Marshal(map[string]string{
		"role": c.Role,
		"jwt":  strings.TrimSpace(string(jwtBytes)),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/v1/auth/kubernetes/login", bytes.NewReader(loginBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openbao k8s login status %d", resp.StatusCode)
	}
	var parsed struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil || parsed.Auth.ClientToken == "" {
		return "", errors.New("missing client_token in openbao login response")
	}
	return parsed.Auth.ClientToken, nil
}

// ReadKV reads the KV v2 secret at kv/data/<path> and returns its data map.
// The map is nil when the response carries no data.
func (c *Client) ReadKV(ctx context.Context, token, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/v1/kv/data/"+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openbao kv read status %d", resp.StatusCode)
	}
	var parsed struct {
		Data struct {
			Data map[string]any `json:"data"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode openbao kv read response: %w", err)
	}
	return parsed.Data.Data, nil
}

// WriteKV writes data to the KV v2 secret at kv/data/<path>.
func (c *Client) WriteKV(ctx context.Context, token, path string, data map[string]any) error {
	body, _ := json.Marshal(map[string]any{"data": data})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+"/v1/kv/data/"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("openbao kv write status %d", resp.StatusCode)
	}
	return nil
}
