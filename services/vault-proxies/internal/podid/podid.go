// Package podid attributes a peer IP to a Kubernetes pod by asking the
// kube-apiserver with the caller's own ServiceAccount token.
package podid

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultTokenPath is the in-cluster ServiceAccount token location.
	DefaultTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	// DefaultCAPath is the in-cluster kube-apiserver CA bundle location.
	DefaultCAPath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// Identity is the pod identity resolved for a peer IP.
type Identity struct {
	Namespace string `json:"namespace"`
	PodName   string `json:"pod_name"`
	AppLabel  string `json:"app_label"`
}

type cachedIdentity struct {
	identity  *Identity
	expiresAt time.Time
}

// Resolver resolves pod IPs to identities, caching each result by IP for ttl.
type Resolver struct {
	// Now returns the current time for cache expiry; defaults to time.Now.
	Now func() time.Time

	apiServer string
	client    *http.Client
	tokenPath string
	ttl       time.Duration

	mu    sync.Mutex
	cache map[string]cachedIdentity
}

// NewResolver builds a Resolver. apiServer is the base URL, for example
// https://kubernetes.default.svc.
func NewResolver(apiServer string, client *http.Client, tokenPath string, ttl time.Duration) *Resolver {
	return &Resolver{
		Now:       time.Now,
		apiServer: strings.TrimRight(apiServer, "/"),
		client:    client,
		tokenPath: tokenPath,
		ttl:       ttl,
		cache:     make(map[string]cachedIdentity),
	}
}

// Resolve returns the identity of the pod that owns ip.
func (r *Resolver) Resolve(ctx context.Context, ip string) (*Identity, error) {
	now := r.Now()
	r.mu.Lock()
	if cached, ok := r.cache[ip]; ok && now.Before(cached.expiresAt) {
		r.mu.Unlock()
		return cached.identity, nil
	}
	r.mu.Unlock()

	if r.apiServer == "" {
		return nil, errors.New("kube-apiserver address not configured (KUBERNETES_SERVICE_HOST/PORT not set)")
	}
	jwtBytes, err := os.ReadFile(r.tokenPath)
	if err != nil {
		return nil, err
	}
	u := r.apiServer + "/api/v1/pods?fieldSelector=" + url.QueryEscape("status.podIP="+ip)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(jwtBytes)))
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kube-apiserver status %d", resp.StatusCode)
	}
	var podList struct {
		Items []struct {
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&podList); err != nil || len(podList.Items) == 0 {
		return nil, errors.New("no matching pod for IP")
	}
	item := podList.Items[0]
	id := &Identity{
		Namespace: item.Metadata.Namespace,
		PodName:   item.Metadata.Name,
		AppLabel:  item.Metadata.Labels["app.kubernetes.io/name"],
	}
	r.mu.Lock()
	r.cache[ip] = cachedIdentity{identity: id, expiresAt: now.Add(r.ttl)}
	r.mu.Unlock()
	return id, nil
}

// PeerIP returns the host part of an http.Request.RemoteAddr.
func PeerIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return strings.TrimSpace(remoteAddr)
	}
	return host
}

// InClusterAPIServer returns the kube-apiserver base URL from the in-cluster
// environment, or "" when KUBERNETES_SERVICE_HOST/PORT are not set.
func InClusterAPIServer() string {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return ""
	}
	return "https://" + net.JoinHostPort(host, port)
}

// InClusterHTTPClient returns an HTTP client that trusts the in-cluster CA.
func InClusterHTTPClient() *http.Client {
	pool := x509.NewCertPool()
	if caBytes, err := os.ReadFile(DefaultCAPath); err == nil {
		_ = pool.AppendCertsFromPEM(caBytes)
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    pool,
			},
		},
	}
}
