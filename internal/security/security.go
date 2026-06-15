package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	signaturePrefix = "t="
	sep             = ",v1="
)

// SignatureRing supports key rotation via current+previous secret.
type SignatureRing struct {
	Current  string
	Previous string
}

func (r SignatureRing) ComputeSignature(message []byte, ts time.Time) (string, error) {
	if r.Current == "" {
		return "", errors.New("current secret is required")
	}
	digest := hmacSign([]byte(r.Current), appendTimestamp(message, ts))
	return formatSignature(ts.Unix(), digest), nil
}

func (r SignatureRing) VerifySignature(message []byte, signature string, window time.Duration, now time.Time) error {
	if r.Current == "" {
		return errors.New("current secret is required")
	}
	parts := strings.SplitN(signature, sep, 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[0], signaturePrefix) {
		return errors.New("invalid signature format")
	}
	timestamp, err := strconv.ParseInt(strings.TrimPrefix(parts[0], signaturePrefix), 10, 64)
	if err != nil {
		return errors.New("invalid signature timestamp")
	}
	expectedTs := time.Unix(timestamp, 0)
	if now.Sub(expectedTs) > window {
		return errors.New("signature expired")
	}
	if now.Sub(expectedTs) < -window {
		return errors.New("signature from future")
	}

	current := formatSignature(timestamp, hmacSign([]byte(r.Current), appendTimestamp(message, expectedTs)))
	if hmac.Equal([]byte(current), []byte(signature)) {
		return nil
	}
	if r.Previous != "" {
		previous := formatSignature(timestamp, hmacSign([]byte(r.Previous), appendTimestamp(message, expectedTs)))
		if hmac.Equal([]byte(previous), []byte(signature)) {
			return nil
		}
	}
	return errors.New("signature mismatch")
}

func appendTimestamp(message []byte, ts time.Time) []byte {
	return []byte(fmt.Sprintf("%d.%s", ts.Unix(), string(message)))
}

func hmacSign(secret []byte, message []byte) string {
	h := hmac.New(sha256.New, secret)
	h.Write(message)
	return hex.EncodeToString(h.Sum(nil))
}

func formatSignature(ts int64, digest string) string {
	return fmt.Sprintf("%s%d%s%s", signaturePrefix, ts, sep, digest)
}

// ReplayGuard blocks duplicate nonce within TTL window.
type ReplayGuard struct {
	mu      sync.Mutex
	entries map[string]time.Time
}

func NewReplayGuard() *ReplayGuard {
	return &ReplayGuard{entries: map[string]time.Time{}}
}

func (g *ReplayGuard) Accept(nonce string, now time.Time, ttl time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if expiry, ok := g.entries[nonce]; ok {
		if expiry.After(now) {
			return false
		}
		delete(g.entries, nonce)
	}
	for key, expiry := range g.entries {
		if !expiry.After(now) {
			delete(g.entries, key)
		}
	}
	g.entries[nonce] = now.Add(ttl)
	return true
}

// CryptoEnvelope encrypt/decrypt request bodies.
type CryptoEnvelope struct {
	Key []byte
}

func NewCryptoEnvelope(key string) (*CryptoEnvelope, error) {
	decoded, err := hex.DecodeString(key)
	if err != nil {
		return nil, fmt.Errorf("invalid key hex: %w", err)
	}
	if len(decoded) != 16 && len(decoded) != 24 && len(decoded) != 32 {
		return nil, errors.New("invalid aes key length")
	}
	return &CryptoEnvelope{Key: decoded}, nil
}

func (e CryptoEnvelope) Encrypt(plainText []byte, aad []byte) (string, error) {
	block, err := aes.NewCipher(e.Key)
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
	cipherText := gcm.Seal(nil, nonce, plainText, aad)
	result := make([]byte, len(nonce)+len(cipherText))
	copy(result, nonce)
	copy(result[len(nonce):], cipherText)
	return base64.StdEncoding.EncodeToString(result), nil
}

func (e CryptoEnvelope) Decrypt(cipherText string, aad []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(e.Key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(raw) < nonceSize {
		return nil, errors.New("cipher text too short")
	}
	plain, err := gcm.Open(nil, raw[:nonceSize], raw[nonceSize:], aad)
	if err != nil {
		return nil, errors.New("invalid ciphertext")
	}
	return plain, nil
}
