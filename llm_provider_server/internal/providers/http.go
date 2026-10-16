package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	maxResponseBody        = 1 << 20
	maxUpstreamFailureBody = 8 << 10
)

type providerCall struct {
	url     string
	headers map[string]string
	body    []byte
}

// postJSON performs the single non-streaming upstream request. Transport, credential and
// rate-limit failures are shared by every REST Api; any other status is left to the Api.
func postJSON(ctx context.Context, client *http.Client, call providerCall) (*http.Response, error) {
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, call.url, bytes.NewReader(call.body))
	if err != nil {
		return nil, ProviderError{http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to create provider request."}
	}
	upstream.Header.Set("Content-Type", "application/json")
	for name, value := range call.headers {
		upstream.Header.Set(name, value)
	}
	response, err := client.Do(upstream)
	if err != nil {
		if IsTimeout(err) {
			return nil, ProviderError{http.StatusGatewayTimeout, "PROVIDER_TIMEOUT", "Provider request timed out."}
		}
		return nil, ProviderError{http.StatusBadGateway, "PROVIDER_UNAVAILABLE", "Provider request failed."}
	}
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		response.Body.Close()
		return nil, ProviderError{http.StatusBadGateway, "AUTHENTICATION_FAILED", "Provider rejected its configured credential."}
	case http.StatusTooManyRequests:
		response.Body.Close()
		return nil, ProviderError{http.StatusTooManyRequests, "RATE_LIMITED", "Provider rate limit was reached."}
	}
	return response, nil
}

func upstreamStatusFailure(upstream *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(upstream.Body, maxUpstreamFailureBody))
	message := fmt.Sprintf("Provider returned HTTP %d.", upstream.StatusCode)
	if category := upstreamFailureCategory(body); category != "" {
		message += " " + category
	}
	return ProviderError{http.StatusBadGateway, "PROVIDER_UNAVAILABLE", message}
}

func upstreamFailureCategory(body []byte) string {
	var failure struct {
		Error struct {
			Code   string `json:"code"`
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"error"`
		Code   string `json:"code"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if json.Unmarshal(body, &failure) != nil {
		return ""
	}
	for _, value := range []string{failure.Error.Code, failure.Error.Type, failure.Error.Status, failure.Code, failure.Type, failure.Status} {
		switch strings.ToLower(value) {
		case "model_not_found", "model_not_found_error", "unknown_model":
			return "Provider does not provide the requested model."
		case "insufficient_quota", "quota_exceeded", "resource_exhausted", "billing_hard_limit_reached", "usage_limit_reached":
			return "Provider quota is exhausted."
		case "authentication_expired", "login_expired", "oauth_token_expired", "token_expired":
			return "Provider login or credential has expired."
		}
	}
	return ""
}

func readProviderBody(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxResponseBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBody {
		return nil, errors.New("provider response exceeds 1 MiB")
	}
	return body, nil
}

func IsTimeout(err error) bool {
	var timeoutError interface{ Timeout() bool }
	return errors.As(err, &timeoutError) && timeoutError.Timeout()
}
