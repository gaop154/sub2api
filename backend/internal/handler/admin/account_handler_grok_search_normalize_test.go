package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestAccountCreateGrokSearchNormalizesSSOToken 验证通用创建接口对 grok_search 的
// SSO token 归一化：用户粘贴 "sso=abc; path=/; HttpOnly" 形式的 cookie 整串，
// 落库前应剥离成纯 token "abc"，避免 forwarder 拼 Cookie: sso=<token> 时出现双 "sso="。
func TestAccountCreateGrokSearchNormalizesSSOToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	router := gin.New()
	router.POST("/api/v1/admin/accounts", handler.Create)

	body := `{"name":"gs","platform":"grok_search","type":"apikey","credentials":{"sso_token":"sso=abc; path=/; HttpOnly","base_url":"https://console.x.ai"}}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, adminSvc.createdAccounts, 1)
	ssoToken, _ := adminSvc.createdAccounts[0].Credentials["sso_token"].(string)
	require.Equal(t, "abc", ssoToken)
}

// TestAccountCreateGrokSearchKeepsPlainSSOToken 验证纯 token（无 sso= 前缀）原样保留。
func TestAccountCreateGrokSearchKeepsPlainSSOToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminSvc := newStubAdminService()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	router := gin.New()
	router.POST("/api/v1/admin/accounts", handler.Create)

	body := `{"name":"gs","platform":"grok_search","type":"apikey","credentials":{"sso_token":"plain-token-xyz"}}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Len(t, adminSvc.createdAccounts, 1)
	ssoToken, _ := adminSvc.createdAccounts[0].Credentials["sso_token"].(string)
	require.Equal(t, "plain-token-xyz", ssoToken)
}
