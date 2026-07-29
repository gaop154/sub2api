//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestAccountTestService_TestAccountConnection_GrokSearchUsesConsoleResponses 验证
// grok_search 账号走 testGrokSearchAccountConnection（不 fallthrough 到 claude）：
//   - 上游 URL 为 console.x.ai/v1/responses
//   - 认证头为 SSO cookie（sso=...; sso-rw=...），不是 Bearer access_token
//   - 用 Chrome uTLS 指纹（DoWithTLS）
//   - 请求体经 console 契约归一（store:false、含 web_search/x_search、input_text）
func TestAccountTestService_TestAccountConnection_GrokSearchUsesConsoleResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	account := &Account{
		ID:          21,
		Name:        "grok-search-sso",
		Platform:    PlatformGrokSearch,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"sso_token": "sso-cookie-value",
			"base_url":  "https://console.x.ai",
		},
	}
	repo := &mockAccountRepoForGemini{
		accountsByID: map[int64]*Account{account.ID: account},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"output":[{"content":[{"type":"output_text","text":"hello from console"}]}]}`,
		)),
	}}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/21/test", nil)

	err := svc.TestAccountConnection(c, account.ID, "grok-4.20-multi-agent-0309", "", AccountTestModeDefault)
	require.NoError(t, err)

	// 上游 URL：自拼 console.x.ai/v1/responses
	require.Equal(t, "https://console.x.ai/v1/responses", upstream.lastReq.URL.String())

	// 认证：SSO cookie，不是 Bearer access_token
	require.Equal(t, "sso=sso-cookie-value; sso-rw=sso-cookie-value", upstream.lastReq.Header.Get("Cookie"))
	require.Equal(t, "Bearer anonymous", upstream.lastReq.Header.Get("Authorization"))

	// console 契约：store=false、tools 含 web_search/x_search、input 已 patch 为 input_text
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.Equal(t, "input_text", gjson.GetBytes(upstream.lastBody, "input.0.content.0.type").String())
	toolTypes := gjson.GetBytes(upstream.lastBody, "tools.#.type").Array()
	var typeStrs []string
	for _, r := range toolTypes {
		typeStrs = append(typeStrs, r.String())
	}
	require.Contains(t, typeStrs, "web_search")
	require.Contains(t, typeStrs, "x_search")

	// 测试不改账号状态：repo 不应被 SetError/SetTempUnschedulable（mock 为 no-op，此处仅校验返回成功）
	require.NotContains(t, rec.Body.String(), "claude")
	require.Contains(t, rec.Body.String(), "hello from console")
	require.Contains(t, rec.Body.String(), `"type":"test_complete"`)
}

// TestAccountTestService_GrokSearchMissingSSOToken 报错退出。
func TestAccountTestService_GrokSearchMissingSSOToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	account := &Account{
		ID:          22,
		Name:        "grok-search-no-sso",
		Platform:    PlatformGrokSearch,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"base_url": "https://console.x.ai",
		},
	}
	repo := &mockAccountRepoForGemini{
		accountsByID: map[int64]*Account{account.ID: account},
	}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: &httpUpstreamRecorder{},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/22/test", nil)

	err := svc.TestAccountConnection(c, account.ID, "grok-4.20-multi-agent-0309", "", AccountTestModeDefault)
	require.Error(t, err)
	require.Contains(t, rec.Body.String(), "sso_token")
}

// TestAccountTestService_GrokSearchUpstreamError 仅反馈前端、不改账号状态。
func TestAccountTestService_GrokSearchUpstreamError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	account := &Account{
		ID:          23,
		Name:        "grok-search-401",
		Platform:    PlatformGrokSearch,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"sso_token": "sso-cookie-value",
		},
	}
	repo := &mockAccountRepoForGemini{
		accountsByID: map[int64]*Account{account.ID: account},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"sso expired"}}`)),
	}}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/23/test", nil)

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault)
	require.Error(t, err)
	// 测试场景不改账号状态：账号仍 active/schedulable
	require.True(t, account.Schedulable)
	require.Equal(t, StatusActive, account.Status)
	// 反馈给前端：错误事件含 401
	require.Contains(t, rec.Body.String(), "401")
}
