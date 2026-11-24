package config

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Resolved struct {
	Server    string
	Token     string
	RootCAs   *x509.CertPool
	TLSConfig *tls.Config
	Client    *http.Client
	Transport *http.Transport
	ModTime   time.Time
}

type Store struct {
	path    string
	mu      sync.RWMutex
	current *Resolved
}

func NewStore(path string) (*Store, error) {
	resolved, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Store{
		path:    path,
		current: resolved,
	}, nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Get() (*Resolved, error) {
	info, err := os.Stat(s.path)
	if err != nil {
		return nil, fmt.Errorf("stat kubeconfig: %w", err)
	}
	s.mu.RLock()
	cur := s.current
	s.mu.RUnlock()
	if cur != nil && info.ModTime().Equal(cur.ModTime) {
		return cur, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	info, err = os.Stat(s.path)
	if err != nil {
		return nil, fmt.Errorf("stat kubeconfig: %w", err)
	}
	if s.current != nil && info.ModTime().Equal(s.current.ModTime) {
		return s.current, nil
	}
	next, err := Load(s.path)
	if err != nil {
		return nil, err
	}
	if s.current != nil && s.current.Transport != nil {
		s.current.Transport.CloseIdleConnections()
	}
	s.current = next
	return next, nil
}

func (s *Store) Reload() error {
	next, err := Load(s.path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.current
	s.current = next
	s.mu.Unlock()
	if old != nil && old.Transport != nil {
		old.Transport.CloseIdleConnections()
	}
	return nil
}

func (s *Store) Close() {
	s.mu.Lock()
	cur := s.current
	s.current = nil
	s.mu.Unlock()
	if cur != nil && cur.Transport != nil {
		cur.Transport.CloseIdleConnections()
	}
}

func Load(path string) (*Resolved, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat kubeconfig: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read kubeconfig: %w", err)
	}
	resolved, err := ParseBytes(raw)
	if err != nil {
		return nil, err
	}
	resolved.ModTime = info.ModTime()
	return resolved, nil
}

func ParseBytes(raw []byte) (*Resolved, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, errors.New("kubeconfig is empty")
	}
	var root map[string]any
	var err error
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal([]byte(trimmed), &root); err != nil {
			return nil, fmt.Errorf("parse json kubeconfig: %w", err)
		}
	} else {
		root, err = parseMinimalYAML(trimmed)
		if err != nil {
			return nil, fmt.Errorf("parse yaml kubeconfig: %w", err)
		}
	}
	if err := validateSafeTree(root); err != nil {
		return nil, err
	}
	server, caData, token, err := extractCredentials(root)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("kubeconfig server must be a valid https:// URL")
	}
	pemBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(caData))
	if err != nil {
		return nil, fmt.Errorf("decode certificate-authority-data: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("certificate-authority-data contains no valid PEM certificates")
	}
	tlsCfg := &tls.Config{
		RootCAs:    pool,
		MinVersion: tls.VersionTLS12,
	}
	tr := &http.Transport{
		TLSClientConfig:       tlsCfg,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 10 * time.Second,
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Resolved{
		Server:    strings.TrimRight(server, "/"),
		Token:     token,
		RootCAs:   pool,
		TLSConfig: tlsCfg,
		Client:    client,
		Transport: tr,
	}, nil
}

func validateSafeTree(node any) error {
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			switch k {
			case "exec", "auth-provider", "client-key-data", "client-key", "client-certificate-data", "client-certificate", "tokenFile":
				return fmt.Errorf("forbidden kubeconfig field: %s", k)
			case "insecure-skip-tls-verify":
				if b, ok := val.(bool); ok && b {
					return errors.New("insecure-skip-tls-verify: true is forbidden")
				}
				if s, ok := val.(string); ok && strings.EqualFold(strings.TrimSpace(s), "true") {
					return errors.New("insecure-skip-tls-verify: true is forbidden")
				}
			}
			if err := validateSafeTree(val); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := validateSafeTree(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractCredentials(root map[string]any) (string, string, string, error) {
	currentCtx, _ := root["current-context"].(string)
	targetCluster := ""
	targetUser := ""

	if contexts, ok := root["contexts"].([]any); ok {
		if currentCtx != "" {
			for _, item := range contexts {
				m, ok := item.(map[string]any)
				if !ok {
					continue
				}
				name, _ := m["name"].(string)
				if name == currentCtx {
					if ctxMap, ok := m["context"].(map[string]any); ok {
						targetCluster, _ = ctxMap["cluster"].(string)
						targetUser, _ = ctxMap["user"].(string)
					}
					break
				}
			}
			if targetCluster == "" || targetUser == "" {
				return "", "", "", fmt.Errorf("current-context %q not found cleanly in kubeconfig", currentCtx)
			}
		} else if len(contexts) == 1 {
			if m, ok := contexts[0].(map[string]any); ok {
				if ctxMap, ok := m["context"].(map[string]any); ok {
					targetCluster, _ = ctxMap["cluster"].(string)
					targetUser, _ = ctxMap["user"].(string)
				}
			}
		}
	}

	clusters, ok := root["clusters"].([]any)
	if !ok || len(clusters) == 0 {
		return "", "", "", errors.New("kubeconfig has no clusters")
	}
	var selectedCluster map[string]any
	for _, item := range clusters {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if targetCluster == "" && len(clusters) == 1 {
			selectedCluster, _ = m["cluster"].(map[string]any)
			break
		}
		if name == targetCluster {
			selectedCluster, _ = m["cluster"].(map[string]any)
			break
		}
	}
	if selectedCluster == nil {
		return "", "", "", errors.New("kubeconfig cluster selection could not be resolved")
	}
	server, _ := selectedCluster["server"].(string)
	caData, _ := selectedCluster["certificate-authority-data"].(string)
	if server == "" {
		return "", "", "", errors.New("kubeconfig cluster server is required")
	}
	if caData == "" {
		return "", "", "", errors.New("kubeconfig cluster certificate-authority-data is required")
	}

	users, ok := root["users"].([]any)
	if !ok || len(users) == 0 {
		return "", "", "", errors.New("kubeconfig has no users")
	}
	var selectedUser map[string]any
	for _, item := range users {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		if targetUser == "" && len(users) == 1 {
			selectedUser, _ = m["user"].(map[string]any)
			break
		}
		if name == targetUser {
			selectedUser, _ = m["user"].(map[string]any)
			break
		}
	}
	if selectedUser == nil {
		return "", "", "", errors.New("kubeconfig user selection could not be resolved")
	}
	token, _ := selectedUser["token"].(string)
	if strings.TrimSpace(token) == "" {
		return "", "", "", errors.New("kubeconfig user token is required")
	}
	return server, caData, token, nil
}

type yamlLine struct {
	indent  int
	content string
}

func parseMinimalYAML(src string) (map[string]any, error) {
	rawLines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	lines := make([]yamlLine, 0, len(rawLines))
	for i, raw := range rawLines {
		cleaned := stripYAMLComment(raw)
		if strings.TrimSpace(cleaned) == "" {
			continue
		}
		if trimmedDoc := strings.TrimSpace(cleaned); trimmedDoc == "---" || trimmedDoc == "..." {
			continue
		}
		indent := 0
		for indent < len(cleaned) && cleaned[indent] == ' ' {
			indent++
		}
		if indent < len(cleaned) && cleaned[indent] == '\t' {
			return nil, fmt.Errorf("line %d uses tab indentation", i+1)
		}
		lines = append(lines, yamlLine{
			indent:  indent,
			content: strings.TrimSpace(cleaned[indent:]),
		})
	}
	if len(lines) == 0 {
		return nil, errors.New("empty YAML document")
	}
	val, next, err := parseYAMLMap(lines, 0, lines[0].indent)
	if err != nil {
		return nil, err
	}
	if next != len(lines) {
		return nil, fmt.Errorf("unexpected trailing YAML content at index %d", next)
	}
	return val, nil
}

func stripYAMLComment(line string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if escaped {
			escaped = false
			continue
		}
		if inDouble {
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				inDouble = false
			}
			continue
		}
		if inSingle {
			if c == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if c == '\'' {
			inSingle = true
			continue
		}
		if c == '"' {
			inDouble = true
			continue
		}
		if c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}

func splitKeyValue(text string) (string, string, bool) {
	inSingle := false
	inDouble := false
	escaped := false
	for i := 0; i < len(text); i++ {
		c := text[i]
		if escaped {
			escaped = false
			continue
		}
		if inDouble {
			if c == '\\' {
				escaped = true
			} else if c == '"' {
				inDouble = false
			}
			continue
		}
		if inSingle {
			if c == '\'' {
				if i+1 < len(text) && text[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
			continue
		}
		if c == '\'' {
			inSingle = true
			continue
		}
		if c == '"' {
			inDouble = true
			continue
		}
		if c == ':' && (i+1 == len(text) || text[i+1] == ' ' || text[i+1] == '\t') {
			key := unquoteYAMLScalar(strings.TrimSpace(text[:i]))
			val := strings.TrimSpace(text[i+1:])
			return key, val, true
		}
	}
	return "", "", false
}

func unquoteYAMLScalar(val string) string {
	val = strings.TrimSpace(val)
	if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
		if u, err := strconv.Unquote(val); err == nil {
			return u
		}
		return val[1 : len(val)-1]
	}
	if len(val) >= 2 && val[0] == '\'' && val[len(val)-1] == '\'' {
		return strings.ReplaceAll(val[1:len(val)-1], "''", "'")
	}
	return val
}

func parseYAMLScalar(val string) any {
	trimmed := strings.TrimSpace(val)
	if trimmed == "{}" {
		return map[string]any{}
	}
	if trimmed == "[]" {
		return []any{}
	}
	return unquoteYAMLScalar(trimmed)
}

func isListItem(content string) (string, int, bool) {
	if content == "-" {
		return "", 2, true
	}
	if strings.HasPrefix(content, "- ") {
		rest := content[1:]
		spaces := 0
		for spaces < len(rest) && rest[spaces] == ' ' {
			spaces++
		}
		return strings.TrimSpace(rest), 1 + spaces, true
	}
	return "", 0, false
}

func parseYAMLMap(lines []yamlLine, start int, mapIndent int) (map[string]any, int, error) {
	res := make(map[string]any)
	i := start
	for i < len(lines) {
		ln := lines[i]
		if ln.indent < mapIndent {
			break
		}
		if _, _, ok := isListItem(ln.content); ok && ln.indent == mapIndent {
			break
		}
		if ln.indent > mapIndent {
			return nil, i, fmt.Errorf("unexpected indentation %d at content %q", ln.indent, ln.content)
		}
		key, val, ok := splitKeyValue(ln.content)
		if !ok {
			return nil, i, fmt.Errorf("expected key: value at %q", ln.content)
		}
		i++
		if val != "" {
			res[key] = parseYAMLScalar(val)
			continue
		}
		if i < len(lines) {
			next := lines[i]
			if _, _, isList := isListItem(next.content); isList && next.indent >= mapIndent {
				listVal, nextIdx, err := parseYAMLList(lines, i, next.indent)
				if err != nil {
					return nil, nextIdx, err
				}
				res[key] = listVal
				i = nextIdx
				continue
			}
			if next.indent > mapIndent {
				childMap, nextIdx, err := parseYAMLMap(lines, i, next.indent)
				if err != nil {
					return nil, nextIdx, err
				}
				res[key] = childMap
				i = nextIdx
				continue
			}
		}
		res[key] = nil
	}
	return res, i, nil
}

func parseYAMLList(lines []yamlLine, start int, listIndent int) ([]any, int, error) {
	var res []any
	i := start
	for i < len(lines) {
		ln := lines[i]
		if ln.indent < listIndent {
			break
		}
		if ln.indent > listIndent {
			return nil, i, fmt.Errorf("unexpected list indentation %d at %q", ln.indent, ln.content)
		}
		rest, offset, ok := isListItem(ln.content)
		if !ok {
			break
		}
		itemIndent := ln.indent + offset
		if rest == "" {
			i++
			if i < len(lines) && lines[i].indent > listIndent {
				if _, _, isList := isListItem(lines[i].content); isList {
					subList, nextIdx, err := parseYAMLList(lines, i, lines[i].indent)
					if err != nil {
						return nil, nextIdx, err
					}
					res = append(res, subList)
					i = nextIdx
					continue
				}
				subMap, nextIdx, err := parseYAMLMap(lines, i, lines[i].indent)
				if err != nil {
					return nil, nextIdx, err
				}
				res = append(res, subMap)
				i = nextIdx
				continue
			}
			res = append(res, nil)
			continue
		}
		if k, v, isKV := splitKeyValue(rest); isKV {
			itemMap := make(map[string]any)
			i++
			if v != "" {
				itemMap[k] = parseYAMLScalar(v)
			} else if i < len(lines) {
				next := lines[i]
				if _, _, subIsList := isListItem(next.content); subIsList && next.indent >= itemIndent {
					subList, nextIdx, err := parseYAMLList(lines, i, next.indent)
					if err != nil {
						return nil, nextIdx, err
					}
					itemMap[k] = subList
					i = nextIdx
				} else if next.indent > itemIndent {
					subMap, nextIdx, err := parseYAMLMap(lines, i, next.indent)
					if err != nil {
						return nil, nextIdx, err
					}
					itemMap[k] = subMap
					i = nextIdx
				} else {
					itemMap[k] = nil
				}
			} else {
				itemMap[k] = nil
			}
			if i < len(lines) && lines[i].indent == itemIndent {
				restMap, nextIdx, err := parseYAMLMap(lines, i, itemIndent)
				if err != nil {
					return nil, nextIdx, err
				}
				for rk, rv := range restMap {
					itemMap[rk] = rv
				}
				i = nextIdx
			}
			res = append(res, itemMap)
			continue
		}
		res = append(res, parseYAMLScalar(rest))
		i++
	}
	return res, i, nil
}
