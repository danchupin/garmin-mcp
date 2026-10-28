package personal

import (
	"context"
	"sync"
	"time"

	"github.com/danchupin/garmin-personal-mcp/internal/domain"
)

// NewLocalKeySource keeps only the temporary token in RAM and rereads the local key when renewal is needed.
func NewLocalKeySource(readKey func() (string, error), region string, options Options) (func(context.Context) (domain.Credential, error), error) {
	if readKey == nil {
		return nil, domain.NewError("CONFIGURATION", "A local credential reader is required.", 500)
	}
	client, err := httpClientForRegion(region, options)
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var current domain.Credential
	var retryAt time.Time
	var previous error
	return func(ctx context.Context) (domain.Credential, error) {
		mu.Lock()
		defer mu.Unlock()
		if ctx.Err() != nil {
			return domain.Credential{}, requestFailure(ctx)
		}
		if current.ExpiresAt.After(client.now().Add(2 * time.Minute)) {
			return current, nil
		}
		if retryAt.After(client.now()) {
			return domain.Credential{}, previous
		}
		key, err := readKey()
		if err == nil {
			current, err = Exchange(ctx, key, region, options)
		}
		key = ""
		if err != nil {
			current = domain.Credential{}
			previous, retryAt = err, client.now().Add(time.Minute)
			return domain.Credential{}, err
		}
		previous, retryAt = nil, time.Time{}
		return current, nil
	}, nil
}
