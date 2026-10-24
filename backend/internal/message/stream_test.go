package message

import (
	"context"
	"testing"
)

func subscriberCount(s *Service) int {
	s.streams.mu.Lock()
	defer s.streams.mu.Unlock()
	return len(s.streams.subscribers)
}

func TestStreamDropsSlowAndUnsubscribedClients(t *testing.T) {
	s, _ := newTestService(t)
	slow, _ := s.Subscribe()
	fast, unsubscribe := s.Subscribe()
	if subscriberCount(s) != 2 {
		t.Fatalf("應有 2 個訂閱者，得到 %d", subscriberCount(s))
	}

	for i := 0; i <= streamBuffer; i++ {
		s.streams.publish(Event{Name: EventCleared})
		<-fast
	}
	for range slow {
	}
	if subscriberCount(s) != 1 {
		t.Fatalf("緩衝滿的訂閱者應被中斷，剩 %d", subscriberCount(s))
	}

	unsubscribe()
	unsubscribe()
	if _, open := <-fast; open || subscriberCount(s) != 0 {
		t.Fatalf("取消訂閱後應關閉 channel 並移除，剩 %d", subscriberCount(s))
	}
}

func TestCloseStreamsEndsEveryClient(t *testing.T) {
	s, _ := newTestService(t)
	first, _ := s.Subscribe()
	s.CloseStreams()
	if _, open := <-first; open {
		t.Fatal("關機後既有連線應結束")
	}
	late, _ := s.Subscribe()
	if _, open := <-late; open {
		t.Fatal("關機後的新連線應立即結束")
	}
	mustPost(t, s, alpha, "關機後發言不會卡住")
	if err := s.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
}
