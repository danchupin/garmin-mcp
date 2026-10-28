package personal

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLocalKeySourceReusesTokenAndRenewsOnceForConcurrentRequests(t *testing.T) {
	var clock, exchanges, reads atomic.Int64
	clock.Store(testNow.Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	source, err := NewLocalKeySource(func() (string, error) { reads.Add(1); return syntheticKey, nil }, "global", Options{
		Now: now, HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Scheme != "https" || r.URL.Host != "personal.connectapi.garmin.com" || r.URL.Path != tokenPath || r.Method != "POST" || r.Header.Get("Authorization") != "ApiKey "+syntheticKey {
				t.Error("local key reached an unexpected endpoint")
			}
			n := exchanges.Add(1)
			return response(200, jwt(int(n), now().Add(24*time.Hour)), "text/plain"), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := source(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(23 * time.Hour / time.Second))
	unchanged, err := source(context.Background())
	if err != nil || unchanged.Token != first.Token || reads.Load() != 1 {
		t.Fatal("valid token was needlessly exchanged")
	}
	clock.Add(int64(59 * time.Minute / time.Second))
	var requests sync.WaitGroup
	for range 12 {
		requests.Go(func() {
			value, err := source(context.Background())
			if err != nil || value.Token == first.Token || !value.ExpiresAt.After(now().Add(23*time.Hour)) {
				t.Error("expired local credential was not renewed")
			}
		})
	}
	requests.Wait()
	if exchanges.Load() != 2 || reads.Load() != 2 {
		t.Fatal("concurrent renewal reread the key or repeated the exchange")
	}
}

func TestLocalKeySourceDoesNotRetryRejectedKeysOrReturnExpiredCredentials(t *testing.T) {
	now := testNow
	var calls atomic.Int32
	source, err := NewLocalKeySource(func() (string, error) { return syntheticKey, nil }, "global", Options{
		Now: func() time.Time { return now }, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return response(401, syntheticKey, "text/plain"), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		value, err := source(context.Background())
		if codeOf(err) != "UNAUTHORIZED" || value.Token != "" {
			t.Fatal("rejected key produced a credential")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("rejected key was retried without backoff")
	}
	now = now.Add(time.Minute)
	_, _ = source(context.Background())
	if calls.Load() != 2 {
		t.Fatal("exchange did not recover after backoff")
	}
}

func TestLocalKeySourceRereadsRotatedKeyAndStartsWithoutPersistentToken(t *testing.T) {
	now := testNow
	key := syntheticKey
	var calls int
	options := Options{Now: func() time.Time { return now }, HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "ApiKey "+key {
			t.Fatal("a previous key was retained during renewal")
		}
		calls++
		return response(200, jwt(calls, now.Add(24*time.Hour)), "text/plain"), nil
	})}}
	reader := func() (string, error) { return key, nil }
	first, err := NewLocalKeySource(reader, "global", options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first(context.Background()); err != nil {
		t.Fatal(err)
	}
	key = "synthetic-rotated-personal-key"
	now = now.Add(24 * time.Hour)
	if _, err := first(context.Background()); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewLocalKeySource(reader, "global", options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("renewal or restart reused a stale credential")
	}
}
