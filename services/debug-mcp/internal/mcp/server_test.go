package mcp_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"nacfson_pipeline/services/debug-mcp/internal/audit"
	"nacfson_pipeline/services/debug-mcp/internal/config"
	"nacfson_pipeline/services/debug-mcp/internal/kube"
	"nacfson_pipeline/services/debug-mcp/internal/mcp"
	"nacfson_pipeline/services/debug-mcp/internal/tools"
)

type mockKube struct {
	mu          sync.Mutex
	validToken  string
	nonGetSeen  int
	inFlight    int32
	maxInFlight int32
}

func (m *mockKube) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := atomic.AddInt32(&m.inFlight, 1)
		for {
			prev := atomic.LoadInt32(&m.maxInFlight)
			if cur <= prev || atomic.CompareAndSwapInt32(&m.maxInFlight, prev, cur) {
				break
			}
		}
		defer atomic.AddInt32(&m.inFlight, -1)

		if r.Method != http.MethodGet {
			m.mu.Lock()
			m.nonGetSeen++
			m.mu.Unlock()
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		m.mu.Lock()
		expected := "Bearer " + m.validToken
		m.mu.Unlock()
		if r.Header.Get("Authorization") != expected {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"status":"Failure","message":"Unauthorized","code":401}`))
			return
		}

		switch r.URL.Path {
		case "/api/v1/nodes":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"node-a","labels":{"node-role.kubernetes.io/worker":"true"}},"status":{"conditions":[{"type":"Ready","status":"True"}],"allocatable":{"cpu":"2","memory":"4Gi"},"nodeInfo":{"kubeletVersion":"v1.35.0"}}}]}`))
		case "/api/v1/namespaces":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"proj-pn"},"status":{"phase":"Active"}},{"metadata":{"name":"flux-system"},"status":{"phase":"Active"}}]}`))
		case "/api/v1/pods":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"backend-0","namespace":"proj-pn"},"spec":{"nodeName":"node-a","containers":[{"name":"app"}]},"status":{"phase":"Pending","reason":"ImagePullBackOff","containerStatuses":[{"name":"app","ready":false,"restartCount":5}]}},{"metadata":{"name":"migrate-job-1","namespace":"proj-pn"},"spec":{"nodeName":"node-a","containers":[{"name":"job"}]},"status":{"phase":"Succeeded","containerStatuses":[{"name":"job","ready":false,"restartCount":0}]}}]}`))
		case "/api/v1/namespaces/proj-pn/deployments":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"backend"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"app","image":"ghcr.io/nacfson/backend@sha256:abc"}]}}},"status":{"replicas":1,"readyReplicas":0,"updatedReplicas":1,"availableReplicas":0}}]}`))
		case "/api/v1/namespaces/proj-pn/pods", "/apis/apps/v1/namespaces/proj-pn/deployments", "/apis/apps/v1/namespaces/proj-pn/statefulsets", "/apis/apps/v1/namespaces/proj-pn/daemonsets", "/apis/batch/v1/namespaces/proj-pn/jobs", "/apis/batch/v1/namespaces/proj-pn/cronjobs":
			if r.URL.Path == "/apis/apps/v1/namespaces/proj-pn/deployments" {
				_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"backend"},"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"app","image":"ghcr.io/nacfson/backend@sha256:abc"}]}}},"status":{"replicas":1,"readyReplicas":0,"updatedReplicas":1,"availableReplicas":0}}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[]}`))
		case "/api/v1/namespaces/proj-pn/pods/backend-0":
			_, _ = w.Write([]byte(`{
				"metadata":{"name":"backend-0","namespace":"proj-pn","labels":{"app":"backend"}},
				"spec":{
					"nodeName":"node-a",
					"restartPolicy":"Always",
					"serviceAccountName":"default",
					"containers":[{
						"name":"app",
						"image":"ghcr.io/nacfson/backend:v1",
						"env":[
							{"name":"LITERAL_SECRET","value":"ULTRA_SECRET_LITERAL_999"},
							{"name":"DB_PASS","valueFrom":{"secretKeyRef":{"name":"db","key":"pass"}}}
						],
						"envFrom":[{"secretRef":{"name":"vault-sec"}}]
					}]
				},
				"status":{
					"phase":"Running",
					"qosClass":"Guaranteed",
					"message":"failed calling Bearer verysecretoauthtoken1234",
					"containerStatuses":[{
						"name":"app",
						"ready":false,
						"restartCount":2,
						"state":{"waiting":{"reason":"CrashLoopBackOff","message":"key sk-secret1234567890abcdef"}}
					}]
				}
			}`))
		case "/api/v1/namespaces/proj-pn/events":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"ev-1","namespace":"proj-pn"},"type":"Warning","reason":"BackOff","message":"token AIzaSySecretKey12345678901234567890123 failed","count":4,"lastTimestamp":"2026-11-23T09:00:00Z","involvedObject":{"kind":"Pod","name":"backend-0"}}]}`))
		case "/apis/kustomize.toolkit.fluxcd.io/v1/kustomizations":
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"projects","namespace":"flux-system"},"status":{"conditions":[{"type":"Ready","status":"True","reason":"ReconciliationSucceeded","message":"Applied revision main@sha1:123"}],"lastAppliedRevision":"main@sha1:123"}}]}`))
		case "/apis/source.toolkit.fluxcd.io/v1/gitrepositories", "/apis/source.toolkit.fluxcd.io/v1/helmrepositories", "/apis/helm.toolkit.fluxcd.io/v2/helmreleases":
			_, _ = w.Write([]byte(`{"items":[]}`))
		case "/api/v1/namespaces/proj-pn/pods/backend-0/log":
			if r.URL.Query().Get("limitBytes") != "32768" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			line1 := "INFO ok request tail=" + r.URL.Query().Get("tailLines") + " since=" + r.URL.Query().Get("sinceSeconds")
			line2 := "ERR Authorization: Bearer leakedbearer999 jwt eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJhIjoxfQ.sigpad"
			big := strings.Repeat("A", 32700)
			_, _ = w.Write([]byte(line1 + "\n" + line2 + "\n" + big + "\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":"Failure","message":"not found","code":404}`))
		}
	})
}

func writeMockKubeconfig(t *testing.T, srv *httptest.Server, path, token string) {
	t.Helper()
	cert := srv.Certificate()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	caB64 := base64.StdEncoding.EncodeToString(pemBytes)
	cfg := fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: debug
clusters:
- name: mock
  cluster:
    certificate-authority-data: %s
    server: %s
contexts:
- name: debug
  context:
    cluster: mock
    user: ai
users:
- name: ai
  user:
    token: %s
`, caB64, srv.URL, token)
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write mock kubeconfig: %v", err)
	}
}

