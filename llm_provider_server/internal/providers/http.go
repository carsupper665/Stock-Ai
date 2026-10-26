package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
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

// errProviderIdle is the cancellation cause when the Provider sends nothing for the idle timeout.
var errProviderIdle = idleTimeoutError{}

type idleTimeoutError struct{}

func (idleTimeoutError) Error() string { return "Provider sent nothing within the idle timeout" }
func (idleTimeoutError) Timeout() bool { return true }

// postJSON performs the single upstream request. It fails only when the Provider stays silent for
// options.IdleTimeout, before its headers or between two reads of the body, so a model that keeps
// streaming may take as long as it needs. Transport, credential and rate-limit failures are shared
// by every REST Api; any other status is left to the Api.
func postJSON(ctx context.Context, options Options, call providerCall) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	var idle *time.Timer
	if options.IdleTimeout > 0 {
		idle = time.AfterFunc(options.IdleTimeout, func() { cancel(errProviderIdle) })
	}
	release := func() {
		if idle != nil {
			idle.Stop()
		}
		cancel(nil)
	}
	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, call.url, bytes.NewReader(call.body))
	if err != nil {
		release()
		return nil, ProviderError{http.StatusInternalServerError, "INTERNAL_ERROR", "Unable to create provider request."}
	}
	upstream.Header.Set("Content-Type", "application/json")
	for name, value := range call.headers {
		upstream.Header.Set(name, value)
	}
	response, err := options.Client.Do(upstream)
	if err != nil {
		timedOut := IsTimeout(err) || errors.Is(context.Cause(ctx), errProviderIdle)
		release()
		if timedOut {
			return nil, ProviderError{http.StatusGatewayTimeout, "PROVIDER_TIMEOUT", "Provider request timed out."}
		}
		return nil, ProviderError{http.StatusBadGateway, "PROVIDER_UNAVAILABLE", redact("Provider request failed: "+err.Error(), options.APIKey)}
	}
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		body := readFailureBody(response.Body)
		response.Body.Close()
		release()
		return nil, ProviderError{http.StatusBadGateway, "AUTHENTICATION_FAILED", providerSaid("Provider rejected its configured credential.", body, options.APIKey)}
	case http.StatusTooManyRequests:
		body := readFailureBody(response.Body)
		response.Body.Close()
		release()
		return nil, ProviderError{http.StatusTooManyRequests, "RATE_LIMITED", providerSaid("Provider rate limit was reached.", body, options.APIKey)}
	}
	if idle != nil {
		idle.Reset(options.IdleTimeout)
	}
	response.Body = &idleBody{ReadCloser: response.Body, ctx: ctx, idle: idle, timeout: options.IdleTimeout, release: release}
	return response, nil
}

// idleBody restarts the idle timer whenever bytes arrive and reports the idle cancellation as a timeout.
type idleBody struct {
	io.ReadCloser
	ctx     context.Context
	idle    *time.Timer
	timeout time.Duration
	release func()
}

func (body *idleBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if n > 0 && body.idle != nil {
		body.idle.Reset(body.timeout)
	}
	if err != nil && err != io.EOF && errors.Is(context.Cause(body.ctx), errProviderIdle) {
		err = errProviderIdle
	}
	return n, err
}

func (body *idleBody) Close() error {
	err := body.ReadCloser.Close()
	body.release()
	return err
}

// readResponseBody returns one JSON response body. A Provider that answers with an event stream has
// its events assembled into the body its non-streaming API would have returned, so every Api keeps a
// single validation path; a Provider that ignores the stream request is read as before.
func readResponseBody(upstream *http.Response, assemble func(io.Reader) ([]byte, error)) ([]byte, error) {
	mediaType, _, _ := mime.ParseMediaType(upstream.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		return assemble(upstream.Body)
	}
	return readProviderBody(upstream.Body)
}

// streamFailure is an error event inside a 200 event stream.
func streamFailure(data []byte, apiKey string) error {
	return ProviderError{http.StatusBadGateway, "PROVIDER_UNAVAILABLE", providerSaid("Provider stream reported an error.", data, apiKey)}
}

// readServerSentEvents calls handle with each event's name and data until the stream ends or
// handle reports that it is done. One event, like a whole response, is bounded by 1 MiB.
func readServerSentEvents(reader io.Reader, handle func(event string, data []byte) (bool, error)) error {
	lines := bufio.NewReaderSize(reader, 64<<10)
	var event string
	var data bytes.Buffer
	hasData := false
	for {
		line, err := lines.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long := append([]byte(nil), line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				if len(long) > maxResponseBody {
					return errors.New("provider stream event exceeds 1 MiB")
				}
				line, err = lines.ReadSlice('\n')
				long = append(long, line...)
			}
			line = long
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		text := bytes.TrimRight(line, "\r\n")
		switch {
		case len(text) == 0:
			if hasData {
				done, handleErr := handle(event, data.Bytes())
				if handleErr != nil || done {
					return handleErr
				}
			}
			event, hasData = "", false
			data.Reset()
		case text[0] == ':':
		default:
			field, value, _ := bytes.Cut(text, []byte(":"))
			value = bytes.TrimPrefix(value, []byte(" "))
			switch string(field) {
			case "event":
				event = string(value)
			case "data":
				if hasData {
					data.WriteByte('\n')
				}
				if data.Len()+len(value) > maxResponseBody {
					return errors.New("provider stream event exceeds 1 MiB")
				}
				data.Write(value)
				hasData = true
			}
		}
		if errors.Is(err, io.EOF) {
			if hasData {
				_, handleErr := handle(event, data.Bytes())
				return handleErr
			}
			return nil
		}
	}
}

// streamText bounds the text an assembled stream may accumulate, like a whole response body.
type streamText struct{ size int }

func (text *streamText) add(values ...string) error {
	for _, value := range values {
		text.size += len(value)
	}
	if text.size > maxResponseBody {
		return errors.New("provider response exceeds 1 MiB")
	}
	return nil
}

func upstreamStatusFailure(upstream *http.Response, apiKey string) error {
	body := readFailureBody(upstream.Body)
	message := fmt.Sprintf("Provider returned HTTP %d.", upstream.StatusCode)
	if category := upstreamFailureCategory(body); category != "" {
		message += " " + category
	}
	message = providerSaid(message, body, apiKey)
	return ProviderError{http.StatusBadGateway, "PROVIDER_UNAVAILABLE", message}
}

func readFailureBody(reader io.Reader) []byte {
	body, _ := io.ReadAll(io.LimitReader(reader, maxUpstreamFailureBody))
	return body
}

// providerSaid appends the Provider's own error text, bounded and without the request credential,
// so a failed call can be diagnosed from the Run.
func providerSaid(message string, body []byte, apiKey string) string {
	detail := strings.TrimSpace(string(body))
	if len(detail) > maxUpstreamFailureBody {
		detail = detail[:maxUpstreamFailureBody]
	}
	if detail == "" {
		return message
	}
	return redact(message+" Provider said: "+detail, apiKey)
}

func redact(text, apiKey string) string {
	if apiKey == "" {
		return text
	}
	return strings.ReplaceAll(text, apiKey, "[REDACTED]")
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
