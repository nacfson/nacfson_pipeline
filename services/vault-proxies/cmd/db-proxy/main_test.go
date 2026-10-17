package main

import (
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResolveProjectBindingMultiTenant(t *testing.T) {
	cfg := &Config{
		AllowedProjects: parseAllowedProjects("pn,blog"),
	}
	proxy := NewDBProxy(cfg)

	slug, db, role, err := proxy.resolveProjectBinding(&CallerClaims{Sub: "project:pn:workload:backend"})
	if err != nil {
		t.Fatalf("expected valid binding for pn, got err: %v", err)
	}
	if slug != "pn" || db != "proj_pn" || role != "user_pn" {
		t.Fatalf("unexpected pn binding: slug=%s db=%s role=%s", slug, db, role)
	}

	slug, db, role, err = proxy.resolveProjectBinding(&CallerClaims{Sub: "project:blog:workload:api"})
	if err != nil {
		t.Fatalf("expected valid binding for blog, got err: %v", err)
	}
	if slug != "blog" || db != "proj_blog" || role != "user_blog" {
		t.Fatalf("unexpected blog binding: slug=%s db=%s role=%s", slug, db, role)
	}

	if _, _, _, err := proxy.resolveProjectBinding(&CallerClaims{Sub: "project:unregistered:workload:backend"}); err == nil {
		t.Fatalf("expected unregistered project to be rejected")
	}
}

func TestLivenessVsReadinessProbes(t *testing.T) {
	// 1. When upstream PostgreSQL is unreachable, /healthz (liveness) MUST return 200 OK,
	// while /readyz (readiness) MUST return 503 Service Unavailable.
	cfgDown := &Config{
		UpstreamPGHost:  "127.0.0.1",
		UpstreamPGPort:  "1", // closed port
		AllowedProjects: parseAllowedProjects("pn"),
	}
	proxyDown := NewDBProxy(cfgDown)
	muxDown := proxyDown.buildAdminMux()

	reqHealth := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recHealth := httptest.NewRecorder()
	muxDown.ServeHTTP(recHealth, reqHealth)
	if recHealth.Code != http.StatusOK || recHealth.Body.String() != "OK" {
		t.Fatalf("expected /healthz 200 OK when upstream down, got %d %q", recHealth.Code, recHealth.Body.String())
	}

	reqReady := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	recReady := httptest.NewRecorder()
	muxDown.ServeHTTP(recReady, reqReady)
	if recReady.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected /readyz 503 when upstream down, got %d", recReady.Code)
	}

	// 2. When upstream PostgreSQL TCP listener is reachable, /readyz MUST return 200 READY.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start mock upstream listener: %v", err)
	}
	defer ln.Close()

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	cfgUp := &Config{
		UpstreamPGHost:  "127.0.0.1",
		UpstreamPGPort:  port,
		AllowedProjects: parseAllowedProjects("pn"),
	}
	proxyUp := NewDBProxy(cfgUp)
	muxUp := proxyUp.buildAdminMux()

	recReadyUp := httptest.NewRecorder()
	muxUp.ServeHTTP(recReadyUp, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if recReadyUp.Code != http.StatusOK || recReadyUp.Body.String() != "READY" {
		t.Fatalf("expected /readyz 200 READY when upstream up, got %d %q", recReadyUp.Code, recReadyUp.Body.String())
	}
}

func TestWireHandshakeScopeAndTokenRejection(t *testing.T) {
	cfg := &Config{
		AllowedProjects: parseAllowedProjects("pn"),
	}
	proxy := NewDBProxy(cfg)

	runHandshake := func(user, database, password string) string {
		clientConn, serverConn := net.Pipe()
		defer clientConn.Close()
		go func() {
			defer serverConn.Close()
			proxy.handleConnection(serverConn)
		}()

		_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))
		var body []byte
		body = binary.BigEndian.AppendUint32(body, 196608)
		body = append(body, []byte("user\x00"+user+"\x00database\x00"+database+"\x00\x00")...)
		startup := make([]byte, 4+len(body))
		binary.BigEndian.PutUint32(startup[:4], uint32(len(startup)))
		copy(startup[4:], body)

		if _, err := clientConn.Write(startup); err != nil {
			t.Fatalf("write startup failed: %v", err)
		}

		// Read AuthCleartextPassword request (9 bytes)
		authReq := make([]byte, 9)
		if _, err := io.ReadFull(clientConn, authReq); err != nil {
			t.Fatalf("read authReq failed: %v", err)
		}

		passBody := []byte(password + "\x00")
		passMsg := []byte{'p'}
		passMsg = binary.BigEndian.AppendUint32(passMsg, uint32(4+len(passBody)))
		passMsg = append(passMsg, passBody...)
		if _, err := clientConn.Write(passMsg); err != nil {
			t.Fatalf("write passMsg failed: %v", err)
		}

		resp, _ := io.ReadAll(clientConn)
		return string(resp)
	}

	// Case 1: Invalid token -> SQLSTATE 28000
	outInvalid := runHandshake("project:pn:workload:backend", "proj_pn", "invalid-token")
	if !strings.Contains(outInvalid, "28000") {
		t.Fatalf("expected SQLSTATE 28000 for invalid token, got %q", outInvalid)
	}

	// Case 2: Valid pn caller attempting to access keycloak DB -> SQLSTATE 42501
	outCrossDB := runHandshake("project:pn:workload:backend", "keycloak", "project:pn:workload:backend")
	if !strings.Contains(outCrossDB, "42501") {
		t.Fatalf("expected SQLSTATE 42501 for cross-database access, got %q", outCrossDB)
	}
}
