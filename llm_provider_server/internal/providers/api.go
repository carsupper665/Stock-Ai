package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Api is one wire protocol, following pi-ai's packages/ai layering: it builds the vendor request,
// owns its authentication, calls the Provider and normalizes the reply into the Gateway contract.
type Api interface {
	Complete(ctx context.Context, model Model, request GenerateRequest, options Options) (GenerateResponse, error)
}

// Model is the resolved Provider row and model ID one Generate runs against.
type Model struct {
	Provider string
	ID       string
	BaseURL  string
	Config   json.RawMessage
}

// Options carries the request's Provider credential, resolved model options and the HTTP client.
// The call fails only when the Provider sends nothing for IdleTimeout: before its headers or between
// two chunks of the stream. OnDelta, when set, receives the text as it streams in.
type Options struct {
	APIKey      string
	Options     map[string]json.RawMessage
	Client      *http.Client
	IdleTimeout time.Duration
	OnDelta     func(content, reasoning string)
}

func (options Options) delta(content, reasoning string) {
	if options.OnDelta != nil && (content != "" || reasoning != "") {
		options.OnDelta(content, reasoning)
	}
}

var errCredentialRequired = ProviderError{http.StatusServiceUnavailable, "TOKEN_UNAVAILABLE", "This Provider type requires a Provider credential."}

// invalidRequest classifies a request-building failure that is not already a Provider error.
func invalidRequest(err error) error {
	var failure ProviderError
	if errors.As(err, &failure) {
		return err
	}
	return ProviderError{http.StatusBadRequest, "REQUEST_INVALID", err.Error()}
}
