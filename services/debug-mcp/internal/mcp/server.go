package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"nacfson_pipeline/services/debug-mcp/internal/audit"
	"nacfson_pipeline/services/debug-mcp/internal/tools"
)

const (
	DefaultProtocolVersion = "2024-11-05"
	MaxMessageBytes        = 1 << 20
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Server struct {
	registry *tools.Registry
	auditor  *audit.Logger
}

func NewServer(registry *tools.Registry, auditor *audit.Logger) *Server {
	return &Server{
		registry: registry,
		auditor:  auditor,
	}
}

func (s *Server) HandleRequest(ctx context.Context, req *Request) *Response {
	hasID := len(bytesTrimSpace(req.ID)) > 0 && string(bytesTrimSpace(req.ID)) != "null"

	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		if !hasID {
			return nil
		}
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: -32600, Message: "unsupported jsonrpc version"},
		}
	}

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		if len(req.Params) > 0 {
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return &Response{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &RPCError{Code: -32602, Message: "invalid initialize params"},
				}
			}
		}
		version := DefaultProtocolVersion
		if params.ProtocolVersion != "" {
			version = params.ProtocolVersion
		}
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": version,
				"capabilities": map[string]any{
					"tools": map[string]any{
						"listChanged": false,
					},
				},
				"serverInfo": map[string]any{
					"name":    "nacfson-mcp",
					"version": "1.0.0",
				},
			},
		}
	case "notifications/initialized", "notifications/cancelled":
		return nil
	case "ping":
		if !hasID {
			return nil
		}
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}
	case "tools/list":
		if !hasID {
			return nil
		}
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": s.registry.Definitions(),
			},
		}
	case "tools/call":
		if !hasID {
			return nil
		}
		return s.handleToolCall(ctx, req)
	default:
		if !hasID || strings.HasPrefix(req.Method, "notifications/") {
			return nil
		}
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
		}
	}
}

func (s *Server) handleToolCall(ctx context.Context, req *Request) *Response {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Name == "" {
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: -32602, Message: "invalid tools/call params"},
		}
	}

	started := time.Now()
	payload, meta, err := s.registry.Call(ctx, params.Name, params.Arguments)
	durMs := time.Since(started).Milliseconds()

	if err != nil {
		errPayload, _ := json.Marshal(map[string]any{
			"error": err.Error(),
		})
		s.auditor.Log(audit.Entry{
			Tool:          params.Name,
			Namespace:     meta.Namespace,
			Target:        meta.Target,
			DurationMs:    durMs,
			Status:        "error",
			BytesOut:      len(errPayload),
			RedactedKinds: meta.RedactedKinds,
			Error:         err.Error(),
		})
		return &Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{
					{
						"type": "text",
						"text": string(errPayload),
					},
				},
				"isError": true,
			},
		}
	}

	s.auditor.Log(audit.Entry{
		Tool:          params.Name,
		Namespace:     meta.Namespace,
		Target:        meta.Target,
		DurationMs:    durMs,
		Status:        "ok",
		BytesOut:      len(payload),
		RedactedKinds: meta.RedactedKinds,
	})
	return &Response{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": string(payload),
				},
			},
			"isError": false,
		},
	}
}

func (s *Server) ServeStream(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxMessageBytes)

	var (
		writeMu sync.Mutex
		wg      sync.WaitGroup
	)
	writeResp := func(resp *Response) {
		if resp == nil {
			return
		}
		line, err := json.Marshal(resp)
		if err != nil {
			return
		}
		line = append(line, '\n')
		writeMu.Lock()
		_, _ = out.Write(line)
		writeMu.Unlock()
	}

	for scanner.Scan() {
		line := bytesTrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			writeResp(&Response{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &RPCError{Code: -32700, Message: "parse error"},
			})
			continue
		}
		wg.Add(1)
		go func(r Request) {
			defer wg.Done()
			resp := s.HandleRequest(ctx, &r)
			writeResp(resp)
		}(req)
	}
	wg.Wait()
	return scanner.Err()
}

func (s *Server) ServeUnix(ctx context.Context, socketPath string, socketMode os.FileMode) error {
	if socketPath == "" {
		return errors.New("socket path is required")
	}
	if _, err := os.Stat(socketPath); err == nil {
		if err := os.Remove(socketPath); err != nil {
			return fmt.Errorf("remove stale socket %s: %w", socketPath, err)
		}
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, socketMode); err != nil {
		_ = ln.Close()
		_ = os.Remove(socketPath)
		return fmt.Errorf("chmod socket %s: %w", socketPath, err)
	}

	var wg sync.WaitGroup
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			continue
		}
		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			done := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					_ = c.Close()
				case <-done:
				}
			}()
			defer close(done)
			defer c.Close()
			_ = s.ServeStream(ctx, c, c)
		}(conn)
	}
	wg.Wait()
	_ = os.Remove(socketPath)
	return nil
}

