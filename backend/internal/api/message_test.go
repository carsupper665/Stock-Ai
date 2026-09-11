package api

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func messageNames(body map[string]any) []string {
	items, _ := body["messages"].([]any)
	out := make([]string, 0, len(items))
	for _, raw := range items {
		m, _ := raw.(map[string]any)
		name, _ := m["user_name"].(string)
		out = append(out, name)
	}
	return out
}

func TestPostMessageRequiresTokenAndReturnsYou(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 100)

	if status, _ := do(t, engine, http.MethodPost, "/v1/messages", "", gin.H{"content": "hi"}); status != http.StatusUnauthorized {
		t.Fatalf("未帶 token 發布應回 401，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodPost, "/v1/messages", "at_bogus", gin.H{"content": "hi"}); status != http.StatusUnauthorized {
		t.Fatalf("無效 token 發布應回 401，得到 %d", status)
	}

	status, body := do(t, engine, http.MethodPost, "/v1/messages", token, gin.H{"content": "BTC breakout looks valid."})
	if status != http.StatusCreated {
		t.Fatalf("發布應回 201，得到 %d: %v", status, body)
	}
	if body["user_name"] != "you" || body["content"] != "BTC breakout looks valid." {
		t.Fatalf("回應應以 you 顯示自己: %v", body)
	}
	for _, hidden := range []string{"author_id", "author_type"} {
		if _, present := body[hidden]; present {
			t.Fatalf("不該外露 %s: %v", hidden, body)
		}
	}

	status, body = do(t, engine, http.MethodPost, "/v1/messages", testUserToken, gin.H{"content": "from user"})
	if status != http.StatusCreated || body["user_name"] != "you" {
		t.Fatalf("USER 也可以發布: %d %v", status, body)
	}
}

func TestPostMessageRejectsClientSuppliedAuthor(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 100)

	for _, field := range []string{"author_id", "author_name", "author_type"} {
		status, body := do(t, engine, http.MethodPost, "/v1/messages", token, gin.H{"content": "hi", field: "spoof"})
		if status != http.StatusBadRequest {
			t.Fatalf("帶 %s 應回 400，得到 %d: %v", field, status, body)
		}
	}
	if status, _ := do(t, engine, http.MethodPost, "/v1/messages", token, gin.H{"content": "   "}); status != http.StatusBadRequest {
		t.Fatalf("空白內容應回 400，得到 %d", status)
	}
}

func TestListMessagesOptionalAuth(t *testing.T) {
	engine, _ := newTestServer(t)
	_, alpha := createAccountFor(t, engine, "BTC-Agent-01", 100)
	_, beta := createAccountFor(t, engine, "ETH-Agent-02", 100)

	do(t, engine, http.MethodPost, "/v1/messages", testUserToken, gin.H{"content": "1"})
	do(t, engine, http.MethodPost, "/v1/messages", alpha, gin.H{"content": "2"})
	do(t, engine, http.MethodPost, "/v1/messages", beta, gin.H{"content": "3"})

	cases := []struct {
		name  string
		token string
		want  []string
	}{
		{"匿名", "", []string{"Bless", "BTC-Agent-01", "ETH-Agent-02"}},
		{"USER", testUserToken, []string{"you", "BTC-Agent-01", "ETH-Agent-02"}},
		{"alpha", alpha, []string{"Bless", "you", "ETH-Agent-02"}},
		{"beta", beta, []string{"Bless", "BTC-Agent-01", "you"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := do(t, engine, http.MethodGet, "/v1/messages?order=asc", tc.token, nil)
			if status != http.StatusOK {
				t.Fatalf("查詢應回 200，得到 %d: %v", status, body)
			}
			got := messageNames(body)
			if len(got) != len(tc.want) {
				t.Fatalf("數量不符: %v", got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("顯示名稱不符: 得到 %v，預期 %v", got, tc.want)
				}
			}
		})
	}

	// 規格 §17：帶錯 token 必須 401，不可以默默當成匿名。
	if status, _ := do(t, engine, http.MethodGet, "/v1/messages", "at_bogus", nil); status != http.StatusUnauthorized {
		t.Fatalf("無效 token 查詢應回 401，得到 %d", status)
	}
}

func TestListMessagesQueryValidation(t *testing.T) {
	engine, _ := newTestServer(t)

	for _, bad := range []string{"limit=0", "limit=abc", "order=sideways", "after=yesterday", "before=2026-13-01"} {
		if status, _ := do(t, engine, http.MethodGet, "/v1/messages?"+bad, "", nil); status != http.StatusBadRequest {
			t.Fatalf("%s 應回 400，得到 %d", bad, status)
		}
	}
	if status, _ := do(t, engine, http.MethodGet, "/v1/messages?limit=30&order=desc&after=2020-01-01T00:00:00Z", "", nil); status != http.StatusOK {
		t.Fatalf("合法參數應回 200，得到 %d", status)
	}
}
