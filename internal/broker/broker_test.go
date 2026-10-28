package broker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danchupin/garmin-personal-mcp/internal/domain"
)

type clientFunc func(context.Context, string, map[string]any) (domain.PersonalResult, error)

func (f clientFunc) Execute(ctx context.Context, id string, args map[string]any) (domain.PersonalResult, error) {
	return f(ctx, id, args)
}
func principal(name string, now time.Time) domain.Principal {
	return domain.Principal{Issuer: "test", Subject: name, Scopes: []string{"profile:read", "activities:export", "activities:read"}, IssuedAt: now, ExpiresAt: now.Add(time.Hour), Credential: &domain.Credential{Token: "synthetic-token-" + name, Region: "global", ExpiresAt: now.Add(24 * time.Hour)}}
}
func errorCode(err error) string {
	var known *domain.Error
	if errors.As(err, &known) {
		return known.Code
	}
	return ""
}
func build(t *testing.T, options Options) *Broker {
	t.Helper()
	b, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestLocalCredentialSourceIsBoundToStdioAndPreservesScopeChecks(t *testing.T) {
	now := time.Now()
	var calls atomic.Int32
	b := build(t, Options{
		LocalCredential: func(context.Context) (domain.Credential, error) {
			calls.Add(1)
			return domain.Credential{Token: "synthetic-local-token", Region: "global", ExpiresAt: now.Add(24 * time.Hour)}, nil
		},
		ClientFactory: func(token, region string) (domain.PersonalClient, error) {
			if token != "synthetic-local-token" || region != "global" {
				t.Fatal("local principal did not receive the current credential")
			}
			return clientFunc(func(context.Context, string, map[string]any) (domain.PersonalResult, error) {
				return domain.PersonalResult{Kind: "json", Data: map[string]any{"ok": true}}, nil
			}), nil
		},
	})
	if _, err := b.ConnectionStatus(principal("remote", now)); errorCode(err) != "FORBIDDEN" || calls.Load() != 0 {
		t.Fatal("remote identity reached the local key source")
	}
	p := domain.Principal{Issuer: "local", Subject: "stdio", Scopes: []string{"profile:read"}, IssuedAt: now}
	connection, err := b.ConnectionStatus(p)
	if err != nil || !connection.ExpiresAt.After(now.Add(23*time.Hour)) {
		t.Fatal("local session retained the former one-hour limit")
	}
	if _, err := b.Execute(context.Background(), p, "profile", nil, false); err != nil {
		t.Fatal(err)
	}
	p.Scopes = nil
	if _, err := b.Execute(context.Background(), p, "profile", nil, false); errorCode(err) != "FORBIDDEN" {
		t.Fatal("local renewal bypassed scopes")
	}
}
func TestEveryReadFetchesFreshDataAndCredentialIsRequestLocal(t *testing.T) {
	now := time.Now()
	var calls atomic.Int32
	b := build(t, Options{ClientFactory: func(token, region string) (domain.PersonalClient, error) {
		return clientFunc(func(context.Context, string, map[string]any) (domain.PersonalResult, error) {
			return domain.PersonalResult{Kind: "json", Data: map[string]any{"credential": token, "number": calls.Add(1)}}, nil
		}), nil
	}})
	alice, bob := principal("alice", now), principal("bob", now)
	for _, p := range []domain.Principal{alice, bob, alice} {
		result, err := b.Execute(context.Background(), p, "profile", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if result.Data.(map[string]any)["credential"] != p.Credential.Token {
			t.Fatal("request inherited another credential")
		}
	}
	if calls.Load() != 3 || len(b.owners) != 0 || len(b.binaries) != 0 {
		t.Fatal("result cached or idle per-user state retained")
	}
}
func TestExportIsAnAuthenticatedRequestNotAStoredSnapshot(t *testing.T) {
	now := time.Now()
	var calls atomic.Int32
	b := build(t, Options{ClientFactory: func(string, string) (domain.PersonalClient, error) {
		return clientFunc(func(context.Context, string, map[string]any) (domain.PersonalResult, error) {
			return domain.PersonalResult{Kind: "json", Data: map[string]any{"number": calls.Add(1)}}, nil
		}), nil
	}})
	p := principal("alice", now)
	result, err := b.Execute(context.Background(), p, "profile", nil, true)
	if err != nil || result.Artifact == nil || result.Artifact.Size != 0 || calls.Load() != 0 {
		t.Fatal("export eagerly fetched or persisted a snapshot")
	}
	for n := int32(1); n <= 2; n++ {
		file, err := b.ReadFile(context.Background(), p, result.Artifact.URI)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]int32
		if json.Unmarshal(file.Bytes, &value) != nil || value["number"] != n {
			t.Fatal("export did not fetch fresh data")
		}
	}
	if _, err = b.ReadFile(context.Background(), principal("bob", now), result.Artifact.URI); errorCode(err) != "NOT_FOUND" {
		t.Fatal("foreign owner read export")
	}
	narrow := p
	narrow.Scopes = []string{"activities:read"}
	if _, err = b.ReadFile(context.Background(), narrow, result.Artifact.URI); errorCode(err) != "FORBIDDEN" {
		t.Fatal("export ignored scopes")
	}
	raw := result.Artifact.URI
	at := len(raw) - 20
	replacement := "A"
	if raw[at] == 'A' {
		replacement = "B"
	}
	raw = raw[:at] + replacement + raw[at+1:]
	if _, err = b.ReadFile(context.Background(), p, raw); errorCode(err) != "NOT_FOUND" {
		t.Fatal("tampered export accepted")
	}
	if calls.Load() != 2 {
		t.Fatal("invalid export request reached upstream")
	}
}
func TestExportCannotBeReusedAfterProcessRestart(t *testing.T) {
	now := time.Now()
	options := Options{ClientFactory: func(string, string) (domain.PersonalClient, error) {
		return clientFunc(func(context.Context, string, map[string]any) (domain.PersonalResult, error) {
			return domain.PersonalResult{Kind: "file", Bytes: []byte("FIT-fixture"), MIMEType: "application/octet-stream", Name: "original.fit"}, nil
		}), nil
	}}
	first, second := build(t, options), build(t, options)
	p := principal("alice", now)
	exported, err := first.Execute(context.Background(), p, "activity_file", map[string]any{"activityId": "123"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.ReadFile(context.Background(), p, exported.Artifact.URI); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ReadFile(context.Background(), p, exported.Artifact.URI); errorCode(err) != "NOT_FOUND" {
		t.Fatal("export remained valid in a different process")
	}
}
func TestExpiryIsCheckedBeforeAndAfterUpstreamAndOnExport(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Now().Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	p := principal("alice", now())
	b := build(t, Options{Now: now, ClientFactory: func(string, string) (domain.PersonalClient, error) {
		return clientFunc(func(context.Context, string, map[string]any) (domain.PersonalResult, error) {
			clock.Add(3601)
			return domain.PersonalResult{Kind: "json", Data: map[string]any{"private": true}}, nil
		}), nil
	}})
	exported, err := b.Execute(context.Background(), p, "profile", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Execute(context.Background(), p, "profile", nil, false); errorCode(err) != "UNAUTHORIZED" {
		t.Fatal("expired in-flight response returned")
	}
	if _, err = b.ReadFile(context.Background(), p, exported.Artifact.URI); errorCode(err) != "UNAUTHORIZED" {
		t.Fatal("expired session read export")
	}
	if _, err = b.ConnectionStatus(p); errorCode(err) != "UNAUTHORIZED" {
		t.Fatal("expired connection reported active")
	}
}
func TestPermissionAndParameterChecksRunBeforeAnyUpstreamWork(t *testing.T) {
	var calls atomic.Int32
	b := build(t, Options{ClientFactory: func(string, string) (domain.PersonalClient, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	}})
	p := principal("alice", time.Now())
	p.Scopes = []string{"activities:read"}
	for _, test := range []struct {
		operation string
		args      map[string]any
		code      string
	}{{"profile", nil, "FORBIDDEN"}, {"activity", map[string]any{"activityId": "../secret"}, "INVALID_ARGUMENTS"}} {
		if _, err := b.Execute(context.Background(), p, test.operation, test.args, false); errorCode(err) != test.code {
			t.Fatalf("unexpected error %v", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request reached upstream")
	}
}
func TestConcurrentUserAndBinaryLimitsReleaseAllIdleState(t *testing.T) {
	now := time.Now()
	p := principal("alice", now)
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	b := build(t, Options{ClientFactory: func(string, string) (domain.PersonalClient, error) {
		return clientFunc(func(ctx context.Context, _ string, _ map[string]any) (domain.PersonalResult, error) {
			entered <- struct{}{}
			select {
			case <-release:
				return domain.PersonalResult{Kind: "json", Data: map[string]any{}}, nil
			case <-ctx.Done():
				return domain.PersonalResult{}, ctx.Err()
			}
		}), nil
	}})
	finished := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { _, err := b.Execute(context.Background(), p, "profile", nil, false); finished <- err }()
	}
	for i := 0; i < 3; i++ {
		<-entered
	}
	if _, err := b.Execute(context.Background(), p, "profile", nil, false); errorCode(err) != "BUSY" {
		close(release)
		t.Fatal("per-user concurrency limit bypassed")
	}
	close(release)
	for i := 0; i < 3; i++ {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.owners) != 0 || len(b.binaries) != 0 || b.active != 0 || b.binary != 0 {
		t.Fatal("idle per-user quota records retained")
	}
}
func TestUnexpectedErrorsAreRedacted(t *testing.T) {
	b := build(t, Options{ClientFactory: func(string, string) (domain.PersonalClient, error) { return nil, errors.New("temporary-secret-canary") }})
	_, err := b.Execute(context.Background(), principal("alice", time.Now()), "profile", nil, false)
	if errorCode(err) != "INTERNAL_ERROR" || strings.Contains(err.Error(), "canary") {
		t.Fatal("untrusted error leaked")
	}
}

func TestBinaryDownloadsHavePerSessionAndGlobalLimits(t *testing.T) {
	now := time.Now()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	b := build(t, Options{ClientFactory: func(string, string) (domain.PersonalClient, error) {
		return clientFunc(func(ctx context.Context, _ string, _ map[string]any) (domain.PersonalResult, error) {
			entered <- struct{}{}
			select {
			case <-release:
				return domain.PersonalResult{Kind: "binary", Bytes: []byte("FIT")}, nil
			case <-ctx.Done():
				return domain.PersonalResult{}, ctx.Err()
			}
		}), nil
	}})
	people := []domain.Principal{principal("alice", now), principal("bob", now), principal("third", now)}
	links := make([]string, len(people))
	for i, person := range people {
		result, err := b.Execute(context.Background(), person, "activity_file", map[string]any{"activityId": "123"}, false)
		if err != nil {
			t.Fatal(err)
		}
		links[i] = result.Artifact.URI
	}
	completed := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(index int) {
			_, err := b.ReadFile(context.Background(), people[index], links[index])
			completed <- err
		}(i)
	}
	for i := 0; i < 2; i++ {
		<-entered
	}
	for _, index := range []int{0, 2} {
		if _, err := b.ReadFile(context.Background(), people[index], links[index]); errorCode(err) != "BUSY" {
			close(release)
			t.Fatal("binary concurrency limit bypassed")
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-completed; err != nil {
			t.Fatal(err)
		}
	}
}
