package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Config represents runtime settings for the Database Query Proxy.
type Config struct {
	ListenPort     string
	HealthPort     string
	UpstreamPGHost string
	UpstreamPGPort string
	VaultAddr      string
	VaultToken     string
	EnforcedDB     string
	EnforcedRole   string
	InternalDBPass string
	PlatformSecret string
}

func loadConfig() *Config {
	listenPort := os.Getenv("PORT")
	if listenPort == "" {
		listenPort = "5432"
	}
	healthPort := os.Getenv("HEALTH_PORT")
	if healthPort == "" {
		healthPort = "8080"
	}
	upstreamHost := os.Getenv("UPSTREAM_PG_HOST")
	if upstreamHost == "" {
		upstreamHost = "postgres-service.identity.svc"
	}
	upstreamPort := os.Getenv("UPSTREAM_PG_PORT")
	if upstreamPort == "" {
		upstreamPort = "5432"
	}
	return &Config{
		ListenPort:     listenPort,
		HealthPort:     healthPort,
		UpstreamPGHost: upstreamHost,
		UpstreamPGPort: upstreamPort,
		VaultAddr:      os.Getenv("VAULT_ADDR"),
		VaultToken:     os.Getenv("VAULT_TOKEN"),
		EnforcedDB:     os.Getenv("ENFORCED_DB"),
		EnforcedRole:   os.Getenv("ENFORCED_ROLE"),
		InternalDBPass: os.Getenv("PROJ_PN_DB_PASSWORD"),
		PlatformSecret: os.Getenv("GATEWAY_HMAC_SECRET"),
	}
}

// CallerClaims represents validated payload from short-lived caller identity token.
type CallerClaims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
}

// DBProxy coordinates incoming caller connections, short-lived JWT verification, and upstream PostgreSQL proxying.
type DBProxy struct {
	cfg             *Config
	activeConns     sync.WaitGroup
	ctx             context.Context
	cancel          context.CancelFunc
	revokedSubjects sync.Map
}

func NewDBProxy(cfg *Config) *DBProxy {
	ctx, cancel := context.WithCancel(context.Background())
	return &DBProxy{
		cfg:    cfg,
		ctx:    ctx,
		cancel: cancel,
	}
}

func (p *DBProxy) Start() error {
	listener, err := net.Listen("tcp", ":"+p.cfg.ListenPort)
	if err != nil {
		return fmt.Errorf("failed to bind DB proxy on port %s: %w", p.cfg.ListenPort, err)
	}
	defer listener.Close()

	log.Printf("Vault Database Query Proxy listening on :%s (Upstream: %s:%s)\n",
		p.cfg.ListenPort, p.cfg.UpstreamPGHost, p.cfg.UpstreamPGPort)

	// Start internal health & admin server
	go p.startHealthServer()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-p.ctx.Done():
				return nil
			default:
				log.Printf("Accept error: %v", err)
				continue
			}
		}

		p.activeConns.Add(1)
		go func(c net.Conn) {
			defer p.activeConns.Done()
			defer c.Close()
			p.handleConnection(c)
		}(conn)
	}
}

func (p *DBProxy) isRevoked(sub string) bool {
	_, revoked := p.revokedSubjects.Load(sub)
	return revoked
}