func callRPC(t *testing.T, r io.Writer, scan *bufio.Scanner, id int, method string, params any) map[string]any {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal rpc: %v", err)
	}
	if _, err := r.Write(append(b, '\n')); err != nil {
		t.Fatalf("write rpc: %v", err)
	}
	if !scan.Scan() {
		t.Fatalf("scan response failed: %v", scan.Err())
	}
	var resp map[string]any
	if err := json.Unmarshal(scan.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", scan.Text(), err)
	}
	return resp
}

func extractToolJSON(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result in resp: %+v", resp)
	}
	isErr, _ := res["isError"].(bool)
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content in result: %+v", res)
	}
	item := content[0].(map[string]any)
	txt, _ := item["text"].(string)
	return txt, isErr
}

func TestEndToEndUnixSocketAndBridge(t *testing.T) {
	mk := &mockKube{validToken: "TEST_KUBE_BEARER_TOKEN_SECRET_XYZ"}
	ts := httptest.NewTLSServer(mk.handler())
	defer ts.Close()

	dir := t.TempDir()
	kcfgPath := filepath.Join(dir, "kubeconfig")
	sockDir, err := os.MkdirTemp("/tmp", "nmcp-*")
	if err != nil {
		t.Fatalf("mkdirtemp socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "mcp.sock")
	writeMockKubeconfig(t, ts, kcfgPath, mk.validToken)

	store, err := config.NewStore(kcfgPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	var auditBuf bytes.Buffer
	auditor := audit.NewLogger(&auditBuf)
	client := kube.NewClient(store)
	reg := tools.NewRegistry(client)
	srv := mcp.NewServer(reg, auditor)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srvErr := make(chan error, 1)
	go func() {
		srvErr <- srv.ServeUnix(ctx, sockPath, 0o600)
	}()

	ready := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-srvErr:
			t.Fatalf("ServeUnix exited early: %v", err)
		default:
		}
		if _, err := os.Stat(sockPath); err == nil {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("socket %s was not created before timeout", sockPath)
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inR.Close()
	defer inW.Close()
	defer outR.Close()
	defer outW.Close()
	bridgeDone := make(chan error, 1)
	go func() {
		bridgeDone <- mcp.ConnectUnix(ctx, sockPath, inR, outW)
	}()

	scan := bufio.NewScanner(outR)
	scan.Buffer(make([]byte, 0, 64*1024), 2<<20)

	initResp := callRPC(t, inW, scan, 1, "initialize", map[string]any{"protocolVersion": "2024-11-05"})
	if initResp["error"] != nil {
		t.Fatalf("initialize error: %+v", initResp["error"])
	}

	listResp := callRPC(t, inW, scan, 2, "tools/list", nil)
	toolDefs := listResp["result"].(map[string]any)["tools"].([]any)
	if len(toolDefs) != 6 {
		t.Fatalf("expected 6 tools, got %d", len(toolDefs))
	}

	ovResp := callRPC(t, inW, scan, 3, "tools/call", map[string]any{
		"name":      "cluster_overview",
		"arguments": map[string]any{},
	})
	ovTxt, isErr := extractToolJSON(t, ovResp)
	if isErr || !strings.Contains(ovTxt, `"nodes_ready":1`) || !strings.Contains(ovTxt, `"total_pods":2`) || !strings.Contains(ovTxt, `"Succeeded":1`) {
		t.Fatalf("cluster_overview unexpected: isErr=%v txt=%s", isErr, ovTxt)
	}
	if !strings.Contains(ovTxt, `"backend-0"`) || strings.Contains(ovTxt, `"migrate-job-1"`) {
		t.Fatalf("cluster_overview problem_pods unexpected for Succeeded pod: %s", ovTxt)
	}

	dpResp := callRPC(t, inW, scan, 4, "tools/call", map[string]any{
		"name":      "describe_pod",
		"arguments": map[string]any{"namespace": "proj-pn", "pod": "backend-0"},
	})
	dpTxt, isErr := extractToolJSON(t, dpResp)
	if isErr {
		t.Fatalf("describe_pod failed: %s", dpTxt)
	}
	for _, secret := range []string{"ULTRA_SECRET_LITERAL_999", "verysecretoauthtoken1234", "sk-secret1234567890abcdef", "AIzaSySecretKey12345678901234567890123"} {
		if strings.Contains(dpTxt, secret) {
			t.Fatalf("describe_pod leaked secret %q: %s", secret, dpTxt)
		}
	}
	if !strings.Contains(dpTxt, `"name":"LITERAL_SECRET","source":"literal"`) {
		t.Fatalf("describe_pod did not keep stripped literal env metadata: %s", dpTxt)
	}

	logResp := callRPC(t, inW, scan, 5, "tools/call", map[string]any{
		"name": "get_pod_logs",
		"arguments": map[string]any{
			"namespace":     "proj-pn",
			"pod":           "backend-0",
			"tail_lines":    999,
			"since_seconds": 99999,
		},
	})
	logTxt, isErr := extractToolJSON(t, logResp)
	if isErr {
		t.Fatalf("get_pod_logs failed: %s", logTxt)
	}
	if len(logTxt) > tools.MaxOutputBytes {
		t.Fatalf("get_pod_logs exceeded 64 KiB cap: %d", len(logTxt))
	}
	if !strings.Contains(logTxt, `"untrusted_log_data":true`) || !strings.Contains(logTxt, `"limits_clamped":true`) {
		t.Fatalf("get_pod_logs missing untrusted wrapper or clamping: %s", logTxt)
	}
	if !strings.Contains(logTxt, "tail=200 since=3600") {
		t.Fatalf("get_pod_logs did not clamp query params: %s", logTxt)
	}
	for _, secret := range []string{"leakedbearer999", "eyJhbGciOi"} {
		if strings.Contains(logTxt, secret) {
			t.Fatalf("get_pod_logs leaked secret %q: %s", secret, logTxt)
		}
	}

	badResp := callRPC(t, inW, scan, 6, "tools/call", map[string]any{
		"name":      "describe_pod",
		"arguments": map[string]any{"namespace": "../kube-system", "pod": "secret"},
	})
	badTxt, badErr := extractToolJSON(t, badResp)
	if !badErr || !strings.Contains(badTxt, "RFC 1123") {
		t.Fatalf("expected RFC 1123 rejection, got isErr=%v txt=%s", badErr, badTxt)
	}

	lwResp := callRPC(t, inW, scan, 7, "tools/call", map[string]any{
		"name":      "list_workloads",
		"arguments": map[string]any{"namespace": "proj-pn", "kind": "deployments"},
	})
	lwTxt, lwErr := extractToolJSON(t, lwResp)
	if lwErr || !strings.Contains(lwTxt, `"backend"`) {
		t.Fatalf("list_workloads failed: isErr=%v txt=%s", lwErr, lwTxt)
	}

	evResp := callRPC(t, inW, scan, 8, "tools/call", map[string]any{
		"name":      "get_events",
		"arguments": map[string]any{"namespace": "proj-pn", "limit": 500},
	})
	evTxt, evErr := extractToolJSON(t, evResp)
	if evErr || !strings.Contains(evTxt, `"limit_clamped":true`) || strings.Contains(evTxt, "AIzaSySecretKey") {
		t.Fatalf("get_events unexpected: isErr=%v txt=%s", evErr, evTxt)
	}

	fxResp := callRPC(t, inW, scan, 9, "tools/call", map[string]any{
		"name":      "get_flux_status",
		"arguments": map[string]any{},
	})
	fxTxt, fxErr := extractToolJSON(t, fxResp)
	if fxErr || !strings.Contains(fxTxt, `"Kustomization"`) {
		t.Fatalf("get_flux_status failed: isErr=%v txt=%s", fxErr, fxTxt)
	}

	mk.mu.Lock()
	mk.validToken = "ROTATED_NEW_TOKEN_ABC"
	mk.mu.Unlock()
	unauthResp := callRPC(t, inW, scan, 10, "tools/call", map[string]any{
		"name":      "cluster_overview",
		"arguments": map[string]any{},
	})
	unauthTxt, unauthErr := extractToolJSON(t, unauthResp)
	if !unauthErr || !strings.Contains(unauthTxt, "refresh-kubeconfig.sh") {
		t.Fatalf("expected 401 refresh hint, got isErr=%v txt=%s", unauthErr, unauthTxt)
	}

	writeMockKubeconfig(t, ts, kcfgPath, "ROTATED_NEW_TOKEN_ABC")
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload after rotation: %v", err)
	}
	reloadedResp := callRPC(t, inW, scan, 11, "tools/call", map[string]any{
		"name":      "cluster_overview",
		"arguments": map[string]any{},
	})
	reloadedTxt, reloadedErr := extractToolJSON(t, reloadedResp)
	if reloadedErr || !strings.Contains(reloadedTxt, `"nodes_ready":1`) {
		t.Fatalf("cluster_overview after reload failed: isErr=%v txt=%s", reloadedErr, reloadedTxt)
	}

	_ = inW.Close()
	_ = outR.Close()
	_ = outW.Close()
	cancel()
	select {
	case err := <-bridgeDone:
		if err != nil && err != context.Canceled {
			t.Fatalf("ConnectUnix returned unexpected error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("ConnectUnix did not exit cleanly")
	}
	select {
	case err := <-srvErr:
		if err != nil {
			t.Fatalf("ServeUnix returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("ServeUnix did not exit cleanly")
	}

	if mk.nonGetSeen != 0 {
		t.Fatalf("non-GET requests seen: %d", mk.nonGetSeen)
	}
	if mk.maxInFlight > kube.MaxConcurrent {
		t.Fatalf("max in-flight %d exceeded limit %d", mk.maxInFlight, kube.MaxConcurrent)
	}
	auditText := auditBuf.String()
	for _, secret := range []string{"TEST_KUBE_BEARER_TOKEN_SECRET_XYZ", "ROTATED_NEW_TOKEN_ABC"} {
		if strings.Contains(auditText, secret) {
			t.Fatalf("audit log leaked bearer token %q", secret)
		}
	}
}

func TestServeUnixShutdownWithIdleClient(t *testing.T) {
	mk := &mockKube{validToken: "tok"}
	ts := httptest.NewTLSServer(mk.handler())
	defer ts.Close()

	dir := t.TempDir()
	kcfgPath := filepath.Join(dir, "kubeconfig")
	sockDir, err := os.MkdirTemp("/tmp", "nmcp-*")
	if err != nil {
		t.Fatalf("mkdirtemp socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "mcp.sock")
	writeMockKubeconfig(t, ts, kcfgPath, mk.validToken)

	store, err := config.NewStore(kcfgPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	srv := mcp.NewServer(tools.NewRegistry(kube.NewClient(store)), audit.NewLogger(io.Discard))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srvErr := make(chan error, 1)
	go func() {
		srvErr <- srv.ServeUnix(ctx, sockPath, 0o600)
	}()

	ready := false
	for i := 0; i < 100; i++ {
		if _, statErr := os.Stat(sockPath); statErr == nil {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("socket not created")
	}

	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial unix: %v", err)
	}
	defer conn.Close()

	cancel()
	select {
	case err := <-srvErr:
		if err != nil {
			t.Fatalf("ServeUnix returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ServeUnix hung on idle client connection during shutdown")
	}
}

func TestConnectUnixLazyBridgeWhenDaemonStoppedThenStarted(t *testing.T) {
	mk := &mockKube{validToken: "lazy-tok"}
	ts := httptest.NewTLSServer(mk.handler())
	defer ts.Close()

	dir := t.TempDir()
	kcfgPath := filepath.Join(dir, "kubeconfig")
	writeMockKubeconfig(t, ts, kcfgPath, mk.validToken)

	sockDir, err := os.MkdirTemp("/tmp", "nmcp-*")
	if err != nil {
		t.Fatalf("mkdirtemp socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	sockPath := filepath.Join(sockDir, "mcp.sock")

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inR.Close()
	defer outR.Close()
	defer outW.Close()

	bridgeCtx, bridgeCancel := context.WithCancel(context.Background())
	defer bridgeCancel()
	bridgeDone := make(chan error, 1)
	go func() {
		bridgeDone <- mcp.ConnectUnix(bridgeCtx, sockPath, inR, outW)
	}()

	scan := bufio.NewScanner(outR)
	scan.Buffer(make([]byte, 0, 64*1024), 2<<20)

	initResp := callRPC(t, inW, scan, 1, "initialize", map[string]any{"protocolVersion": "2024-11-05"})
	if initResp["error"] != nil {
		t.Fatalf("initialize error while daemon stopped: %+v", initResp["error"])
	}

	listResp := callRPC(t, inW, scan, 2, "tools/list", nil)
	toolDefs := listResp["result"].(map[string]any)["tools"].([]any)
	if len(toolDefs) != 6 {
		t.Fatalf("expected 6 tools while daemon stopped, got %d", len(toolDefs))
	}

	stoppedResp := callRPC(t, inW, scan, 3, "tools/call", map[string]any{
		"name":      "cluster_overview",
		"arguments": map[string]any{},
	})
	stoppedTxt, isErr := extractToolJSON(t, stoppedResp)
	if !isErr || !strings.Contains(stoppedTxt, "mcp-session.sh start") || strings.Contains(stoppedTxt, "ALLOW_CLUSTER_CLI") {
		t.Fatalf("expected stopped-daemon MCP hint without ALLOW_CLUSTER_CLI, got isErr=%v txt=%s", isErr, stoppedTxt)
	}

	store, err := config.NewStore(kcfgPath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	srv := mcp.NewServer(tools.NewRegistry(kube.NewClient(store)), audit.NewLogger(io.Discard))
	srvCtx, srvCancel := context.WithCancel(context.Background())
	srvErr := make(chan error, 1)
	go func() {
		srvErr <- srv.ServeUnix(srvCtx, sockPath, 0o600)
	}()

	ready := false
	for i := 0; i < 100; i++ {
		if _, statErr := os.Stat(sockPath); statErr == nil {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("socket not created")
	}

	liveResp := callRPC(t, inW, scan, 4, "tools/call", map[string]any{
		"name":      "cluster_overview",
		"arguments": map[string]any{},
	})
	liveTxt, isErr := extractToolJSON(t, liveResp)
	if isErr || !strings.Contains(liveTxt, `"nodes_ready":1`) {
		t.Fatalf("expected live cluster_overview after daemon start, got isErr=%v txt=%s", isErr, liveTxt)
	}

	srvCancel()
	select {
	case err := <-srvErr:
		if err != nil {
			t.Fatalf("ServeUnix returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ServeUnix did not stop cleanly")
	}

	afterStopResp := callRPC(t, inW, scan, 5, "tools/call", map[string]any{
		"name":      "cluster_overview",
		"arguments": map[string]any{},
	})
	afterStopTxt, isErr := extractToolJSON(t, afterStopResp)
	if !isErr || !strings.Contains(afterStopTxt, "mcp-session.sh start") {
		t.Fatalf("expected stopped-daemon hint after daemon shutdown, got isErr=%v txt=%s", isErr, afterStopTxt)
	}

	_ = inW.Close()
	select {
	case err := <-bridgeDone:
		if err != nil {
			t.Fatalf("ConnectUnix returned error on EOF: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ConnectUnix did not exit after stdin EOF")
	}
}
