package broker

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/danchupin/garmin-personal-mcp/internal/domain"
	"github.com/danchupin/garmin-personal-mcp/internal/personal"
	"github.com/danchupin/garmin-personal-mcp/internal/session"
)

type Options struct {
	ClientFactory   func(token, region string) (domain.PersonalClient, error)
	LocalCredential func(context.Context) (domain.Credential, error)
	Now             func() time.Time
}
type Broker struct {
	mu               sync.Mutex
	factory          func(string, string) (domain.PersonalClient, error)
	localCredential  func(context.Context) (domain.Credential, error)
	now              func() time.Time
	exports          *session.Sealer
	owners, binaries map[string]int
	active, binary   int
	closed           bool
	ctx              context.Context
	cancel           context.CancelFunc
}
type exportTicket struct {
	Owner, Operation string
	Parameters       map[string]any
	JSON             bool
}

func New(options Options) (*Broker, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	defer clear(key)
	exports, err := session.New(key, "local", "export", now)
	if err != nil {
		return nil, err
	}
	factory := options.ClientFactory
	if factory == nil {
		factory = func(token, region string) (domain.PersonalClient, error) {
			return personal.NewAccess(token, region, personal.Options{Now: now})
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Broker{factory: factory, localCredential: options.LocalCredential, now: now, exports: exports, owners: map[string]int{}, binaries: map[string]int{}, ctx: ctx, cancel: cancel}, nil
}
func (b *Broker) Close() { b.mu.Lock(); b.closed = true; b.cancel(); b.mu.Unlock() }
func (b *Broker) principal(ctx context.Context, p domain.Principal) (domain.Principal, error) {
	if b.localCredential == nil {
		return p, nil
	}
	if p.Issuer != "local" || p.Subject != "stdio" {
		return p, domain.NewError("FORBIDDEN", "Local credentials are available only to this stdio process.", 403)
	}
	credential, err := b.localCredential(ctx)
	if err != nil {
		return p, safe(err)
	}
	p.Credential, p.ExpiresAt = &credential, credential.ExpiresAt
	return p, nil
}
func (b *Broker) credential(p domain.Principal) error {
	if p.Credential == nil || p.Credential.Token == "" || !p.ExpiresAt.After(b.now()) || !p.Credential.ExpiresAt.After(b.now()) {
		return domain.NewError("UNAUTHORIZED", "Garmin access has expired. Check the local key or replace the manually supplied temporary token.", 401)
	}
	return nil
}
func (b *Broker) ConnectionStatus(p domain.Principal) (*domain.Connection, error) {
	var err error
	if p, err = b.principal(b.ctx, p); err != nil {
		return nil, err
	}
	if err := b.credential(p); err != nil {
		return nil, err
	}
	return &domain.Connection{ID: p.Subject, Region: p.Credential.Region, Version: 1, CreatedAt: p.IssuedAt, UpdatedAt: p.IssuedAt, ExpiresAt: p.ExpiresAt}, nil
}
func safe(err error) error {
	var known *domain.Error
	if errors.As(err, &known) {
		return known
	}
	if err != nil {
		return domain.NewError("INTERNAL_ERROR", "The request could not be completed.", 500)
	}
	return nil
}
func (b *Broker) acquire(p domain.Principal, binary bool) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	owner := p.ID()
	if b.closed {
		return nil, domain.NewError("UNAVAILABLE", "The server is shutting down.", 503)
	}
	if b.active >= 24 || b.owners[owner] >= 3 || (binary && (b.binary >= 2 || b.binaries[owner] >= 1)) {
		return nil, domain.NewError("BUSY", "Request concurrency limit reached. Retry later.", 429)
	}
	b.active++
	b.owners[owner]++
	if binary {
		b.binary++
		b.binaries[owner]++
	}
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		b.active--
		b.owners[owner]--
		if b.owners[owner] == 0 {
			delete(b.owners, owner)
		}
		if binary {
			b.binary--
			b.binaries[owner]--
			if b.binaries[owner] == 0 {
				delete(b.binaries, owner)
			}
		}
	}, nil
}
func (b *Broker) perform(ctx context.Context, p domain.Principal, operation personal.Operation, args map[string]any) (domain.PersonalResult, error) {
	if err := b.credential(p); err != nil {
		return domain.PersonalResult{}, err
	}
	if !p.HasScope(operation.Scope) {
		return domain.PersonalResult{}, domain.NewError("FORBIDDEN", "This operation is outside the granted permissions.", 403)
	}
	done, err := b.acquire(p, operation.Binary)
	if err != nil {
		return domain.PersonalResult{}, err
	}
	defer done()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(b.ctx, cancel)
	defer stop()
	client, err := b.factory(p.Credential.Token, p.Credential.Region)
	if err != nil {
		return domain.PersonalResult{}, safe(err)
	}
	result, err := client.Execute(ctx, operation.ID, args)
	if err != nil {
		return domain.PersonalResult{}, safe(err)
	}
	if err = b.credential(p); err != nil {
		return domain.PersonalResult{}, err
	}
	return result, nil
}
func (b *Broker) Execute(ctx context.Context, p domain.Principal, id string, args map[string]any, exportJSON bool) (domain.Result, error) {
	operation, err := personal.GetOperation(id)
	if err != nil {
		return domain.Result{}, safe(err)
	}
	if p, err = b.principal(ctx, p); err != nil {
		return domain.Result{}, err
	}
	if err = b.credential(p); err != nil {
		return domain.Result{}, err
	}
	if !p.HasScope(operation.Scope) {
		return domain.Result{}, domain.NewError("FORBIDDEN", "This operation is outside the granted permissions.", 403)
	}
	parameters, err := operation.Normalize(args)
	if err != nil {
		return domain.Result{}, safe(err)
	}
	if operation.Binary || exportJSON {
		if operation.Binary && exportJSON {
			return domain.Result{}, domain.NewError("INVALID_ARGUMENTS", "JSON export requires a JSON operation.", 400)
		}
		expires := b.now().Add(10 * time.Minute)
		if p.ExpiresAt.Before(expires) {
			expires = p.ExpiresAt
		}
		ticket, err := b.exports.Seal(exportTicket{Owner: p.ID(), Operation: id, Parameters: parameters, JSON: exportJSON}, expires)
		if err != nil {
			return domain.Result{}, safe(err)
		}
		hash := sha256.Sum256([]byte(ticket))
		name, mime := id+".json", "application/json"
		if operation.Binary {
			name, mime = "activity-original", "application/octet-stream"
		}
		return domain.Result{Kind: "file", Artifact: &domain.Artifact{ID: base64.RawURLEncoding.EncodeToString(hash[:]), URI: "garmin://files/" + ticket, Name: name, MIMEType: mime, ExpiresAt: expires}}, nil
	}
	response, err := b.perform(ctx, p, operation, parameters)
	if err != nil {
		return domain.Result{}, err
	}
	return domain.Result{Kind: "json", Data: response.Data}, nil
}
func (b *Broker) ReadFile(ctx context.Context, p domain.Principal, uri string) (domain.File, error) {
	missing := domain.NewError("NOT_FOUND", "This export is unavailable or expired.", 404)
	var err error
	if p, err = b.principal(ctx, p); err != nil {
		return domain.File{}, err
	}
	if err := b.credential(p); err != nil {
		return domain.File{}, err
	}
	if !strings.HasPrefix(uri, "garmin://files/") {
		return domain.File{}, missing
	}
	var ticket exportTicket
	expires, err := b.exports.Open(strings.TrimPrefix(uri, "garmin://files/"), &ticket)
	if err != nil || ticket.Owner != p.ID() {
		return domain.File{}, missing
	}
	operation, err := personal.GetOperation(ticket.Operation)
	if err != nil || (!operation.Binary && !ticket.JSON) || (operation.Binary && ticket.JSON) {
		return domain.File{}, missing
	}
	parameters, err := operation.Normalize(ticket.Parameters)
	if err != nil {
		return domain.File{}, missing
	}
	response, err := b.perform(ctx, p, operation, parameters)
	if err != nil {
		return domain.File{}, err
	}
	bytes, mime, name := response.Bytes, response.MIMEType, response.Name
	if ticket.JSON {
		bytes, err = json.Marshal(response.Data)
		mime, name = "application/json", ticket.Operation+".json"
	}
	if err != nil {
		return domain.File{}, safe(err)
	}
	if len(bytes) > 25*1024*1024 {
		return domain.File{}, domain.NewError("RESPONSE_TOO_LARGE", "The export exceeds the operation size limit.", 502)
	}
	return domain.File{Artifact: domain.Artifact{URI: uri, MIMEType: mime, Name: name, Size: len(bytes), ExpiresAt: expires}, Bytes: bytes}, nil
}
