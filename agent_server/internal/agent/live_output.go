package agent

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"stock-ai/agent-server/internal/server"
)

// liveStreamPing keeps a Run stream open through proxies that close quiet connections (nginx
// reads time out after 60 seconds).
const liveStreamPing = 15 * time.Second

// liveOutput holds the text the open model call has streamed so far, for viewers of a running
// Run. It lives only in memory: the Run's progress records the final response once the call ends.
type liveOutput struct {
	mu        sync.Mutex
	call      int // progress index of the model call that is streaming
	open      bool
	content   strings.Builder
	reasoning strings.Builder
	watchers  map[chan liveEvent]struct{}
	closed    bool
}

type liveEvent struct {
	name string
	data any
}

type liveDelta struct {
	Content   string `json:"content,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

// begin starts a model call at the given progress index; its text starts empty.
func (live *liveOutput) begin(call int) {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.call, live.open = call, true
	live.content.Reset()
	live.reasoning.Reset()
	live.broadcastLocked(liveEvent{"call", map[string]int{"index": call}})
}

func (live *liveOutput) append(content, reasoning string) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if !live.open {
		return
	}
	live.content.WriteString(content)
	live.reasoning.WriteString(reasoning)
	live.broadcastLocked(liveEvent{"delta", liveDelta{content, reasoning}})
}

func (live *liveOutput) end() {
	live.mu.Lock()
	live.open = false
	live.mu.Unlock()
}

// close ends the Run: every viewer is told and no new viewer can attach.
func (live *liveOutput) close() {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.closed = true
	for watcher := range live.watchers {
		close(watcher)
	}
	live.watchers = nil
}

// watch attaches a viewer and returns what the open call has streamed so far.
func (live *liveOutput) watch() (chan liveEvent, []liveEvent, bool) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.closed {
		return nil, nil, false
	}
	if live.watchers == nil {
		live.watchers = map[chan liveEvent]struct{}{}
	}
	watcher := make(chan liveEvent, 64)
	live.watchers[watcher] = struct{}{}
	var snapshot []liveEvent
	if live.open {
		snapshot = []liveEvent{
			{"call", map[string]int{"index": live.call}},
			{"delta", liveDelta{live.content.String(), live.reasoning.String()}},
		}
	}
	return watcher, snapshot, true
}

func (live *liveOutput) unwatch(watcher chan liveEvent) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if _, exists := live.watchers[watcher]; exists {
		delete(live.watchers, watcher)
		close(watcher)
	}
}

func (live *liveOutput) finished() bool {
	live.mu.Lock()
	defer live.mu.Unlock()
	return live.closed
}

// broadcastLocked never waits for a viewer: one that falls behind is dropped, and its browser
// reconnects and starts again from a fresh snapshot.
func (live *liveOutput) broadcastLocked(event liveEvent) {
	for watcher := range live.watchers {
		select {
		case watcher <- event:
		default:
			delete(live.watchers, watcher)
			close(watcher)
		}
	}
}

// streamRun serves GET .../runs/{run_id}/stream as Server-Sent Events: "call" names the progress
// index of the model call that is streaming, "delta" adds text to it, and "end" means the Run is
// no longer running. Structure (tools, status) still comes from polling the Run.
func (r *AgentRuntime) streamRun(w http.ResponseWriter, request *http.Request, sessionID, rawRunID string) {
	if request.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		server.WriteJSONError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
		return
	}
	runID, err := strconv.ParseInt(rawRunID, 10, 64)
	if err != nil || runID <= 0 {
		server.WriteJSONError(w, http.StatusNotFound, "RUN_NOT_FOUND", "Run does not exist")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	send := func(event liveEvent) {
		data, _ := json.Marshal(event.data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, data)
	}
	ended := liveEvent{"end", struct{}{}}

	r.mu.Lock()
	active := r.active[sessionID]
	r.mu.Unlock()
	var events chan liveEvent
	var snapshot []liveEvent
	watching := false
	if active != nil && active.runID == runID {
		events, snapshot, watching = active.live.watch()
	}
	if !watching {
		send(ended)
		_ = controller.Flush()
		return
	}
	defer active.live.unwatch(events)
	for _, event := range snapshot {
		send(event)
	}
	_ = controller.Flush()
	ping := time.NewTicker(liveStreamPing)
	defer ping.Stop()
	for {
		select {
		case event, open := <-events:
			if !open {
				if active.live.finished() {
					send(ended)
					_ = controller.Flush()
				}
				return
			}
			send(event)
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
		case <-request.Context().Done():
			return
		}
		_ = controller.Flush()
	}
}
