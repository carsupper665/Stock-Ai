package api

import (
	"errors"
	"net/http"
	"time"

	"backend/internal/account"
	"backend/internal/database"

	"github.com/gin-gonic/gin"
)

// accountView 是帳號的對外表示。
// Token 只在 USER 查詢時填入：規格 §1 規定 Account Token 只有 USER 可以查看。
type accountView struct {
	ID             string    `json:"id"`
	UserName       string    `json:"user_name"`
	InitialBalance float64   `json:"initial_balance"`
	Balance        float64   `json:"balance"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	Token          string    `json:"token,omitempty"`
}

func viewAccount(a *database.Account, includeToken bool) accountView {
	v := accountView{
		ID:             a.ID,
		UserName:       a.UserName,
		InitialBalance: a.InitialBalance,
		Balance:        a.Balance,
		Status:         a.Status,
		CreatedAt:      a.CreatedAt.UTC(),
	}
	if includeToken {
		v.Token = a.Token
	}
	return v
}

type createAccountRequest struct {
	UserName       string  `json:"user_name"`
	InitialBalance float64 `json:"initial_balance"`
}

func (s *Server) createAccount(c *gin.Context) {
	var req createAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", "請求內容不是合法的 JSON")
		return
	}

	acc, err := s.accounts.Create(c.Request.Context(), account.CreateInput{
		UserName:       req.UserName,
		InitialBalance: req.InitialBalance,
	})
	if err != nil {
		s.writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusCreated, viewAccount(acc, true))
}

func (s *Server) listAccounts(c *gin.Context) {
	accounts, err := s.accounts.List(c.Request.Context())
	if err != nil {
		s.writeAccountError(c, err)
		return
	}

	views := make([]accountView, 0, len(accounts))
	for i := range accounts {
		views = append(views, viewAccount(&accounts[i], true))
	}
	c.JSON(http.StatusOK, gin.H{"accounts": views})
}

func (s *Server) getAccount(c *gin.Context) {
	acc, err := s.accounts.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		s.writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, viewAccount(acc, true))
}

type updateAccountRequest struct {
	UserName *string `json:"user_name"`
	Status   *string `json:"status"`
}

func (s *Server) updateAccount(c *gin.Context) {
	var req updateAccountRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid_request", "請求內容不是合法的 JSON")
		return
	}
	if req.UserName == nil && req.Status == nil {
		fail(c, http.StatusBadRequest, "invalid_request", "沒有任何要修改的欄位")
		return
	}

	acc, err := s.accounts.Update(c.Request.Context(), c.Param("id"), account.UpdateInput{
		UserName: req.UserName,
		Status:   req.Status,
	})
	if err != nil {
		s.writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, viewAccount(acc, true))
}

func (s *Server) deleteAccount(c *gin.Context) {
	if err := s.accounts.Delete(c.Request.Context(), c.Param("id")); err != nil {
		s.writeAccountError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) resetAccountToken(c *gin.Context) {
	acc, err := s.accounts.ResetToken(c.Request.Context(), c.Param("id"))
	if err != nil {
		s.writeAccountError(c, err)
		return
	}
	c.JSON(http.StatusOK, viewAccount(acc, true))
}

// selfAccount 是 Account Token 的帳務總覽。
// 不回傳 token：規格 §1 規定只有 USER 可以查看 Account Token。
func (s *Server) selfAccount(c *gin.Context) {
	accountID, ok := s.ownAccount(c)
	if !ok {
		return
	}

	acc, err := s.accounts.Get(c.Request.Context(), accountID)
	if err != nil {
		s.writeAccountError(c, err)
		return
	}
	summary, err := s.trading.Summary(c.Request.Context(), accountID)
	if err != nil {
		s.writeTradingError(c, err)
		return
	}

	c.JSON(http.StatusOK, selfAccountView{
		accountView:  viewAccount(acc, false),
		LockedMargin: round(summary.LockedMargin),
		Available:    round(summary.Available),
		Unrealized:   round(summary.Unrealized),
		Equity:       round(summary.Equity),
	})
}

// selfAccountView 在帳號基本資料上補齊保證金與權益。
type selfAccountView struct {
	accountView
	LockedMargin float64 `json:"locked_margin"`
	Available    float64 `json:"available"`
	Unrealized   float64 `json:"unrealized_pnl"`
	Equity       float64 `json:"equity"`
}

// writeAccountError 把 service 與 store 的錯誤對應到 HTTP 回應。
func (s *Server) writeAccountError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, database.ErrNotFound):
		fail(c, http.StatusNotFound, "not_found", "帳號不存在")
	case errors.Is(err, account.ErrNameTaken):
		fail(c, http.StatusConflict, "name_taken", err.Error())
	case errors.Is(err, account.ErrInvalidName),
		errors.Is(err, account.ErrInvalidBalance),
		errors.Is(err, account.ErrInvalidStatus):
		fail(c, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		s.log.Errorf(c.Request.Context(), "帳號操作失敗: %v", err)
		fail(c, http.StatusInternalServerError, "internal_error", "帳號操作失敗")
	}
}
