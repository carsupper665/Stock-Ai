package api

import (
	"backend/internal/database"
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestChatMutedPostAndExpiry(t *testing.T) {
	engine, store := newTestServer(t)
	id, token := createAccountFor(t, engine, "muted", 100)
	_, muted := do(t, engine, "POST", "/v1/accounts/"+id+"/chat/mute", testUserToken, gin.H{"minutes": 10, "reason": "洗版 😀"})
	status, body := do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "hi"})
	text, _ := body["message"].(string)
	until, _ := time.Parse(time.RFC3339Nano, muted["chat_muted_until"].(string))
	if status != 403 || body["error"] != "chat_muted" || !strings.Contains(text, "洗版 😀") || !strings.Contains(text, "剩餘 10 分鐘") || !strings.Contains(text, until.UTC().Format(time.RFC3339)) {
		t.Fatalf("muted post: %d %v", status, body)
	}
	acc, _ := store.AccountByID(context.Background(), id)
	past := time.Now().Add(-time.Second)
	acc.ChatMutedUntil = &past
	if err := store.SaveAccount(context.Background(), acc); err != nil {
		t.Fatal(err)
	}
	if status, body = do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "after expiry"}); status != 201 {
		t.Fatalf("expiry: %d %v", status, body)
	}
	status, body = do(t, engine, "GET", "/v1/account", token, nil)
	if status != 200 || body["chat_muted_until"] != nil || body["chat_mute_reason"] != nil {
		t.Fatalf("expired view: %d %v", status, body)
	}
}

func TestChatRateDefault(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "rate", 100)
	status, body := do(t, engine, "GET", "/v1/account", token, nil)
	if status != 200 || body["chat_rate_minutes"] != float64(1) || body["chat_rate_count"] != float64(3) {
		t.Fatalf("defaults: %d %v", status, body)
	}
	for i := 0; i < 3; i++ {
		if status, body := do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "hi"}); status != 201 {
			t.Fatalf("allowed %d: %d %v", i, status, body)
		}
	}
	status, body = do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "fourth"})
	text, _ := body["message"].(string)
	if status != 429 || body["error"] != "chat_rate_limited" || !strings.Contains(text, "每 1 分鐘最多 3 則") || !strings.Contains(text, "約 1 分鐘後") {
		t.Fatalf("rate: %d %v", status, body)
	}
}

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
	createAccountFor(t, engine, "ETH-Agent-02", 100)

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

	status, body = do(t, engine, http.MethodPost, "/v1/messages", testUserToken, gin.H{"content": "from user", "tags": []string{"BTC-Agent-01"}})
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

func TestPostMessageTagsAreOptionalButMustNameOthers(t *testing.T) {
	engine, _ := newTestServer(t)
	_, alpha := createAccountFor(t, engine, "BTC-Agent-01", 100)
	createAccountFor(t, engine, "ETH-Agent-02", 100)

	for _, test := range []struct {
		name  string
		token string
		tags  []string
	}{
		{"不存在的帳號", alpha, []string{"risk"}},
		{"帳號 tag 自己", alpha, []string{"BTC-Agent-01"}},
		{"USER tag 自己", testUserToken, []string{"USER"}},
		{"USER 用 USER_NAME tag 自己", testUserToken, []string{"Bless"}},
		{"USER 與 USER_NAME 重複", alpha, []string{"USER", "Bless"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := gin.H{"content": "hi"}
			if test.tags != nil {
				body["tags"] = test.tags
			}
			if status, resp := do(t, engine, http.MethodPost, "/v1/messages", test.token, body); status != http.StatusBadRequest {
				t.Fatalf("應回 400，得到 %d: %v", status, resp)
			}
		})
	}

	for _, body := range []gin.H{{"content": "no tags"}, {"content": "empty tags", "tags": []string{}}} {
		if status, resp := do(t, engine, http.MethodPost, "/v1/messages", alpha, body); status != http.StatusCreated {
			t.Fatalf("群聊的 tag 是選填，應回 201，得到 %d: %v", status, resp)
		}
	}

	status, body := do(t, engine, http.MethodPost, "/v1/messages", alpha, gin.H{"content": "bug report", "tags": []string{"Bless", "ETH-Agent-02"}})
	tags, _ := body["tags"].([]any)
	if status != http.StatusCreated || fmt.Sprint(tags) != "[USER ETH-Agent-02]" {
		t.Fatalf("USER_NAME 應存成 USER: %d %v", status, body)
	}
	status, body = do(t, engine, http.MethodGet, "/v1/messages?tag=USER", testUserToken, nil)
	if status != http.StatusOK || len(body["messages"].([]any)) != 1 {
		t.Fatalf("tag=USER 應查到寄給 USER 的留言: %d %v", status, body)
	}
}

