package domain

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"
)

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func NewError(code, message string, status int) *Error {
	return &Error{Code: code, Message: message, Status: status}
}

type Principal struct {
	Issuer     string
	Subject    string
	Scopes     []string
	Credential *Credential `json:"-"`
	ExpiresAt  time.Time   `json:"-"`
	IssuedAt   time.Time   `json:"-"`
}

func (p Principal) HasScope(scope string) bool {
	for _, granted := range p.Scopes {
		if granted == scope {
			return true
		}
	}
	return false
}

func (p Principal) ID() string {
	value, _ := json.Marshal([2]string{p.Issuer, p.Subject})
	hash := sha256.Sum256(value)
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

type Connection struct {
	ID        string    `json:"id"`
	Region    string    `json:"region"`
	Version   uint64    `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
}

// Credential is request-local; it must never be included in MCP output.
type Credential struct {
	Token     string    `json:"-"`
	Region    string    `json:"-"`
	ExpiresAt time.Time `json:"-"`
}

type PersonalResult struct {
	Kind     string
	Data     any
	Bytes    []byte
	MIMEType string
	Name     string
}

type PersonalClient interface {
	Execute(context.Context, string, map[string]any) (PersonalResult, error)
}

type Artifact struct {
	ID        string    `json:"id"`
	URI       string    `json:"uri"`
	Name      string    `json:"name"`
	MIMEType  string    `json:"mimeType"`
	Size      int       `json:"size,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Result struct {
	Kind     string    `json:"kind"`
	Data     any       `json:"data,omitempty"`
	Artifact *Artifact `json:"artifact,omitempty"`
}

type File struct {
	Artifact Artifact
	Bytes    []byte
}

type Service interface {
	ConnectionStatus(Principal) (*Connection, error)
	Execute(context.Context, Principal, string, map[string]any, bool) (Result, error)
	ReadFile(context.Context, Principal, string) (File, error)
}
