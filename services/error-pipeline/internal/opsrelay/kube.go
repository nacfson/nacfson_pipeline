package opsrelay

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"

type KubeEvent struct {
	Reason         string `json:"reason"`
	Message        string `json:"message"`
	LastTimestamp  string `json:"lastTimestamp"`
	EventTime      string `json:"eventTime"`
	InvolvedObject struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"involvedObject"`
	Source struct {
		Host string `json:"host"`
	} `json:"source"`
}

type Lease struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		HolderIdentity       *string `json:"holderIdentity"`
		AcquireTime          *string `json:"acquireTime"`
		RenewTime            *string `json:"renewTime"`
		LeaseDurationSeconds *int32  `json:"leaseDurationSeconds"`
	} `json:"spec"`
}

func (ev KubeEvent) Time() time.Time {
	for _, s := range []string{ev.EventTime, ev.LastTimestamp} {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

type Kube struct {
	BaseURL   string
	Namespace string
	HTTP      *http.Client
	Token     func() (string, error)
}

func InCluster(namespace string) (*Kube, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not running in a cluster")
	}
	ca, err := os.ReadFile(serviceAccountDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	return &Kube{
		BaseURL:   "https://" + net.JoinHostPort(host, port),
		Namespace: namespace,
		HTTP:      &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}},
		Token: func() (string, error) {
			b, err := os.ReadFile(serviceAccountDir + "/token")
			return strings.TrimSpace(string(b)), err
		},
	}, nil
}

func (k *Kube) get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if k.Token != nil {
		tok, err := k.Token()
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := k.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	return resp, nil
}

func (k *Kube) path(group, resource string) string {
	if group == "" {
		return "/api/v1/namespaces/" + k.Namespace + "/" + resource
	}
	return "/apis/" + group + "/namespaces/" + k.Namespace + "/" + resource
}

func (k *Kube) Follow(ctx context.Context, group, resource string, listed func([]json.RawMessage), changed func(json.RawMessage), deleted func(json.RawMessage)) error {
	resp, err := k.get(ctx, k.path(group, resource))
	if err != nil {
		return err
	}
	var list struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Items []json.RawMessage `json:"items"`
	}
	err = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if err != nil {
		return err
	}
	listed(list.Items)
	resp, err = k.get(ctx, k.path(group, resource)+"?watch=true&allowWatchBookmarks=false&resourceVersion="+list.Metadata.ResourceVersion)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var w struct {
			Type   string          `json:"type"`
			Object json.RawMessage `json:"object"`
		}
		if err := json.Unmarshal(sc.Bytes(), &w); err != nil {
			continue
		}
		switch w.Type {
		case "ADDED", "MODIFIED":
			changed(w.Object)
		case "DELETED":
			deleted(w.Object)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("watch closed")
}

func SortEventsByTime(items []json.RawMessage) []json.RawMessage {
	type timed struct {
		at  time.Time
		raw json.RawMessage
	}
	list := make([]timed, 0, len(items))
	for _, raw := range items {
		var ev KubeEvent
		_ = json.Unmarshal(raw, &ev)
		list = append(list, timed{ev.Time(), raw})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	out := make([]json.RawMessage, len(list))
	for i, t := range list {
		out[i] = t.raw
	}
	return out
}

func SlotOf(lease Lease) (int, bool) {
	n, err := strconv.Atoi(strings.TrimPrefix(lease.Metadata.Name, "wg-slot-"))
	return n, err == nil && strings.HasPrefix(lease.Metadata.Name, "wg-slot-")
}

func LeaseMetrics(lease Lease, at time.Time) []Metric {
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
		return nil
	}
	src := "gke/" + *lease.Spec.HolderIdentity
	var out []Metric
	for annotation, metric := range map[string]string{
		"oci-link.nacfson.io/fluent-bit-buffer-bytes": "fluentbit.buffer.bytes",
		"oci-link.nacfson.io/handshake-age-seconds":   "wireguard.handshake_age",
	} {
		if v, err := strconv.ParseFloat(lease.Metadata.Annotations[annotation], 64); err == nil {
			out = append(out, Metric{Schema: 1, Source: src, Metric: metric, Value: v, Time: at})
		}
	}
	return out
}
