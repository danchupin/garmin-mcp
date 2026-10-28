package personal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danchupin/garmin-personal-mcp/internal/domain"
)

const tokenPath = "/personalapitoken-service/personal-api/personal-api-key/token"
const jsonBytes = 4 * 1024 * 1024
const fileBytes = 25 * 1024 * 1024
const tokenBytes = 16384

type Result = domain.PersonalResult

type Options struct {
	HTTPClient *http.Client
	Now        func() time.Time
}

type Client struct {
	base                 string
	http                 *http.Client
	now                  func() time.Time
	mu                   sync.Mutex
	token                string
	expiry               time.Time
	rateUntil, authUntil time.Time
	slots                chan struct{}
	waiting              int
}

func httpClientForRegion(region string, options Options) (*Client, error) {
	bases := map[string]string{"global": "https://personal.connectapi.garmin.com", "china": "https://personal.connectapi.garmin.cn"}
	base, ok := bases[region]
	if !ok {
		return nil, domain.NewError("INVALID_REGION", "The Garmin region is invalid.", 400)
	}
	client := http.Client{}
	if options.HTTPClient != nil {
		client = *options.HTTPClient
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Client{base: base, http: &client, now: now, slots: make(chan struct{}, 3)}, nil
}

// NewAccess never renews a token and never accepts a Personal API key.

func NewAccess(token, region string, options Options) (*Client, error) {
	expiry, err := TokenExpiry(token)
	if err != nil {
		return nil, err
	}
	client, err := httpClientForRegion(region, options)
	if err != nil {
		return nil, err
	}
	if !expiry.After(client.now().Add(time.Minute)) || expiry.After(client.now().Add(24*time.Hour+time.Minute)) {
		return nil, domain.NewError("UNAUTHORIZED", "The Garmin access token has expired or has an invalid lifetime.", 401)
	}
	client.token, client.expiry = token, expiry
	return client, nil
}

func TokenExpiry(token string) (time.Time, error) {
	if token != strings.TrimSpace(token) || len(token) > 8192 {
		return time.Time{}, invalidResponse()
	}
	return expiryOf(token)
}

// Exchange is used only by local CLI commands and the local stdio credential source.
func Exchange(ctx context.Context, key, region string, options Options) (domain.Credential, error) {
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return domain.Credential{}, domain.NewError("INVALID_CREDENTIAL", "A valid local Personal API key is required.", 400)
	}
	client, err := httpClientForRegion(region, options)
	if err != nil {
		return domain.Credential{}, err
	}
	response, err := client.request(ctx, http.MethodPost, tokenPath, "ApiKey "+key, 8192)
	key = ""
	if err != nil {
		return domain.Credential{}, err
	}
	defer clear(response.data)
	if response.status != http.StatusOK {
		return domain.Credential{}, upstreamError(response.status)
	}
	token := strings.TrimSpace(string(response.data))
	expires, err := TokenExpiry(token)
	if err != nil || !expires.After(client.now().Add(time.Minute)) || expires.After(client.now().Add(24*time.Hour+time.Minute)) {
		return domain.Credential{}, invalidResponse()
	}
	return domain.Credential{Token: token, Region: region, ExpiresAt: expires}, nil
}

func upstreamError(status int) error {
	switch status {
	case 401, 403:
		return domain.NewError("UNAUTHORIZED", "Garmin authorization failed. Check the local Personal API key; run login and restart MCP after replacing it.", 401)
	case 429:
		return domain.NewError("GARMIN_RATE_LIMITED", "Garmin rate limit reached. Try again later.", 429)
	case 404:
		return domain.NewError("GARMIN_NOT_FOUND", "The requested Garmin record was not found.", 404)
	default:
		return domain.NewError("GARMIN_UPSTREAM", "Garmin could not complete the request.", 502)
	}
}

func invalidResponse() error {
	return domain.NewError("GARMIN_INVALID_RESPONSE", "Garmin returned an unexpected response.", 502)
}
func requestFailure(ctx context.Context) error {
	if ctx.Err() != nil {
		return domain.NewError("GARMIN_TIMEOUT", "Garmin request timed out.", 504)
	}
	return upstreamError(502)
}

func boundedBody(body io.Reader, contentLength int64, maximum int64) ([]byte, error) {
	if contentLength > maximum {
		return nil, domain.NewError("RESPONSE_TOO_LARGE", "Garmin response exceeds the operation size limit.", 502)
	}
	data, err := io.ReadAll(io.LimitReader(body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, domain.NewError("RESPONSE_TOO_LARGE", "Garmin response exceeds the operation size limit.", 502)
	}
	return data, nil
}

type received struct {
	status      int
	data        []byte
	contentType string
}

func (c *Client) request(ctx context.Context, method, path, authorization string, maximum int64) (received, error) {
	c.mu.Lock()
	rateLimited, authFailed := c.rateUntil.After(c.now()), c.authUntil.After(c.now())
	c.mu.Unlock()
	if rateLimited {
		return received{}, upstreamError(429)
	}
	if authFailed {
		return received{}, upstreamError(401)
	}
	requestCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, method, c.base+path, nil)
	if err != nil {
		return received{}, upstreamError(502)
	}
	request.Header.Set("Authorization", authorization)
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("Accept", "*/*")
	response, err := c.http.Do(request)
	if err != nil {
		return received{}, requestFailure(requestCtx)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		seconds, err := strconv.Atoi(response.Header.Get("Retry-After"))
		if err != nil || seconds < 60 {
			seconds = 60
		}
		if seconds > 300 {
			seconds = 300
		}
		c.mu.Lock()
		c.rateUntil = c.now().Add(time.Duration(seconds) * time.Second)
		c.mu.Unlock()
	}
	result := received{status: response.StatusCode, contentType: response.Header.Get("Content-Type")}
	if response.StatusCode != http.StatusOK {
		return result, nil
	}
	result.data, err = boundedBody(response.Body, response.ContentLength, maximum)
	if err != nil {
		var safe *domain.Error
		if errors.As(err, &safe) {
			return received{}, safe
		}
		return received{}, requestFailure(requestCtx)
	}
	return result, nil
}