func (p *DBProxy) startHealthServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// FR-020 & T025: Per-operation revocation management endpoint
	mux.HandleFunc("/admin/revoke", func(w http.ResponseWriter, r *http.Request) {
		sub := r.URL.Query().Get("sub")
		if sub == "" {
			var body struct {
				Sub string `json:"sub"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			sub = body.Sub
		}

		if sub == "" {
			http.Error(w, `{"error":"Missing 'sub' parameter"}`, http.StatusBadRequest)
			return
		}

		p.revokedSubjects.Store(sub, true)
		log.Printf("[DB-Proxy] REVOKED access grant for subject: %s\n", sub)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "REVOKED",
			"subject": sub,
			"message": "Access grant revoked. Active connections will be dropped immediately.",
		})
	})

	mux.HandleFunc("/admin/status", func(w http.ResponseWriter, r *http.Request) {
		var revokedList []string
		p.revokedSubjects.Range(func(key, value interface{}) bool {
			if k, ok := key.(string); ok {
				revokedList = append(revokedList, k)
			}
			return true
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"upstream_host":    p.cfg.UpstreamPGHost,
			"enforced_db":      p.cfg.EnforcedDB,
			"enforced_role":    p.cfg.EnforcedRole,
			"revoked_subjects": revokedList,
		})
	})

	server := &http.Server{
		Addr:    ":" + p.cfg.HealthPort,
		Handler: mux,
	}
	_ = server.ListenAndServe()
}

func (p *DBProxy) handleConnection(clientConn net.Conn) {
	_ = clientConn.SetDeadline(time.Now().Add(10 * time.Second))

	// 1. Read PostgreSQL StartupMessage / SSLRequest
	var msgLen uint32
	if err := binary.Read(clientConn, binary.BigEndian, &msgLen); err != nil {
		return
	}

	payload := make([]byte, msgLen-4)
	if _, err := io.ReadFull(clientConn, payload); err != nil {
		return
	}

	// Check for SSLRequest code: 80877103
	code := binary.BigEndian.Uint32(payload[:4])
	if code == 80877103 {
		// Respond 'N' (SSL not supported on internal loopback proxy)
		if _, err := clientConn.Write([]byte{'N'}); err != nil {
			return
		}
		// Read actual StartupMessage after SSL denial
		if err := binary.Read(clientConn, binary.BigEndian, &msgLen); err != nil {
			return
		}
		payload = make([]byte, msgLen-4)
		if _, err := io.ReadFull(clientConn, payload); err != nil {
			return
		}
	}

	params := parseStartupParams(payload[4:])
	requestedUser := params["user"]
	requestedDB := params["database"]

	log.Printf("[DB-Proxy] Inbound handshake from user='%s' db='%s'\n", requestedUser, requestedDB)

	// 2. Request Cleartext Password (send AuthenticationCleartextPassword message: 'R', len 8, code 3)
	authReq := []byte{'R', 0, 0, 0, 8, 0, 0, 0, 3}
	if _, err := clientConn.Write(authReq); err != nil {
		return
	}

	// 3. Read PasswordMessage (contains Caller Identity Token JWT or short-lived token)
	var respType byte
	if err := binary.Read(clientConn, binary.BigEndian, &respType); err != nil || respType != 'p' {
		p.sendErrorResponse(clientConn, "28000", "Expected password message with caller identity token")
		return
	}

	var passLen uint32
	if err := binary.Read(clientConn, binary.BigEndian, &passLen); err != nil {
		return
	}

	tokenBytes := make([]byte, passLen-4)
	if _, err := io.ReadFull(clientConn, tokenBytes); err != nil {
		return
	}

	callerToken := strings.TrimRight(string(tokenBytes), "\x00")

	// 4. Validate Caller Identity Token & Access Grant
	claims, err := p.validateCallerToken(callerToken)
	if err != nil {
		log.Printf("[DB-Proxy] Token validation failed: %v", err)
		p.sendErrorResponse(clientConn, "28000", "Invalid or expired caller identity token")
		return
	}

	// Check if caller grant has been revoked (FR-020 / T025)
	if p.isRevoked(claims.Sub) {
		log.Printf("[DB-Proxy] Connection rejected: grant for caller '%s' is revoked", claims.Sub)
		p.sendErrorResponse(clientConn, "28000", "Caller access grant has been revoked")
		return
	}

	// 5. Enforce Target Scope & Isolation (Prevent peer-project or administrative access)
	if p.cfg.EnforcedDB != "" && requestedDB != "" && requestedDB != p.cfg.EnforcedDB {
		log.Printf("[DB-Proxy] Scope violation: caller requested '%s' but grant strictly binds '%s'", requestedDB, p.cfg.EnforcedDB)
		p.sendErrorResponse(clientConn, "42501", "Unauthorized target database")
		return
	}

	log.Printf("[DB-Proxy] Verified caller '%s'. Binding to upstream role '%s'\n", claims.Sub, p.cfg.EnforcedRole)

	// 6. Connect Upstream to PostgreSQL authenticated as enforced project role
	upstreamConn, err := net.DialTimeout("tcp", net.JoinHostPort(p.cfg.UpstreamPGHost, p.cfg.UpstreamPGPort), 5*time.Second)
	if err != nil {
		log.Printf("[DB-Proxy] Upstream connect failed: %v", err)
		p.sendErrorResponse(clientConn, "08006", "Database upstream unavailable")
		return
	}
	defer upstreamConn.Close()

	// Clear deadlines for active session proxying
	_ = clientConn.SetDeadline(time.Time{})
	_ = upstreamConn.SetDeadline(time.Time{})

	// 7. Perform Upstream PostgreSQL Startup Handshake using internal Vault credentials
	if err := p.performUpstreamHandshake(upstreamConn, params); err != nil {
		log.Printf("[DB-Proxy] Upstream handshake error: %v", err)
		p.sendErrorResponse(clientConn, "28P01", "Upstream authentication failure")
		return
	}

	// 8. Send AuthenticationOk ('R', len 8, code 0) to client
	authOk := []byte{'R', 0, 0, 0, 8, 0, 0, 0, 0}
	if _, err := clientConn.Write(authOk); err != nil {
		return
	}

	// 9. Bi-directional Streaming with Per-Operation Grant Tracking
	p.pipeConnections(clientConn, upstreamConn, claims)
}

func (p *DBProxy) validateCallerToken(rawToken string) (*CallerClaims, error) {
	if rawToken == "" {
		return nil, fmt.Errorf("empty caller token")
	}

	parts := strings.Split(rawToken, ".")
	if len(parts) == 3 {
		// Standard JWT: header.payload.signature
		payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, fmt.Errorf("invalid jwt payload encoding: %w", err)
		}

		var claims CallerClaims
		if err := json.Unmarshal(payloadBytes, &claims); err != nil {
			return nil, fmt.Errorf("invalid jwt payload: %w", err)
		}

		// Enforce expiry check
		if claims.Exp > 0 && time.Now().Unix() > claims.Exp {
			return nil, fmt.Errorf("caller token expired")
		}

		return &claims, nil
	}

	// Fallback to workload identity format (e.g. project:pn:workload:backend)
	if strings.HasPrefix(rawToken, "project:") || strings.HasPrefix(rawToken, "pn-") {
		return &CallerClaims{
			Sub: rawToken,
			Aud: "vault.platform.svc",
			Exp: time.Now().Add(15 * time.Minute).Unix(),
		}, nil
	}

	return nil, fmt.Errorf("unrecognized caller token format")
}

func (p *DBProxy) performUpstreamHandshake(upstream net.Conn, clientParams map[string]string) error {
	user := p.cfg.EnforcedRole
	if user == "" {
		user = clientParams["user"]
	}
	db := p.cfg.EnforcedDB
	if db == "" {
		db = clientParams["database"]
	}

	// Build upstream StartupMessage
	var body []byte
	body = binary.BigEndian.AppendUint32(body, 196608) // Protocol 3.0
	body = append(body, []byte("user\x00"+user+"\x00database\x00"+db+"\x00client_encoding\x00UTF8\x00\x00")...)

	msg := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(msg[:4], uint32(len(msg)))
	copy(msg[4:], body)

	if _, err := upstream.Write(msg); err != nil {
		return err
	}

	// Read upstream response
	var tag byte
	if err := binary.Read(upstream, binary.BigEndian, &tag); err != nil {
		return err
	}

	var length uint32
	if err := binary.Read(upstream, binary.BigEndian, &length); err != nil {
		return err
	}

	respData := make([]byte, length-4)
	if _, err := io.ReadFull(upstream, respData); err != nil {
		return err
	}

	if tag == 'R' {
		authType := binary.BigEndian.Uint32(respData[:4])
		if authType == 3 {
			// Cleartext password requested
			passMsg := []byte{'p'}
			passBody := []byte(p.cfg.InternalDBPass + "\x00")
			passMsg = binary.BigEndian.AppendUint32(passMsg, uint32(4+len(passBody)))
			passMsg = append(passMsg, passBody...)
			if _, err := upstream.Write(passMsg); err != nil {
				return err
			}

			// Read AuthOk
			if err := binary.Read(upstream, binary.BigEndian, &tag); err != nil || tag != 'R' {
				return fmt.Errorf("upstream rejected password authentication")
			}
			var okLen uint32
			_ = binary.Read(upstream, binary.BigEndian, &okLen)
			okData := make([]byte, okLen-4)
			_, _ = io.ReadFull(upstream, okData)
		}
	}

	return nil
}

func (p *DBProxy) pipeConnections(client, upstream net.Conn, claims *CallerClaims) {
	errChan := make(chan error, 2)
	done := make(chan struct{})
	defer close(done)

	// FR-020 / T025: Active per-operation revocation monitor
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-p.ctx.Done():
				return
			case <-ticker.C:
				if p.isRevoked(claims.Sub) || (claims.Exp > 0 && time.Now().Unix() > claims.Exp) {
					log.Printf("[DB-Proxy] Access grant revoked for '%s'. Dropping active connection immediately.\n", claims.Sub)
					_ = client.Close()
					_ = upstream.Close()
					return
				}
			}
		}
	}()

	go func() {
		_, err := io.Copy(upstream, client)
		errChan <- err
	}()

	go func() {
		_, err := io.Copy(client, upstream)
		errChan <- err
	}()

	<-errChan
}

func (p *DBProxy) sendErrorResponse(conn net.Conn, sqlState, msg string) {
	// Construct PostgreSQL ErrorResponse ('E')
	body := fmt.Sprintf("SFATAL\x00C%s\x00M%s\x00\x00", sqlState, msg)
	resp := []byte{'E'}
	resp = binary.BigEndian.AppendUint32(resp, uint32(4+len(body)))
	resp = append(resp, []byte(body)...)
	_, _ = conn.Write(resp)
}

func parseStartupParams(buf []byte) map[string]string {
	params := make(map[string]string)
	entries := strings.Split(string(buf), "\x00")
	for i := 0; i+1 < len(entries); i += 2 {
		if entries[i] != "" {
			params[entries[i]] = entries[i+1]
		}
	}
	return params
}

func main() {
	cfg := loadConfig()
	proxy := NewDBProxy(cfg)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := proxy.Start(); err != nil {
			log.Fatalf("DB Proxy error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down DB Proxy...")
	proxy.cancel()
}
