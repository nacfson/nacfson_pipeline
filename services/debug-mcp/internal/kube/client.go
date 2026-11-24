package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nacfson_pipeline/services/debug-mcp/internal/config"
	"nacfson_pipeline/services/debug-mcp/internal/redact"
)

const (
	MaxConcurrent  = 4
	RequestTimeout = 10 * time.Second
	MaxJSONBytes   = 1 << 20
	MaxLogBytes    = 32 * 1024
)

var rfc1123Name = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return e.Message
}

type statusResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Code    int    `json:"code"`
}

type Client struct {
	store *config.Store
	sem   chan struct{}
}

func NewClient(store *config.Store) *Client {
	return &Client{
		store: store,
		sem:   make(chan struct{}, MaxConcurrent),
	}
}

func ValidateName(name string) error {
	if len(name) == 0 || len(name) > 253 {
		return errors.New("identifier must be 1 to 253 characters")
	}
	if !rfc1123Name.MatchString(name) {
		return fmt.Errorf("identifier %q must match RFC 1123 DNS label pattern", name)
	}
	return nil
}

func (c *Client) doGet(ctx context.Context, path string, query url.Values, maxBytes int64) ([]byte, int, error) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}

	resolved, err := c.store.Get()
	if err != nil {
		return nil, 0, fmt.Errorf("load kubeconfig: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()

	fullURL := resolved.Server + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+resolved.Token)
	req.Header.Set("Accept", "application/json, text/plain;q=0.9")
	req.Header.Set("User-Agent", "nacfson-mcp/1.0")

	resp, err := resolved.Client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("kubernetes API GET %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response %s: %w", path, err)
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, resp.StatusCode, &APIError{
			StatusCode: http.StatusUnauthorized,
			Message:    "kubernetes API returned 401 Unauthorized (token expired or revoked); refresh with: sudo ./scripts/mcp/refresh-kubeconfig.sh",
		}
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, resp.StatusCode, &APIError{
			StatusCode: http.StatusForbidden,
			Message:    fmt.Sprintf("kubernetes RBAC returned 403 Forbidden for GET %s", path),
		}
	}
	if resp.StatusCode >= 400 {
		msg := fmt.Sprintf("kubernetes API returned HTTP %d for GET %s", resp.StatusCode, path)
		var st statusResponse
		if json.Unmarshal(body, &st) == nil && st.Message != "" {
			cleanMsg, _ := redact.Redact(st.Message)
			msg = fmt.Sprintf("kubernetes API returned HTTP %d: %s", resp.StatusCode, cleanMsg)
		}
		return nil, resp.StatusCode, &APIError{
			StatusCode: resp.StatusCode,
			Message:    msg,
		}
	}
	return body, resp.StatusCode, nil
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, dst any) error {
	body, _, err := c.doGet(ctx, path, query, MaxJSONBytes)
	if err != nil {
		return err
	}
	if int64(len(body)) > MaxJSONBytes {
		return fmt.Errorf("kubernetes API response exceeded %d bytes", MaxJSONBytes)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("decode kubernetes response for %s: %w", path, err)
	}
	return nil
}

