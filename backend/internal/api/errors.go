package api

import "github.com/gin-gonic/gin"

// errorBody 是所有錯誤回應的固定格式。
// error 是給程式判斷的代碼，message 是給人看的說明。
type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// fail 中止請求並回傳錯誤。
func fail(c *gin.Context, status int, code, message string) {
	c.AbortWithStatusJSON(status, errorBody{Error: code, Message: message})
}
