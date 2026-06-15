package security

import (
	"testing"
	"time"
)

func TestSignatureRotation(t *testing.T) {
	ring := SignatureRing{Current: "cur-key", Previous: "old-key"}
	message := []byte(`{"status":"ok"}`)
	ts := time.Now()
	sig, err := ring.ComputeSignature(message, ts)
	if err != nil {
		t.Fatalf("compute signature failed: %v", err)
	}
	if err := ring.VerifySignature(message, sig, 5*time.Minute, ts.Add(time.Second)); err != nil {
		t.Fatalf("verify current key failed: %v", err)
	}

	// verify fallback to previous key
	legacy := SignatureRing{Current: "old-key"}
	legacySig, err := legacy.ComputeSignature(message, ts)
	if err != nil {
		t.Fatalf("compute legacy signature failed: %v", err)
	}
	if err := ring.VerifySignature(message, legacySig, 5*time.Minute, ts.Add(time.Second)); err != nil {
		t.Fatalf("expected verify on previous key: %v", err)
	}

	// ensure legacy signature cannot verify if previous key is dropped
	ring.Previous = ""
	if err := ring.VerifySignature(message, legacySig, 5*time.Minute, ts.Add(time.Second)); err == nil {
		t.Fatal("expected fail when only previous key removed")
	}
}

func TestReplayGuard(t *testing.T) {
	guard := NewReplayGuard()
	now := time.Now()
	if ok := guard.Accept("nonce-1", now, 10*time.Second); !ok {
		t.Fatal("expected first nonce accepted")
	}
	if ok := guard.Accept("nonce-1", now.Add(time.Second), 10*time.Second); ok {
		t.Fatal("expected second nonce reject")
	}
	if ok := guard.Accept("nonce-2", now.Add(11*time.Second), 10*time.Second); !ok {
		t.Fatal("expected different nonce accepted")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	env, err := NewCryptoEnvelope("000102030405060708090a0b0c0d0e0f")
	if err != nil {
		t.Fatalf("unexpected key parse: %v", err)
	}
	plaintext := []byte("secret payload")
	cipher, err := env.Encrypt(plaintext, []byte("aad"))
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if cipher == "" {
		t.Fatal("cipher empty")
	}
	decrypted, err := env.Decrypt(cipher, []byte("aad"))
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if string(decrypted) != string(plaintext) {
		t.Fatalf("round trip mismatch: %s", string(decrypted))
	}
}
