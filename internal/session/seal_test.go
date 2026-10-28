package session

import (
	"strings"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

func TestEncryptionIsRandomizedBoundToPurposeIssuerAndKey(t *testing.T) {
	now := time.Now()
	key := []byte("01234567890123456789012345678901")
	s, err := New(key, "https://mcp.test", "access", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Seal(map[string]string{"credential": "temporary-credential-canary"}, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Seal(map[string]string{"credential": "temporary-credential-canary"}, now.Add(time.Hour))
	if err != nil || first == second || strings.Contains(first, "canary") {
		t.Fatal("encryption leaked data or reused a nonce")
	}
	var value map[string]string
	if _, err = s.Open(first, &value); err != nil || value["credential"] != "temporary-credential-canary" {
		t.Fatal("valid token rejected")
	}
	for _, pair := range [][2]string{{"https://other.test", "access"}, {"https://mcp.test", "code"}} {
		other, _ := New(key, pair[0], pair[1], func() time.Time { return now })
		if _, err = other.Open(first, &value); err == nil {
			t.Fatal("cross-purpose or cross-resource token accepted")
		}
	}
	other, _ := New([]byte("11234567890123456789012345678901"), "https://mcp.test", "access", func() time.Time { return now })
	if _, err = other.Open(first, &value); err == nil {
		t.Fatal("wrong key accepted")
	}
	now = now.Add(time.Hour + time.Second)
	if _, err = s.Open(first, &value); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestSealerRejectsOversizeCompressedAndUnboundedTokens(t *testing.T) {
	now := time.Now()
	s, _ := New([]byte("01234567890123456789012345678901"), "https://mcp.test", "access", func() time.Time { return now })
	for _, expiry := range []time.Time{now.Add(-time.Second), now.Add(2 * time.Hour)} {
		if _, err := s.Seal(map[string]string{}, expiry); err == nil {
			t.Fatal("invalid token lifetime accepted")
		}
	}
	if _, err := s.Seal(strings.Repeat("x", MaxTokenBytes), now.Add(time.Minute)); err == nil {
		t.Fatal("oversized token issued")
	}
	var value any
	if _, err := s.Open(strings.Repeat("x", MaxTokenBytes+1), &value); err == nil {
		t.Fatal("oversized input accepted")
	}
	options := new(jose.EncrypterOptions).WithType("garmin-mcp/access/v1")
	options.Compression = jose.DEFLATE
	encryptor, err := jose.NewEncrypter(jose.A256GCM, jose.Recipient{Algorithm: jose.DIRECT, Key: s.key}, options)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := encryptor.Encrypt([]byte(strings.Repeat("private", 1000)))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sealed.CompactSerialize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Open(raw, &value); err == nil {
		t.Fatal("compressed token accepted")
	}
}