func TestListMessagesOptionalAuth(t *testing.T) {
	engine, _ := newTestServer(t)
	_, alpha := createAccountFor(t, engine, "BTC-Agent-01", 100)
	_, beta := createAccountFor(t, engine, "ETH-Agent-02", 100)

	do(t, engine, http.MethodPost, "/v1/messages", testUserToken, gin.H{"content": "1", "tags": []string{"BTC-Agent-01"}})
	do(t, engine, http.MethodPost, "/v1/messages", alpha, gin.H{"content": "2", "tags": []string{"USER"}})
	do(t, engine, http.MethodPost, "/v1/messages", beta, gin.H{"content": "3", "tags": []string{"USER"}})

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
	for _, bad := range []string{"page=0", "page=abc", "sort=sideways", "tag=%20%20", "limit=51", "order=asc", "page=1&page=2"} {
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
	id, alpha := createAccountFor(t, engine, "BTC-Agent-01", 100)
	_, beta := createAccountFor(t, engine, "ETH-Agent-02", 100)
	do(t, engine, http.MethodPatch, "/v1/accounts/"+id, testUserToken, gin.H{"chat_rate_count": 100})
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

func TestChatPermissionsAreEnforcedPerAccount(t *testing.T) {
	engine, _ := newTestServer(t)
	id, alpha := createAccountFor(t, engine, "BTC-Agent-01", 100)
	createAccountFor(t, engine, "ETH-Agent-02", 100)

	status, body := do(t, engine, http.MethodPatch, "/v1/accounts/"+id, testUserToken, gin.H{"chat_post": false})
	if status != http.StatusOK || body["chat_post"] != false || body["chat_read"] != true {
		t.Fatalf("設為唯讀: %d %v", status, body)
	}
	status, body = do(t, engine, http.MethodPost, "/v1/messages", alpha, gin.H{"content": "hi"})
	if status != http.StatusForbidden || body["error"] != "chat_post_disabled" || body["message"] != "此帳號已被管理者停用群聊發言權限" {
		t.Fatalf("唯讀帳號發言應回 403 chat_post_disabled: %d %v", status, body)
	}
	if status, body := do(t, engine, http.MethodGet, "/v1/messages", alpha, nil); status != http.StatusOK {
		t.Fatalf("唯讀帳號仍可讀: %d %v", status, body)
	}

	if status, body := do(t, engine, http.MethodPatch, "/v1/accounts/"+id, testUserToken, gin.H{"chat_read": false}); status != http.StatusOK || body["chat_read"] != false {
		t.Fatalf("停權: %d %v", status, body)
	}
	status, body = do(t, engine, http.MethodGet, "/v1/messages", alpha, nil)
	if status != http.StatusForbidden || body["error"] != "chat_read_disabled" {
		t.Fatalf("停權帳號讀取應回 403 chat_read_disabled: %d %v", status, body)
	}
	if status, _ := do(t, engine, http.MethodPost, "/v1/messages", testUserToken, gin.H{"content": "USER 不受限", "tags": []string{"BTC-Agent-01"}}); status != http.StatusCreated {
		t.Fatalf("USER 發言應不受帳號權限影響: %d", status)
	}

	status, body = do(t, engine, http.MethodPatch, "/v1/accounts/"+id, testUserToken, gin.H{"chat_read": true, "chat_post": true})
	if status != http.StatusOK || body["chat_read"] != true || body["chat_read_since"] == nil {
		t.Fatalf("重新開放讀取應記下 chat_read_since: %d %v", status, body)
	}
}

func TestListMessagesAfterReturnsOnlyLaterMessages(t *testing.T) {
	engine, _ := newTestServer(t)
	_, alpha := createAccountFor(t, engine, "BTC-Agent-01", 100)
	ids := make([]string, 0, 3)
	for _, content := range []string{"one", "two", "three"} {
		status, body := do(t, engine, http.MethodPost, "/v1/messages", alpha, gin.H{"content": content})
		if status != http.StatusCreated {
			t.Fatalf("post %s: %d %v", content, status, body)
		}
		ids = append(ids, body["id"].(string))
	}
	status, body := do(t, engine, http.MethodGet, "/v1/messages?sort=asc&after="+ids[0], alpha, nil)
	messages, _ := body["messages"].([]any)
	if status != http.StatusOK || len(messages) != 2 || messages[0].(map[string]any)["content"] != "two" || messages[1].(map[string]any)["content"] != "three" {
		t.Fatalf("after 應只回之後的留言: %d %v", status, body)
	}
	if status, _ := do(t, engine, http.MethodGet, "/v1/messages?after=msg_missing", alpha, nil); status != http.StatusBadRequest {
		t.Fatalf("不存在的 after 應回 400: %d", status)
	}
}

func TestChatRateWindowDeletionAndIsolation(t *testing.T) {
	engine, store := newTestServer(t)
	id, token := createAccountFor(t, engine, "limited", 100)
	_, other := createAccountFor(t, engine, "other", 100)
	status, body := do(t, engine, "PATCH", "/v1/accounts/"+id, testUserToken, gin.H{"chat_rate_minutes": 2, "chat_rate_count": 1})
	if status != 200 {
		t.Fatal(status, body)
	}
	_, first := do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "first"})
	status, body = do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "second"})
	if status != 429 || !strings.Contains(body["message"].(string), "每 2 分鐘最多 1 則，約 2 分鐘後") {
		t.Fatalf("custom: %d %v", status, body)
	}
	if status, body := do(t, engine, "POST", "/v1/messages", other, gin.H{"content": "independent"}); status != 201 {
		t.Fatalf("other: %d %v", status, body)
	}
	if status, body := do(t, engine, "DELETE", "/v1/messages/"+first["id"].(string), token, nil); status != 204 {
		t.Fatal(status, body)
	}
	if status, body := do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "after deletion"}); status != 201 {
		t.Fatalf("deleted excluded: %d %v", status, body)
	}
	// Move the existing fixture outside the window without waiting two minutes.
	rows, err := store.ListMessages(context.Background(), database.MessageQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.AuthorID == id {
			if err := store.DeleteMessage(context.Background(), row.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	err = store.CreateMessage(context.Background(), &database.Message{ID: "old", AuthorType: database.AuthorAccount, AuthorID: id, Content: "old", CreatedAt: time.Now().UTC().Add(-2*time.Minute - time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if status, body := do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "after window"}); status != 201 {
		t.Fatalf("window expired: %d %v", status, body)
	}
}

func TestChatPostingRestrictionOrderAndUserExemption(t *testing.T) {
	engine, _ := newTestServer(t)
	id, token := createAccountFor(t, engine, "restricted", 100)
	for i := 0; i < 3; i++ {
		if status, body := do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "fill window"}); status != 201 {
			t.Fatal(status, body)
		}
	}
	path := "/v1/accounts/" + id
	do(t, engine, "POST", path+"/chat/mute", testUserToken, gin.H{"minutes": 1, "reason": "原因"})
	do(t, engine, "PATCH", path, testUserToken, gin.H{"chat_post": false})
	status, body := do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "all three"})
	if status != 403 || body["error"] != "chat_post_disabled" {
		t.Fatalf("permission first: %d %v", status, body)
	}
	do(t, engine, "PATCH", path, testUserToken, gin.H{"chat_post": true})
	status, body = do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "mute and rate"})
	if status != 403 || body["error"] != "chat_muted" {
		t.Fatalf("mute second: %d %v", status, body)
	}
	for i := 0; i < 5; i++ {
		if status, body := do(t, engine, "POST", "/v1/messages", testUserToken, gin.H{"content": "USER"}); status != 201 {
			t.Fatalf("USER exempt: %d %v", status, body)
		}
	}
	do(t, engine, "DELETE", path+"/chat/mute", testUserToken, nil)
	status, body = do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "rate only"})
	if status != 429 || body["error"] != "chat_rate_limited" {
		t.Fatalf("rate third: %d %v", status, body)
	}
}