func expiryOf(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > tokenBytes || strings.ContainsAny(token, "\r\n\t ") {
		return time.Time{}, invalidResponse()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, invalidResponse()
	}
	var value struct {
		Exp json.Number `json:"exp"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return time.Time{}, invalidResponse()
	}
	expires, err := value.Exp.Int64()
	if err != nil || expires <= 0 {
		return time.Time{}, invalidResponse()
	}
	return time.Unix(expires, 0), nil
}

func (c *Client) bearer() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" || !c.expiry.After(c.now()) {
		return "", upstreamError(401)
	}
	return c.token, nil
}

func (c *Client) acquire(ctx context.Context) error {
	select {
	case c.slots <- struct{}{}:
		return nil
	default:
	}
	c.mu.Lock()
	if c.waiting >= 64 {
		c.mu.Unlock()
		return domain.NewError("BUSY", "Too many pending Garmin requests.", 429)
	}
	c.waiting++
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.waiting--; c.mu.Unlock() }()
	select {
	case c.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return requestFailure(ctx)
	}
}

var credentialField = regexp.MustCompile(`(?i)^(authorization|token|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret)$`)

func redact(value any, secrets []string, depth int) (any, error) {
	if depth > 64 {
		return nil, invalidResponse()
	}
	scrub := func(value string) string {
		for _, secret := range secrets {
			if secret != "" {
				value = strings.ReplaceAll(value, secret, "[redacted]")
			}
		}
		return value
	}
	switch v := value.(type) {
	case nil, bool:
		return v, nil
	case json.Number:
		// Keep unsafe integer literals exact, including exponent notation.
		n, err := v.Float64()
		if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
			return nil, invalidResponse()
		}
		if math.Trunc(n) == n && math.Abs(n) > maxSafeInteger {
			return v.String(), nil
		}
		return v, nil
	case string:
		return scrub(v), nil
	case []any:
		output := make([]any, len(v))
		for i, item := range v {
			var err error
			output[i], err = redact(item, secrets, depth+1)
			if err != nil {
				return nil, err
			}
		}
		return output, nil
	case map[string]any:
		output := make(map[string]any, len(v))
		for name, item := range v {
			if credentialField.MatchString(name) {
				output[scrub(name)] = "[redacted]"
				continue
			}
			parsed, err := redact(item, secrets, depth+1)
			if err != nil {
				return nil, err
			}
			output[scrub(name)] = parsed
		}
		return output, nil
	default:
		return nil, invalidResponse()
	}
}

func (c *Client) Execute(ctx context.Context, operationID string, args map[string]any) (Result, error) {
	op, err := GetOperation(operationID)
	if err != nil {
		return Result{}, err
	}
	path, err := op.Build(args)
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.acquire(operationCtx); err != nil {
		return Result{}, err
	}
	defer func() { <-c.slots }()
	first, err := c.bearer()
	if err != nil {
		return Result{}, err
	}
	maximum := int64(jsonBytes)
	if op.Binary {
		maximum = fileBytes
	}
	response, err := c.request(operationCtx, http.MethodGet, path, "Bearer "+first, maximum)
	if err != nil {
		return Result{}, err
	}
	if response.status == http.StatusNoContent {
		if op.Binary {
			return Result{}, domain.NewError("GARMIN_NOT_FOUND", "No Garmin activity file is available.", 404)
		}
		return Result{Kind: "json"}, nil
	}
	if response.status != http.StatusOK {
		if response.status == http.StatusUnauthorized || response.status == http.StatusForbidden {
			c.mu.Lock()
			c.authUntil, c.token = c.now().Add(time.Minute), ""
			c.mu.Unlock()
		}
		return Result{}, upstreamError(response.status)
	}
	if op.Binary {
		if len(response.data) == 0 {
			return Result{}, domain.NewError("GARMIN_NOT_FOUND", "No Garmin activity file is available.", 404)
		}
		parameters, _ := op.Normalize(args)
		mime, extension := "application/octet-stream", "bin"
		if strings.Contains(strings.ToLower(response.contentType), "zip") {
			mime, extension = "application/zip", "zip"
		}
		return Result{Kind: "binary", Bytes: response.data, MIMEType: mime, Name: "activity-" + queryValue(parameters["activityId"]) + "." + extension}, nil
	}
	if len(response.data) == 0 {
		return Result{Kind: "json"}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(response.data))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return Result{}, invalidResponse()
	}
	if decoder.Decode(new(any)) != io.EOF {
		return Result{}, invalidResponse()
	}
	payload, err = redact(payload, []string{first}, 0)
	if err != nil {
		return Result{}, err
	}
	return Result{Kind: "json", Data: payload}, nil
}