func (c *Client) ListNodes(ctx context.Context) (*NodeList, error) {
	var out NodeList
	if err := c.getJSON(ctx, "/api/v1/nodes", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListNamespaces(ctx context.Context) (*NamespaceList, error) {
	var out NamespaceList
	if err := c.getJSON(ctx, "/api/v1/namespaces", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListPods(ctx context.Context, namespace string) (*PodList, error) {
	path := "/api/v1/pods"
	if namespace != "" {
		if err := ValidateName(namespace); err != nil {
			return nil, err
		}
		path = "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods"
	}
	var out PodList
	if err := c.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetPod(ctx context.Context, namespace, pod string) (*Pod, error) {
	if err := ValidateName(namespace); err != nil {
		return nil, err
	}
	if err := ValidateName(pod); err != nil {
		return nil, err
	}
	var out Pod
	path := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods/" + url.PathEscape(pod)
	if err := c.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListDeployments(ctx context.Context, namespace string) (*DeploymentList, error) {
	if err := ValidateName(namespace); err != nil {
		return nil, err
	}
	var out DeploymentList
	path := "/apis/apps/v1/namespaces/" + url.PathEscape(namespace) + "/deployments"
	if err := c.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListStatefulSets(ctx context.Context, namespace string) (*StatefulSetList, error) {
	if err := ValidateName(namespace); err != nil {
		return nil, err
	}
	var out StatefulSetList
	path := "/apis/apps/v1/namespaces/" + url.PathEscape(namespace) + "/statefulsets"
	if err := c.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListDaemonSets(ctx context.Context, namespace string) (*DaemonSetList, error) {
	if err := ValidateName(namespace); err != nil {
		return nil, err
	}
	var out DaemonSetList
	path := "/apis/apps/v1/namespaces/" + url.PathEscape(namespace) + "/daemonsets"
	if err := c.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListJobs(ctx context.Context, namespace string) (*JobList, error) {
	if err := ValidateName(namespace); err != nil {
		return nil, err
	}
	var out JobList
	path := "/apis/batch/v1/namespaces/" + url.PathEscape(namespace) + "/jobs"
	if err := c.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListCronJobs(ctx context.Context, namespace string) (*CronJobList, error) {
	if err := ValidateName(namespace); err != nil {
		return nil, err
	}
	var out CronJobList
	path := "/apis/batch/v1/namespaces/" + url.PathEscape(namespace) + "/cronjobs"
	if err := c.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListEvents(ctx context.Context, namespace, involvedObject string, limit int) (*EventList, error) {
	if err := ValidateName(namespace); err != nil {
		return nil, err
	}
	q := url.Values{}
	if involvedObject != "" {
		if err := ValidateName(involvedObject); err != nil {
			return nil, err
		}
		q.Set("fieldSelector", "involvedObject.name="+involvedObject)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out EventList
	path := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/events"
	if err := c.getJSON(ctx, path, q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) ListFluxResources(ctx context.Context, group, version, plural, namespace string) (*FluxResourceList, error) {
	var path string
	if namespace != "" {
		if err := ValidateName(namespace); err != nil {
			return nil, err
		}
		path = fmt.Sprintf("/apis/%s/%s/namespaces/%s/%s", group, version, url.PathEscape(namespace), plural)
	} else {
		path = fmt.Sprintf("/apis/%s/%s/%s", group, version, plural)
	}
	var out FluxResourceList
	if err := c.getJSON(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetPodLogs(ctx context.Context, namespace, pod, container string, previous bool, tailLines int, sinceSeconds int) (string, bool, error) {
	if err := ValidateName(namespace); err != nil {
		return "", false, err
	}
	if err := ValidateName(pod); err != nil {
		return "", false, err
	}
	q := url.Values{}
	q.Set("limitBytes", strconv.Itoa(MaxLogBytes))
	if container != "" {
		if err := ValidateName(container); err != nil {
			return "", false, err
		}
		q.Set("container", container)
	}
	if previous {
		q.Set("previous", "true")
	}
	if tailLines > 0 {
		q.Set("tailLines", strconv.Itoa(tailLines))
	}
	if sinceSeconds > 0 {
		q.Set("sinceSeconds", strconv.Itoa(sinceSeconds))
	}
	path := "/api/v1/namespaces/" + url.PathEscape(namespace) + "/pods/" + url.PathEscape(pod) + "/log"
	body, _, err := c.doGet(ctx, path, q, MaxLogBytes)
	if err != nil {
		return "", false, err
	}
	truncated := len(body) >= MaxLogBytes
	if len(body) > MaxLogBytes {
		body = body[:MaxLogBytes]
	}
	return strings.TrimRight(string(body), "\r\n"), truncated, nil
}