func TestChatRateConcurrentPosts(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "concurrent", 100)
	var wg sync.WaitGroup
	statuses := make(chan int, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, _ := do(t, engine, "POST", "/v1/messages", token, gin.H{"content": "parallel"})
			statuses <- status
		}()
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[201] != 3 || counts[429] != 7 {
		t.Fatalf("concurrent limit: %v", counts)
	}
}

func postContents(t *testing.T, engine *gin.Engine, token string, count int) []string {
	t.Helper()
	ids := make([]string, 0, count)
	for i := 0; i < count; i++ {
		tags := []string{}
		if i%2 == 0 {
			tags = []string{"BTC-Agent-01"}
		}
		status, body := do(t, engine, http.MethodPost, "/v1/messages", token, gin.H{"content": fmt.Sprintf("msg %02d", i), "tags": tags})
		if status != http.StatusCreated {
			t.Fatalf("post %d: %d %v", i, status, body)
		}
		ids = append(ids, body["id"].(string))
		time.Sleep(time.Millisecond)
	}
	return ids
}

func messageContents(body map[string]any) []string {
	items, _ := body["messages"].([]any)
	out := make([]string, 0, len(items))
	for _, raw := range items {
		out = append(out, raw.(map[string]any)["content"].(string))
	}
	return out
}

