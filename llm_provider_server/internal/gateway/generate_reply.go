package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"stock-ai/llm-provider-server/internal/providers"
)

// generatePingInterval keeps a streamed Generate visibly alive while the Provider is silent, so
// the caller's idle timeout measures this Server, not the model's thinking time.
const generatePingInterval = 5 * time.Second

// generateReply answers one Generate. A caller that accepts application/x-ndjson gets the reply as
// it happens: text deltas, a ping every few seconds, then exactly one done or error line. Anyone
// else gets the single JSON response or error envelope, as before.
type generateReply struct {
	w      http.ResponseWriter
	stream bool
	mu     sync.Mutex
	stop   chan struct{}
	ended  sync.Once
	pinged sync.WaitGroup
}

func newGenerateReply(w http.ResponseWriter, r *http.Request) *generateReply {
	reply := &generateReply{w: w, stream: strings.Contains(r.Header.Get("Accept"), "application/x-ndjson")}
	if !reply.stream {
		return reply
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	reply.flush()
	reply.stop = make(chan struct{})
	reply.pinged.Go(func() {
		ticker := time.NewTicker(generatePingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				reply.line(map[string]string{"type": "ping"})
			case <-reply.stop:
				return
			}
		}
	})
	return reply
}

// delta forwards streamed text; it is nil for a JSON reply, which carries only the final response.
func (reply *generateReply) delta() func(content, reasoning string) {
	if !reply.stream {
		return nil
	}
	return func(content, reasoning string) {
		reply.line(struct {
			Type      string `json:"type"`
			Content   string `json:"content,omitempty"`
			Reasoning string `json:"reasoning,omitempty"`
		}{"delta", content, reasoning})
	}
}

func (reply *generateReply) succeed(response providers.GenerateResponse) {
	if !reply.stream {
		writeJSON(reply.w, http.StatusOK, response)
		return
	}
	reply.finish(map[string]any{"type": "done", "response": response})
}

// fail logs only the Provider and error code: Provider prose may echo credentials or prompts, and
// diagnostic model names are chosen by callers.
func (reply *generateReply) fail(provider Provider, err error) {
	status, code, message := providerFailure(err)
	log.Printf("event=provider_error provider=%s type=%s status=%d detail=%s", provider.ID, provider.Type, status, code)
	if !reply.stream {
		writeError(reply.w, status, code, message)
		return
	}
	reply.finish(map[string]any{"type": "error", "status": status, "error": map[string]string{"code": code, "message": message}})
}

func (reply *generateReply) finish(value any) {
	reply.end()
	reply.line(value)
}

// end stops the pings; nothing may write to the response after its handler returns.
func (reply *generateReply) end() {
	if reply.stream {
		reply.ended.Do(func() { close(reply.stop); reply.pinged.Wait() })
	}
}

func (reply *generateReply) line(value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	reply.mu.Lock()
	defer reply.mu.Unlock()
	_, _ = reply.w.Write(append(encoded, '\n'))
	reply.flush()
}

// flush goes through the ResponseController because the access log wraps the writer.
func (reply *generateReply) flush() { _ = http.NewResponseController(reply.w).Flush() }

func providerFailure(err error) (int, string, string) {
	status, code, message := http.StatusBadGateway, "PROVIDER_RESPONSE_INVALID", "Provider returned an invalid response."
	var failure providers.ProviderError
	switch {
	case errors.As(err, &failure):
		status, code, message = failure.Status, failure.Code, failure.Message
	case providers.IsTimeout(err):
		status, code, message = http.StatusGatewayTimeout, "PROVIDER_TIMEOUT", "Provider request timed out."
	case errors.Is(err, context.Canceled):
		status, code, message = http.StatusBadGateway, "PROVIDER_UNAVAILABLE", "Provider request was cancelled."
	}
	return status, code, message
}
