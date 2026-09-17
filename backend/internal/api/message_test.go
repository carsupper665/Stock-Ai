package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func messageNames(body map[string]any) []string {
	items, _ := body["messages"].([]any)
	out := make([]string, 0, len(items))
	for _, raw := range items {
		message, _ := raw.(map[string]any)
		name, _ := message["user_name"].(string)
		out = append(out, name)
	}
	return out
}

func TestPostMessageRequiresTokenAndReturnsTags(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 100)

	if status, _ := do(t, engine, http.MethodPost, "/v1/messages", "", gin.H{"content": "hi"}); status != http.StatusUnauthorized {
		t.Fatalf("未帶 token 發布應回 401，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodPost, "/v1/messages", "at_bogus", gin.H{"content": "hi"}); status != http.StatusUnauthorized {
		t.Fatalf("無效 token 發布應回 401，得到 %d", status)
	}

	status, body := do(t, engine, http.MethodPost, "/v1/messages", token, gin.H{
		"content": "BTC breakout looks valid.", "tags": []string{"ETH-Agent-02"},
	})
	if status != http.StatusCreated || body["user_name"] != "you" || body["content"] != "BTC breakout looks valid." {
		t.Fatalf("發布回應不符: %d %v", status, body)
	}
	tags, _ := body["tags"].([]any)
	if len(tags) != 1 || tags[0] != "ETH-Agent-02" {
		t.Fatalf("回應應帶 tags: %v", body)
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

func TestPostMessageValidation(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 100)

	for _, field := range []string{"author_id", "author_name", "author_type"} {
		status, body := do(t, engine, http.MethodPost, "/v1/messages", token, gin.H{"content": "hi", field: "spoof"})
		if status != http.StatusBadRequest {
			t.Fatalf("帶 %s 應回 400，得到 %d: %v", field, status, body)
		}
	}
	for _, body := range []gin.H{
		{"content": "   "},
		{"content": "hi", "tags": []string{"x", " x "}},
		{"content": "hi", "tags": []string{""}},
	} {
		if status, _ := do(t, engine, http.MethodPost, "/v1/messages", token, body); status != http.StatusBadRequest {
			t.Fatalf("無效內容應回 400，得到 %d: %v", status, body)
		}
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
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			status, body := do(t, engine, http.MethodGet, "/v1/messages?sort=asc", test.token, nil)
			if status != http.StatusOK || fmt.Sprint(messageNames(body)) != fmt.Sprint(test.want) {
				t.Fatalf("查詢不符: %d got=%v want=%v", status, messageNames(body), test.want)
			}
		})
	}
	if status, _ := do(t, engine, http.MethodGet, "/v1/messages", "at_bogus", nil); status != http.StatusUnauthorized {
		t.Fatalf("無效 token 查詢應回 401，得到 %d", status)
	}
}

func TestListMessagesQueryValidation(t *testing.T) {
	engine, _ := newTestServer(t)
	for _, bad := range []string{"page=0", "page=abc", "sort=sideways", "tag=%20%20", "limit=10", "order=asc", "page=1&page=2"} {
		if status, _ := do(t, engine, http.MethodGet, "/v1/messages?"+bad, "", nil); status != http.StatusBadRequest {
			t.Fatalf("%s 應回 400，得到 %d", bad, status)
		}
	}
	if status, _ := do(t, engine, http.MethodGet, "/v1/messages?page=2&sort=desc&tag=agent-name", "", nil); status != http.StatusOK {
		t.Fatalf("合法參數應回 200，得到 %d", status)
	}
}

func TestMessagesPageFilterAndDeleteAuthorization(t *testing.T) {
	engine, _ := newTestServer(t)
	_, alpha := createAccountFor(t, engine, "BTC-Agent-01", 100)
	_, beta := createAccountFor(t, engine, "ETH-Agent-02", 100)
	ids := make([]string, 0, 12)
	for index := 0; index < 12; index++ {
		status, body := do(t, engine, http.MethodPost, "/v1/messages", alpha, gin.H{
			"content": fmt.Sprint(index), "tags": []string{"ETH-Agent-02"},
		})
		if status != http.StatusCreated {
			t.Fatal(status, body)
		}
		ids = append(ids, body["id"].(string))
	}
	status, body := do(t, engine, http.MethodGet, "/v1/messages?page=1&sort=asc&tag=ETH-Agent-02", beta, nil)
	if status != http.StatusOK || body["page"] != float64(1) || body["has_more"] != true || len(body["messages"].([]any)) != 10 {
		t.Fatalf("固定十筆分頁回應不符: %d %v", status, body)
	}
	if status, _ = do(t, engine, http.MethodDelete, "/v1/messages/"+ids[0], beta, nil); status != http.StatusForbidden {
		t.Fatalf("非作者帳號刪除應回 403，得到 %d", status)
	}
	if status, _ = do(t, engine, http.MethodDelete, "/v1/messages/"+ids[0], alpha, nil); status != http.StatusNoContent {
		t.Fatalf("作者刪除應回 204，得到 %d", status)
	}
	if status, _ = do(t, engine, http.MethodDelete, "/v1/messages/"+ids[1], testUserToken, nil); status != http.StatusNoContent {
		t.Fatalf("USER 管理刪除應回 204，得到 %d", status)
	}
}
