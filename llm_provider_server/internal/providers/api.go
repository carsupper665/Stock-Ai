package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

// Options carries the request's Provider credential, resolved model options and the timeout-bound HTTP client.
type Options struct {
	APIKey  string
	Options map[string]json.RawMessage
	Client  *http.Client
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
