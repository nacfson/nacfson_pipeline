package kube

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"
	MicroTimeLayout   = "2006-01-02T15:04:05.000000Z07:00"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

type ObjectMeta struct {
	Name            string            `json:"name,omitempty"`
	GenerateName    string            `json:"generateName,omitempty"`
	Namespace       string            `json:"namespace,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
}

type LeaseSpec struct {
	HolderIdentity       *string `json:"holderIdentity,omitempty"`
	LeaseDurationSeconds *int32  `json:"leaseDurationSeconds,omitempty"`
	AcquireTime          *string `json:"acquireTime,omitempty"`
	RenewTime            *string `json:"renewTime,omitempty"`
}

type Lease struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   ObjectMeta `json:"metadata"`
	Spec       LeaseSpec  `json:"spec"`
}

type LeaseList struct {
	Items []Lease `json:"items"`
}

type Secret struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   ObjectMeta        `json:"metadata"`
	Type       string            `json:"type,omitempty"`
	Data       map[string][]byte `json:"data,omitempty"`
}

type ConfigMap struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   ObjectMeta        `json:"metadata"`
	Data       map[string]string `json:"data,omitempty"`
}

type ObjectReference struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

type EventSource struct {
	Component string `json:"component"`
	Host      string `json:"host,omitempty"`
}

type Event struct {
	APIVersion     string          `json:"apiVersion"`
	Kind           string          `json:"kind"`
	Metadata       ObjectMeta      `json:"metadata"`
	InvolvedObject ObjectReference `json:"involvedObject"`
	Reason         string          `json:"reason"`
	Message        string          `json:"message"`
	Type           string          `json:"type"`
	Source         EventSource     `json:"source"`
	FirstTimestamp string          `json:"firstTimestamp"`
	LastTimestamp  string          `json:"lastTimestamp"`
	Count          int32           `json:"count"`
}

type Client struct {
	BaseURL   string
	Namespace string
	HTTP      *http.Client
	Token     func() (string, error)
}

func InCluster() (*Client, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT must be set")
	}
	caPEM, err := os.ReadFile(serviceAccountDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no certificates in service account ca.crt")
	}
	ns, err := os.ReadFile(serviceAccountDir + "/namespace")
	if err != nil {
		return nil, err
	}
	return &Client{
		BaseURL:   "https://" + net.JoinHostPort(host, port),
		Namespace: strings.TrimSpace(string(ns)),
		HTTP: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		},
		Token: func() (string, error) {
			b, err := os.ReadFile(serviceAccountDir + "/token")
			return strings.TrimSpace(string(b)), err
		},
	}, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != nil {
		tok, err := c.Token()
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	case resp.StatusCode == http.StatusConflict:
		return fmt.Errorf("%s %s: %w", method, path, ErrConflict)
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) nsPath(group, resource, name string) string {
	prefix := "/api/v1"
	if group != "" {
		prefix = "/apis/" + group
	}
	p := prefix + "/namespaces/" + c.Namespace + "/" + resource
	if name != "" {
		p += "/" + name
	}
	return p
}

func (c *Client) ListLeases(ctx context.Context) ([]Lease, error) {
	var list LeaseList
	err := c.do(ctx, http.MethodGet, c.nsPath("coordination.k8s.io/v1", "leases", ""), nil, &list)
	return list.Items, err
}

func (c *Client) GetLease(ctx context.Context, name string) (Lease, error) {
	var l Lease
	err := c.do(ctx, http.MethodGet, c.nsPath("coordination.k8s.io/v1", "leases", name), nil, &l)
	return l, err
}

func (c *Client) CreateLease(ctx context.Context, l Lease) (Lease, error) {
	l.APIVersion, l.Kind = "coordination.k8s.io/v1", "Lease"
	l.Metadata.Namespace = c.Namespace
	var out Lease
	err := c.do(ctx, http.MethodPost, c.nsPath("coordination.k8s.io/v1", "leases", ""), l, &out)
	return out, err
}

func (c *Client) UpdateLease(ctx context.Context, l Lease) (Lease, error) {
	l.APIVersion, l.Kind = "coordination.k8s.io/v1", "Lease"
	var out Lease
	err := c.do(ctx, http.MethodPut, c.nsPath("coordination.k8s.io/v1", "leases", l.Metadata.Name), l, &out)
	return out, err
}

func (c *Client) NodeExists(ctx context.Context, name string) (bool, error) {
	err := c.do(ctx, http.MethodGet, "/api/v1/nodes/"+name, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

func (c *Client) GetSecret(ctx context.Context, name string) (Secret, error) {
	var s Secret
	err := c.do(ctx, http.MethodGet, c.nsPath("", "secrets", name), nil, &s)
	return s, err
}

func (c *Client) PutSecret(ctx context.Context, s Secret) error {
	s.APIVersion, s.Kind = "v1", "Secret"
	s.Metadata.Namespace = c.Namespace
	if s.Metadata.ResourceVersion == "" {
		return c.do(ctx, http.MethodPost, c.nsPath("", "secrets", ""), s, nil)
	}
	return c.do(ctx, http.MethodPut, c.nsPath("", "secrets", s.Metadata.Name), s, nil)
}

func (c *Client) GetConfigMap(ctx context.Context, name string) (ConfigMap, error) {
	var cm ConfigMap
	err := c.do(ctx, http.MethodGet, c.nsPath("", "configmaps", name), nil, &cm)
	return cm, err
}

func (c *Client) PutConfigMap(ctx context.Context, cm ConfigMap) error {
	cm.APIVersion, cm.Kind = "v1", "ConfigMap"
	cm.Metadata.Namespace = c.Namespace
	if cm.Metadata.ResourceVersion == "" {
		return c.do(ctx, http.MethodPost, c.nsPath("", "configmaps", ""), cm, nil)
	}
	return c.do(ctx, http.MethodPut, c.nsPath("", "configmaps", cm.Metadata.Name), cm, nil)
}

func (c *Client) CreateEvent(ctx context.Context, involved ObjectReference, eventType, reason, message, host string, now time.Time) error {
	ts := now.UTC().Format(time.RFC3339)
	e := Event{
		APIVersion:     "v1",
		Kind:           "Event",
		Metadata:       ObjectMeta{GenerateName: "wg-slot-agent-", Namespace: c.Namespace},
		InvolvedObject: involved,
		Reason:         reason,
		Message:        message,
		Type:           eventType,
		Source:         EventSource{Component: "wg-slot-agent", Host: host},
		FirstTimestamp: ts,
		LastTimestamp:  ts,
		Count:          1,
	}
	return c.do(ctx, http.MethodPost, c.nsPath("", "events", ""), e, nil)
}