func TestListMessagesLimit(t *testing.T) {
	engine, _ := newTestServer(t)
	createAccountFor(t, engine, "BTC-Agent-01", 100)
	postContents(t, engine, testUserToken, 60)
	for _, limit := range []string{"0", "51", "-1", "1.5", "abc"} {
		if status, body := do(t, engine, http.MethodGet, "/v1/messages?limit="+limit, testUserToken, nil); status != 400 || body["error"] != "invalid_request" {
			t.Fatalf("limit=%s: %d %v", limit, status, body)
		}
	}
	_, body := do(t, engine, http.MethodGet, "/v1/messages", testUserToken, nil)
	if got := messageContents(body); len(got) != 10 || got[0] != "msg 59" || body["has_more"] != true {
		t.Fatalf("預設一頁 10 則: %v", body)
	}
	_, body = do(t, engine, http.MethodGet, "/v1/messages?limit=15&page=2", testUserToken, nil)
	if got := messageContents(body); len(got) != 15 || got[0] != "msg 44" {
		t.Fatalf("limit 15 第二頁: %v", got)
	}
	_, body = do(t, engine, http.MethodGet, "/v1/messages?limit=50", testUserToken, nil)
	if got := messageContents(body); len(got) != 50 || body["has_more"] != true {
		t.Fatalf("limit 50: %d %v", len(got), body["has_more"])
	}
	_, body = do(t, engine, http.MethodGet, "/v1/messages?limit=1", testUserToken, nil)
	if got := messageContents(body); len(got) != 1 || got[0] != "msg 59" {
		t.Fatalf("limit 1: %v", got)
	}
}

func TestListMessagesBefore(t *testing.T) {
	engine, _ := newTestServer(t)
	createAccountFor(t, engine, "BTC-Agent-01", 100)
	ids := postContents(t, engine, testUserToken, 25)
	get := func(query string) (int, map[string]any) {
		return do(t, engine, http.MethodGet, "/v1/messages?"+query, testUserToken, nil)
	}
	_, body := get("before=" + ids[20] + "&limit=5")
	if got := fmt.Sprint(messageContents(body)); got != "[msg 19 msg 18 msg 17 msg 16 msg 15]" || body["has_more"] != true {
		t.Fatalf("before desc: %s %v", got, body["has_more"])
	}
	_, body = get("before=" + ids[20] + "&limit=5&tag=BTC-Agent-01")
	if got := fmt.Sprint(messageContents(body)); got != "[msg 18 msg 16 msg 14 msg 12 msg 10]" {
		t.Fatalf("before + tag: %s", got)
	}
	_, body = get("before=" + ids[3] + "&sort=asc")
	if got := fmt.Sprint(messageContents(body)); got != "[msg 00 msg 01 msg 02]" || body["has_more"] != false {
		t.Fatalf("before + asc: %s %v", got, body["has_more"])
	}
	if status, body := get("before=" + ids[3] + "&after=" + ids[1]); status != 400 || body["error"] != "invalid_request" {
		t.Fatalf("before + after: %d %v", status, body)
	}
	if status, body := get("before=msg_missing"); status != 400 || body["error"] != "invalid_request" {
		t.Fatalf("missing before: %d %v", status, body)
	}
}

