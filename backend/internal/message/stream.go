package message

import "sync"

const (
	EventCreated = "created"
	EventDeleted = "deleted"
	EventCleared = "cleared"

	// streamBuffer 是每個訂閱者最多積壓的事件數；滿了就中斷它，讓它自己重連補齊。
	streamBuffer = 32
)

// Event 是推播給 USER 前端的群聊變動。Message 只在 created 有值，ID 只在 deleted 有值。
type Event struct {
	Name    string
	Message View
	ID      string
}

// streams 只在這個行程內廣播：Backend 只有一個行程，不做跨行程同步。
type streams struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
	closed      bool
}

// Subscribe 回傳事件 channel 與取消訂閱的函式。channel 被關閉表示連線該結束了。
func (s *Service) Subscribe() (<-chan Event, func()) {
	st := s.streams
	ch := make(chan Event, streamBuffer)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.closed {
		close(ch)
		return ch, func() {}
	}
	st.subscribers[ch] = struct{}{}
	return ch, func() {
		st.mu.Lock()
		defer st.mu.Unlock()
		st.drop(ch)
	}
}

// CloseStreams 在關機時結束所有推播連線，否則 graceful shutdown 會一直等它們。
func (s *Service) CloseStreams() {
	st := s.streams
	st.mu.Lock()
	defer st.mu.Unlock()
	st.closed = true
	for ch := range st.subscribers {
		st.drop(ch)
	}
}

func (st *streams) publish(event Event) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for ch := range st.subscribers {
		select {
		case ch <- event:
		default:
			st.drop(ch)
		}
	}
}

func (st *streams) drop(ch chan Event) {
	if _, ok := st.subscribers[ch]; !ok {
		return
	}
	delete(st.subscribers, ch)
	close(ch)
}
