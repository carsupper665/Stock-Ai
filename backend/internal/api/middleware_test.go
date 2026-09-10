package api

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestAccountEndpointsRequireUserToken 是這一步最重要的檢查：
// 帳號管理端點對三種呼叫者的行為必須明確區分。
func TestAccountEndpointsRequireUserToken(t *testing.T) {
	engine, _ := newTestServer(t)
	id, accountToken := createAccountFor(t, engine, "BTC-Agent-01", 1000)

	endpoints := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/v1/accounts", gin.H{"user_name": "x", "initial_balance": 1}},
		{http.MethodGet, "/v1/accounts", nil},
		{http.MethodGet, "/v1/accounts/" + id, nil},
		{http.MethodPatch, "/v1/accounts/" + id, gin.H{"status": "disabled"}},
		{http.MethodDelete, "/v1/accounts/" + id, nil},
		{http.MethodPost, "/v1/accounts/" + id + "/token/reset", nil},
	}

	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			// 沒帶 token：401
			if status, _ := do(t, engine, ep.method, ep.path, "", ep.body); status != http.StatusUnauthorized {
				t.Fatalf("未帶 token 應回 401，得到 %d", status)
			}
			// 帶錯 token：401
			if status, _ := do(t, engine, ep.method, ep.path, "at_bogus", ep.body); status != http.StatusUnauthorized {
				t.Fatalf("無效 token 應回 401，得到 %d", status)
			}
			// 帶合法的 Account token：403（身分有效但權限不足）
			if status, _ := do(t, engine, ep.method, ep.path, accountToken, ep.body); status != http.StatusForbidden {
				t.Fatalf("Account token 應回 403，得到 %d", status)
			}
		})
	}
}

func TestSelfAccountHidesTokenAndRejectsUser(t *testing.T) {
	engine, _ := newTestServer(t)
	id, accountToken := createAccountFor(t, engine, "BTC-Agent-01", 1000)

	status, body := do(t, engine, http.MethodGet, "/v1/account", accountToken, nil)
	if status != http.StatusOK {
		t.Fatalf("帳號查自己應回 200，得到 %d: %v", status, body)
	}
	if body["id"] != id {
		t.Fatalf("回傳了別的帳號: %v", body)
	}
	// 規格 §1：Account Token 只有 USER 可以查看。
	if _, present := body["token"]; present {
		t.Fatalf("帳號自查不該回傳 token: %v", body)
	}

	// USER 沒有「自己的帳號」，這個端點對 USER 沒有意義。
	if status, _ := do(t, engine, http.MethodGet, "/v1/account", testUserToken, nil); status != http.StatusForbidden {
		t.Fatalf("USER 查 /v1/account 應回 403，得到 %d", status)
	}
	if status, _ := do(t, engine, http.MethodGet, "/v1/account", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("未帶 token 應回 401，得到 %d", status)
	}
}

func TestDisabledAccountTokenStopsWorking(t *testing.T) {
	engine, _ := newTestServer(t)
	id, accountToken := createAccountFor(t, engine, "BTC-Agent-01", 1000)

	if status, _ := do(t, engine, http.MethodGet, "/v1/account", accountToken, nil); status != http.StatusOK {
		t.Fatalf("停用前應可使用，得到 %d", status)
	}

	if status, body := do(t, engine, http.MethodPatch, "/v1/accounts/"+id, testUserToken, gin.H{"status": "disabled"}); status != http.StatusOK {
		t.Fatalf("停用帳號失敗: %d %v", status, body)
	}

	if status, _ := do(t, engine, http.MethodGet, "/v1/account", accountToken, nil); status != http.StatusUnauthorized {
		t.Fatalf("停用後 token 應失效並回 401，得到 %d", status)
	}
}