func ConnectUnix(ctx context.Context, socketPath string, in io.Reader, out io.Writer) error {
	if socketPath == "" {
		return errors.New("socket path is required")
	}
	defs := tools.NewRegistry(nil).Definitions()
	lines := make(chan []byte)
	scanErrCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64*1024), MaxMessageBytes)
		for scanner.Scan() {
			cp := append([]byte(nil), scanner.Bytes()...)
			select {
			case lines <- cp:
			case <-ctx.Done():
				scanErrCh <- nil
				return
			}
		}
		close(lines)
		scanErrCh <- scanner.Err()
	}()

	var (
		writeMu sync.Mutex
		wg      sync.WaitGroup
	)
	writeLine := func(b []byte) {
		buf := make([]byte, len(b)+1)
		copy(buf, b)
		buf[len(b)] = '\n'
		writeMu.Lock()
		_, _ = out.Write(buf)
		writeMu.Unlock()
	}
	writeResp := func(resp *Response) {
		if resp == nil {
			return
		}
		b, err := json.Marshal(resp)
		if err != nil {
			return
		}
		writeLine(b)
	}

loop:
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case raw, ok := <-lines:
			if !ok {
				break loop
			}
			trimmed := bytesTrimSpace(raw)
			if len(trimmed) == 0 {
				continue
			}
			var req Request
			if err := json.Unmarshal(trimmed, &req); err != nil {
				writeResp(&Response{
					JSONRPC: "2.0",
					ID:      json.RawMessage("null"),
					Error:   &RPCError{Code: -32700, Message: "parse error"},
				})
				continue
			}
			hasID := len(bytesTrimSpace(req.ID)) > 0 && string(bytesTrimSpace(req.ID)) != "null"
			if req.JSONRPC != "" && req.JSONRPC != "2.0" {
				if hasID {
					writeResp(&Response{
						JSONRPC: "2.0",
						ID:      req.ID,
						Error:   &RPCError{Code: -32600, Message: "unsupported jsonrpc version"},
					})
				}
				continue
			}
			switch req.Method {
			case "initialize", "notifications/initialized", "notifications/cancelled", "ping":
				localSrv := &Server{}
				writeResp(localSrv.HandleRequest(ctx, &req))
			case "tools/list":
				if hasID {
					writeResp(&Response{
						JSONRPC: "2.0",
						ID:      req.ID,
						Result: map[string]any{
							"tools": defs,
						},
					})
				}
			case "tools/call":
				if !hasID {
					continue
				}
				lineCopy := append([]byte(nil), trimmed...)
				reqID := append(json.RawMessage(nil), req.ID...)
				wg.Add(1)
				go func() {
					defer wg.Done()
					respBytes, fallback := callUnixTool(ctx, socketPath, reqID, lineCopy)
					if len(respBytes) > 0 {
						writeLine(respBytes)
					} else if fallback != nil {
						writeResp(fallback)
					}
				}()
			default:
				if !hasID || strings.HasPrefix(req.Method, "notifications/") {
					continue
				}
				writeResp(&Response{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error:   &RPCError{Code: -32601, Message: fmt.Sprintf("method not found: %s", req.Method)},
				})
			}
		}
	}
	wg.Wait()
	return <-scanErrCh
}

func callUnixTool(ctx context.Context, socketPath string, reqID json.RawMessage, reqLine []byte) ([]byte, *Response) {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, daemonUnavailableResponse(reqID, err)
	}
	defer conn.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()

	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	outBuf := make([]byte, len(reqLine)+1)
	copy(outBuf, reqLine)
	outBuf[len(reqLine)] = '\n'
	if _, err := conn.Write(outBuf); err != nil {
		return nil, daemonBridgeErrorResponse(reqID, err)
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), MaxMessageBytes)
	if !scanner.Scan() {
		scanErr := scanner.Err()
		if scanErr == nil {
			scanErr = io.EOF
		}
		return nil, daemonBridgeErrorResponse(reqID, scanErr)
	}
	return append([]byte(nil), scanner.Bytes()...), nil
}

func daemonUnavailableResponse(reqID json.RawMessage, dialErr error) *Response {
	payload, _ := json.Marshal(map[string]any{
		"error":     "nacfson-mcp daemon is not running or socket is unreachable",
		"detail":    dialErr.Error(),
		"next_step": "Ask the operator to start a temporary session with: sudo ./scripts/mcp/mcp-session.sh start <gke|local-k3s> --ttl 1h (see .agents/skills/debug-mcp-session/SKILL.md), then retry this MCP tool. Do not fall back to kubectl or cloud CLIs.",
	})
	return &Response{
		JSONRPC: "2.0",
		ID:      reqID,
		Result: map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": string(payload),
				},
			},
			"isError": true,
		},
	}
}

func daemonBridgeErrorResponse(reqID json.RawMessage, err error) *Response {
	payload, _ := json.Marshal(map[string]any{
		"error":     "nacfson-mcp socket request failed",
		"detail":    err.Error(),
		"next_step": "Check ./scripts/mcp/mcp-session.sh status and ask the operator to restart the temporary session if needed.",
	})
	return &Response{
		JSONRPC: "2.0",
		ID:      reqID,
		Result: map[string]any{
			"content": []map[string]any{
				{
					"type": "text",
					"text": string(payload),
				},
			},
			"isError": true,
		},
	}
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