func TestClearMessages(t *testing.T) {
	engine, _ := newTestServer(t)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 100)
	postContents(t, engine, testUserToken, 4)
	if status, body := do(t, engine, http.MethodDelete, "/v1/messages", token, nil); status != 403 {
		t.Fatalf("account token clear: %d %v", status, body)
	}
	if status, body := do(t, engine, http.MethodDelete, "/v1/messages", "", nil); status != 401 {
		t.Fatalf("anonymous clear: %d %v", status, body)
	}
	if status, body := do(t, engine, http.MethodDelete, "/v1/messages", testUserToken, nil); status != 204 {
		t.Fatalf("user clear: %d %v", status, body)
	}
	for _, query := range []string{"", "?tag=BTC-Agent-01"} {
		if status, body := do(t, engine, http.MethodGet, "/v1/messages"+query, testUserToken, nil); status != 200 || len(messageContents(body)) != 0 {
			t.Fatalf("after clear %q: %d %v", query, status, body)
		}
	}
}

type sseEvent struct {
	name string
	data map[string]any
}

// openStream 連上 SSE，回傳讀事件的 channel；ping 註解行不會送出。
func openStream(t *testing.T, server *httptest.Server, token string) (<-chan sseEvent, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/messages/stream", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		cancel()
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := make(chan sseEvent, 16)
	go func() {
		defer resp.Body.Close()
		defer close(events)
		scanner := bufio.NewScanner(resp.Body)
		var current sseEvent
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				current.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &current.data)
			case line == "" && current.name != "":
				events <- current
				current = sseEvent{}
			}
		}
	}()
	return events, cancel
}

func nextEvent(t *testing.T, events <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(2 * time.Second):
		t.Fatal("沒有收到推播事件")
	}
	return sseEvent{}
}

func TestMessageStreamEvents(t *testing.T) {
	engine, _ := newTestServer(t)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	_, token := createAccountFor(t, engine, "BTC-Agent-01", 100)

	resp, err := http.DefaultClient.Do(func() *http.Request {
		req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/messages/stream", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return req
	}())
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("account token stream: %v %v", err, resp)
	}
	resp.Body.Close()

	events, cancel := openStream(t, server, testUserToken)
	defer cancel()

	_, posted := do(t, engine, http.MethodPost, "/v1/messages", token, gin.H{"content": "hi", "tags": []string{"USER"}})
	created := nextEvent(t, events)
	_, listed := do(t, engine, http.MethodGet, "/v1/messages", testUserToken, nil)
	first := listed["messages"].([]any)[0].(map[string]any)
	if created.name != "created" || fmt.Sprint(created.data) != fmt.Sprint(first) || created.data["id"] != posted["id"] {
		t.Fatalf("created 應與列表單筆相同: %v vs %v", created, first)
	}

	_, mine := do(t, engine, http.MethodPost, "/v1/messages", testUserToken, gin.H{"content": "mine"})
	if event := nextEvent(t, events); event.name != "created" || event.data["user_name"] != "you" {
		t.Fatalf("USER 自己的訊息應顯示 you: %v", event)
	}

	do(t, engine, http.MethodDelete, "/v1/messages/"+mine["id"].(string), testUserToken, nil)
	if event := nextEvent(t, events); event.name != "deleted" || fmt.Sprint(event.data) != fmt.Sprintf("map[id:%s]", mine["id"]) {
		t.Fatalf("deleted: %v", event)
	}

	do(t, engine, http.MethodDelete, "/v1/messages", testUserToken, nil)
	if event := nextEvent(t, events); event.name != "cleared" || len(event.data) != 0 {
		t.Fatalf("cleared: %v", event)
	}

	select {
	case event := <-events:
		t.Fatalf("每個動作只應推一次，多了 %v", event)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMessageStreamReleasesDisconnectedClients(t *testing.T) {
	engine, _ := newTestServer(t)
	server := httptest.NewServer(engine)
	t.Cleanup(server.Close)
	http.DefaultClient.CloseIdleConnections()
	baseline := runtime.NumGoroutine()

	cancels := []context.CancelFunc{}
	for i := 0; i < 5; i++ {
		_, cancel := openStream(t, server, testUserToken)
		cancels = append(cancels, cancel)
	}
	if runtime.NumGoroutine() <= baseline {
		t.Fatal("連線中應有額外的 goroutine")
	}
	for _, cancel := range cancels {
		cancel()
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("斷線後 goroutine 沒有回收：baseline %d，現在 %d", baseline, runtime.NumGoroutine())
		}
		http.DefaultClient.CloseIdleConnections()
		time.Sleep(20 * time.Millisecond)
	}
	if status, body := do(t, engine, http.MethodPost, "/v1/messages", testUserToken, gin.H{"content": "沒人聽也不會卡住"}); status != 201 {
		t.Fatalf("post: %d %v", status, body)
	}
}
