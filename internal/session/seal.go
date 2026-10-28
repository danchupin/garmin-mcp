package session

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const MaxTokenBytes = 24576

var ErrInvalid = errors.New("invalid or expired session")

type Sealer struct {
	key             []byte
	origin, purpose string
	now             func() time.Time
}
type envelope struct {
	Issuer   string          `json:"iss"`
	Audience string          `json:"aud"`
	Purpose  string          `json:"purpose"`
	Expires  int64           `json:"exp"`
	Data     json.RawMessage `json:"data"`
}

func New(key []byte, origin, purpose string, now func() time.Time) (*Sealer, error) {
	if len(key) != 32 || origin == "" || purpose == "" {
		return nil, ErrInvalid
	}
	if now == nil {
		now = time.Now
	}
	derive := hmac.New(sha256.New, key)
	_, _ = derive.Write([]byte("garmin-mcp:jwe:v1:" + origin + ":" + purpose))
	return &Sealer{key: derive.Sum(nil), origin: origin, purpose: purpose, now: now}, nil
}
func (s *Sealer) Seal(value any, expires time.Time) (string, error) {
	if !expires.After(s.now()) || expires.After(s.now().Add(time.Hour+time.Minute)) {
		return "", ErrInvalid
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", ErrInvalid
	}
	plain, err := json.Marshal(envelope{Issuer: s.origin, Audience: s.origin + "/mcp", Purpose: s.purpose, Expires: expires.Unix(), Data: data})
	if err != nil {
		return "", ErrInvalid
	}
	defer clear(plain)
	encryptor, err := jose.NewEncrypter(jose.A256GCM, jose.Recipient{Algorithm: jose.DIRECT, Key: s.key}, new(jose.EncrypterOptions).WithType(jose.ContentType("garmin-mcp/"+s.purpose+"/v1")))
	if err != nil {
		return "", ErrInvalid
	}
	sealed, err := encryptor.Encrypt(plain)
	if err != nil {
		return "", ErrInvalid
	}
	result, err := sealed.CompactSerialize()
	if err != nil || len(result) > MaxTokenBytes {
		return "", ErrInvalid
	}
	return result, nil
}
func (s *Sealer) Open(raw string, value any) (time.Time, error) {
	if len(raw) > MaxTokenBytes || strings.Count(raw, ".") != 4 {
		return time.Time{}, ErrInvalid
	}
	sealed, err := jose.ParseEncryptedCompact(raw, []jose.KeyAlgorithm{jose.DIRECT}, []jose.ContentEncryption{jose.A256GCM})
	if err != nil || sealed.Header.ExtraHeaders["typ"] != "garmin-mcp/"+s.purpose+"/v1" || sealed.Header.ExtraHeaders["zip"] != nil {
		return time.Time{}, ErrInvalid
	}
	plain, err := sealed.Decrypt(s.key)
	if err != nil {
		return time.Time{}, ErrInvalid
	}
	defer clear(plain)
	var payload envelope
	if json.Unmarshal(plain, &payload) != nil || payload.Issuer != s.origin || payload.Audience != s.origin+"/mcp" || payload.Purpose != s.purpose {
		return time.Time{}, ErrInvalid
	}
	expires := time.Unix(payload.Expires, 0)
	if !expires.After(s.now()) || expires.After(s.now().Add(time.Hour+time.Minute)) || json.Unmarshal(payload.Data, value) != nil {
		return time.Time{}, ErrInvalid
	}
	return expires, nil
}
