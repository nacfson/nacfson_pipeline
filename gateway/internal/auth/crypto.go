package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	sealedPrefix    = "v1."
	handoffTTL      = 60 * time.Second
	maxStateAgeSecs = 600 // 10-minute window for PKCE state
)

// HandoffTicket represents a single-use callback ticket issued by Gateway for a Project Backend.
type HandoffTicket struct {
	TicketID      string `json:"ticket_id"`
	Host          string `json:"host"`
	SessionHandle string `json:"session_handle"`
	Subject       string `json:"subject"`
	Issuer        string `json:"issuer"`
	Audience      string `json:"audience"`
	ExpiresAt     int64  `json:"exp"`
}

// TicketStore tracks consumed single-use handoff ticket IDs until their TTL expires.
type TicketStore struct {
	mu       sync.Mutex
	consumed map[string]int64
}

func newTicketStore() *TicketStore {
	return &TicketStore{
		consumed: make(map[string]int64),
	}
}

// deriveAESKey derives a 32-byte AES-256 key from a secret slice using SHA-256.
func deriveAESKey(secret []byte) []byte {
	sum := sha256.Sum256(secret)
	return sum[:]
}

// sealJSON encrypts and authenticates an arbitrary struct using AES-256-GCM.
func sealJSON(v any, secret []byte) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("empty encryption secret")
	}
	plaintext, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(deriveAESKey(secret))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return sealedPrefix + base64.RawURLEncoding.EncodeToString(ciphertext), nil
}

// unsealJSON decrypts an AES-256-GCM sealed payload using primary or previous secret.
func unsealJSON(token string, primarySecret, previousSecret []byte, out any) error {
	if !strings.HasPrefix(token, sealedPrefix) {
		return errors.New("not a v1 sealed payload")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, sealedPrefix))
	if err != nil {
		return err
	}
	if err := decryptWithKey(raw, primarySecret, out); err == nil {
		return nil
	}
	if len(previousSecret) >= 32 {
		if err := decryptWithKey(raw, previousSecret, out); err == nil {
			return nil
		}
	}
	return errors.New("failed to decrypt or authenticate sealed payload")
}

func decryptWithKey(raw, secret []byte, out any) error {
	if len(secret) == 0 {
		return errors.New("empty key")
	}
	block, err := aes.NewCipher(deriveAESKey(secret))
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if len(raw) < gcm.NonceSize() {
		return errors.New("ciphertext too short")
	}
	nonce := raw[:gcm.NonceSize()]
	ciphertext := raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(plaintext, out)
}

// generateRandomID creates a 128-bit (16-byte) random hex identifier.
func generateRandomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// IssueTicket creates a single-use AES-256-GCM sealed handoff ticket bound to a specific project host.
func (h *Handler) IssueTicket(host, sessionHandle, subject, issuer, audience string) (string, error) {
	id, err := generateRandomID()
	if err != nil {
		return "", err
	}
	exp := time.Now().Add(handoffTTL).Unix()
	t := HandoffTicket{
		TicketID:      id,
		Host:          strings.ToLower(strings.TrimSpace(host)),
		SessionHandle: sessionHandle,
		Subject:       subject,
		Issuer:        issuer,
		Audience:      audience,
		ExpiresAt:     exp,
	}

	sealed, err := sealJSON(t, h.Config.HMACSecret)
	if err != nil {
		return "", err
	}
	return sealed, nil
}

// RedeemTicket validates and consumes a single-use handoff ticket for a specific project host.
func (h *Handler) RedeemTicket(rawTicket, callerHost string) (*HandoffTicket, error) {
	if rawTicket == "" {
		return nil, errors.New("missing ticket")
	}
	var t HandoffTicket
	if err := unsealJSON(rawTicket, h.Config.HMACSecret, h.Config.HMACSecretPrevious, &t); err != nil {
		return nil, fmt.Errorf("invalid ticket: %w", err)
	}
	now := time.Now().Unix()
	if t.ExpiresAt < now {
		return nil, errors.New("ticket expired")
	}
	normCaller := strings.ToLower(strings.TrimSpace(strings.Split(callerHost, ":")[0]))
	if t.Host != "" && normCaller != "" && !strings.EqualFold(t.Host, normCaller) {
		return nil, fmt.Errorf("ticket host mismatch: expected %s, got %s", t.Host, normCaller)
	}

	h.ensureTicketStore()
	h.tickets.mu.Lock()
	defer h.tickets.mu.Unlock()
	h.evictExpiredTicketsLocked(now)

	if _, alreadyUsed := h.tickets.consumed[t.TicketID]; alreadyUsed {
		return nil, errors.New("ticket already consumed")
	}
	h.tickets.consumed[t.TicketID] = t.ExpiresAt
	return &t, nil
}

func (h *Handler) ensureTicketStore() {
	h.ticketInitOnce.Do(func() {
		if h.tickets == nil {
			h.tickets = newTicketStore()
		}
	})
}

func (h *Handler) evictExpiredTicketsLocked(now int64) {
	for k, exp := range h.tickets.consumed {
		if exp < now {
			delete(h.tickets.consumed, k)
		}
	}
}

// verifyLegacyHMAC verifies an HMAC-SHA256 signature against primary and previous secrets.
func verifyLegacyHMAC(payloadBytes, sigBytes, primarySecret, previousSecret []byte) bool {
	mac := hmac.New(sha256.New, primarySecret)
	mac.Write(payloadBytes)
	if hmac.Equal(sigBytes, mac.Sum(nil)) {
		return true
	}
	if len(previousSecret) > 0 {
		prevMac := hmac.New(sha256.New, previousSecret)
		prevMac.Write(payloadBytes)
		if hmac.Equal(sigBytes, prevMac.Sum(nil)) {
			return true
		}
	}
	return false
}
