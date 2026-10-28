package personal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danchupin/garmin-personal-mcp/internal/domain"
)

const syntheticKey = "synthetic-personal-key-never-real"

var testNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func jwt(n int, expiry time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"ES384"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"canary":%d}`, expiry.Unix(), n)))
	return header + "." + payload + ".synthetic-signature"
}
func response(status int, data, contentType string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(data)), ContentLength: int64(len(data))}
}
func codeOf(err error) string {
	var known *domain.Error
	if errors.As(err, &known) {
		return known.Code
	}
	return ""
}
func newClient(t *testing.T, transport roundTripFunc, now func() time.Time) *Client {
	t.Helper()
	if now == nil {
		now = func() time.Time { return testNow }
	}
	client, err := NewAccess(jwt(1, now().Add(24*time.Hour)), "global", Options{Now: now, HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestTemporaryTokenFixedRegionsAndHeaders(t *testing.T) {
	token := jwt(2, testNow.Add(24*time.Hour))
	for _, region := range []string{"global", "china"} {
		host := "personal.connectapi.garmin.com"
		if region == "china" {
			host = "personal.connectapi.garmin.cn"
		}
		client, err := NewAccess(token, region, Options{Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != host || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path == tokenPath {
				t.Fatal("unexpected upstream request")
			}
			return response(200, `{"ok":true}`, "application/json"), nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = client.Execute(context.Background(), "profile", nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []struct{ raw, region string }{{syntheticKey, "global"}, {jwt(1, testNow.Add(-time.Second)), "global"}, {jwt(1, testNow.Add(25*time.Hour)), "global"}, {token, "https://attacker.test"}, {token, "__proto__"}} {
		if _, err := NewAccess(value.raw, value.region, Options{Now: func() time.Time { return testNow }}); err == nil {
			t.Fatal("invalid temporary credential accepted")
		}
	}
}
func TestInvalidArgumentsNeverReachGarmin(t *testing.T) {
	var calls atomic.Int32
	client := newClient(t, func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, io.EOF }, nil)
	if _, err := client.Execute(context.Background(), "activity", map[string]any{"activityId": "../secret"}); codeOf(err) != "INVALID_ARGUMENTS" {
		t.Fatal("invalid path accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid arguments reached upstream")
	}
}
func TestTemporaryTokenNeverRenewsAndExpiryIsEnforced(t *testing.T) {
	var calls atomic.Int32
	now := testNow
	client := newClient(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path == tokenPath {
			t.Fatal("temporary client exchanged a key")
		}
		return response(401, "credential-canary", "text/plain"), nil
	}, func() time.Time { return now })
	if _, err := client.Execute(context.Background(), "profile", nil); codeOf(err) != "UNAUTHORIZED" || strings.Contains(err.Error(), "canary") {
		t.Fatal("revoked token did not fail safely")
	}
	if _, err := client.Execute(context.Background(), "profile", nil); codeOf(err) != "UNAUTHORIZED" {
		t.Fatal("revoked token revived")
	}
	if calls.Load() != 1 {
		t.Fatal("revoked credential retried")
	}
	client = newClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("expired credential reached network")
		return nil, io.EOF
	}, func() time.Time { return now })
	now = now.Add(24*time.Hour + time.Second)
	if _, err := client.Execute(context.Background(), "profile", nil); codeOf(err) != "UNAUTHORIZED" {
		t.Fatal("expired token accepted")
	}
}
func TestLocalExchangeUsesOnlyOfficialEndpointAndDoesNotSerializeSecrets(t *testing.T) {
	token := jwt(3, testNow.Add(24*time.Hour))
	var calls atomic.Int32
	credential, err := Exchange(context.Background(), syntheticKey, "china", Options{Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Host != "personal.connectapi.garmin.cn" || r.URL.Path != tokenPath || r.Header.Get("Authorization") != "ApiKey "+syntheticKey {
			t.Fatal("unsafe local exchange")
		}
		return response(200, token, "text/plain"), nil
	})}})
	if err != nil || credential.Token != token || credential.Region != "china" || calls.Load() != 1 {
		t.Fatal("local exchange failed")
	}
	encoded, _ := json.Marshal(credential)
	if strings.Contains(string(encoded), token) || strings.Contains(string(encoded), syntheticKey) {
		t.Fatal("secret serializable")
	}
}
func TestLocalExchangeRejectsBadKeysErrorsRedirectsAndMalformedTokens(t *testing.T) {
	for _, key := range []string{"", "line\nbreak", "with space"} {
		if _, err := Exchange(context.Background(), key, "global", Options{}); err == nil {
			t.Fatal("invalid local key accepted")
		}
	}
	for _, status := range []int{301, 302, 401, 403, 404, 429, 500} {
		var calls atomic.Int32
		_, err := Exchange(context.Background(), syntheticKey, "global", Options{Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls.Add(1)
			out := response(status, syntheticKey, "text/plain")
			out.Header.Set("Location", "https://attacker.test")
			return out, nil
		})}})
		if err == nil || strings.Contains(err.Error(), syntheticKey) || calls.Load() != 1 {
			t.Fatal("unsafe exchange error or redirect")
		}
	}
	for _, body := range []string{"not-a-token", jwt(1, testNow.Add(-time.Second)), jwt(1, testNow.Add(25*time.Hour)), strings.Repeat("x", 8193)} {
		_, err := Exchange(context.Background(), syntheticKey, "global", Options{Now: func() time.Time { return testNow }, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return response(200, body, "text/plain"), nil })}})
		if err == nil {
			t.Fatal("malformed exchange accepted")
		}
	}
}
func TestRateLimitsDoNotProduceAutomaticRetries(t *testing.T) {
	var calls atomic.Int32
	client := newClient(t, func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		out := response(429, "temporary-token-canary", "text/plain")
		out.Header.Set("Retry-After", "60")
		return out, nil
	}, nil)
	for i := 0; i < 2; i++ {
		if _, err := client.Execute(context.Background(), "profile", nil); codeOf(err) != "GARMIN_RATE_LIMITED" {
			t.Fatal("429 ignored")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("rate limited request retried")
	}
}
func TestCredentialRedactionAndExactNumbers(t *testing.T) {
	token := jwt(1, testNow.Add(24*time.Hour))
	client := newClient(t, func(*http.Request) (*http.Response, error) {
		body, _ := json.Marshal(map[string]any{"access_token": token, "note": "prefix " + token, "health": 42})
		return response(200, string(body), "application/json"), nil
	}, nil)
	result, err := client.Execute(context.Background(), "profile", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(result.Data)
	if strings.Contains(string(body), token) || !strings.Contains(string(body), "health") {
		t.Fatal("temporary token leaked or health data lost")
	}
	client = newClient(t, func(*http.Request) (*http.Response, error) {
		return response(200, `{"large":9223372036854775807,"exact":42}`, "application/json"), nil
	}, nil)
	result, err = client.Execute(context.Background(), "profile", nil)
	if err != nil || result.Data.(map[string]any)["large"] != "9223372036854775807" {
		t.Fatal("integer precision lost")
	}
	client = newClient(t, func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("transport leaked %s", token) }, nil)
	if _, err = client.Execute(context.Background(), "profile", nil); codeOf(err) != "GARMIN_UPSTREAM" || strings.Contains(err.Error(), token) {
		t.Fatal("transport error leaked")
	}
}
func TestMalformedOversizedAndEmptyResponses(t *testing.T) {
	for _, body := range []string{`{"broken":`, `{} {}`, strings.Repeat("x", jsonBytes+1)} {
		client := newClient(t, func(*http.Request) (*http.Response, error) { return response(200, body, "application/json"), nil }, nil)
		if _, err := client.Execute(context.Background(), "profile", nil); err == nil {
			t.Fatal("invalid response accepted")
		}
	}
	for _, status := range []int{200, 204} {
		client := newClient(t, func(*http.Request) (*http.Response, error) { return response(status, "", "application/json"), nil }, nil)
		result, err := client.Execute(context.Background(), "profile", nil)
		if err != nil || result.Data != nil {
			t.Fatal("no data was invented")
		}
	}
}
func TestBinaryResponseSizeAndFilenameAreSafe(t *testing.T) {
	client := newClient(t, func(*http.Request) (*http.Response, error) {
		out := response(200, "synthetic-fit-bytes", "application/octet-stream")
		out.Header.Set("Content-Disposition", "attachment; filename="+syntheticKey)
		return out, nil
	}, nil)
	result, err := client.Execute(context.Background(), "activity_file", map[string]any{"activityId": "9223372036854775807"})
	if err != nil || string(result.Bytes) != "synthetic-fit-bytes" || strings.Contains(result.Name, syntheticKey) {
		t.Fatal("binary metadata unsafe")
	}
	client = newClient(t, func(*http.Request) (*http.Response, error) {
		out := response(200, "", "application/octet-stream")
		out.ContentLength = fileBytes + 1
		return out, nil
	}, nil)
	if _, err = client.Execute(context.Background(), "activity_file", map[string]any{"activityId": "1"}); codeOf(err) != "RESPONSE_TOO_LARGE" {
		t.Fatal("oversized binary accepted")
	}
}
func TestActivityDownloadAcceptsGarminZIPResponse(t *testing.T) {
	client := newClient(t, func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Accept") != "*/*" {
			return response(406, "synthetic-response", "application/json"), nil
		}
		return response(200, "synthetic-zip-bytes", "application/zip"), nil
	}, nil)
	result, err := client.Execute(context.Background(), "activity_file", map[string]any{"activityId": "123"})
	if err != nil || result.MIMEType != "application/zip" || result.Name != "activity-123.zip" {
		t.Fatal("Garmin ZIP download was rejected or mislabeled")
	}
}

func TestCancellationIsBoundedAndRedacted(t *testing.T) {
	client := newClient(t, func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() }, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Execute(ctx, "profile", nil); codeOf(err) != "GARMIN_TIMEOUT" {
		t.Fatal("cancellation ignored")
	}
}
