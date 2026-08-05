//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		accountRepo:    repo,
		httpUpstream:   upstream,
		grokSearchDPoP: newGrokSearchDPoPSessionManager(),
	}
	// 预填充 DPoP session，跳过真实 token 交换（含 EC 密钥绑定校验，测试无法预知 cnf.jkt）
	storeGrokSearchDPoPSessionForTest(t, svc.grokSearchDPoP, account, "sso-cookie-value")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/21/test", nil)

	err := svc.TestAccountConnection(c, account.ID, "grok-4.20-multi-agent-0309", "", AccountTestModeDefault)
	require.NoError(t, err)

	// 上游 URL：自拼 console.x.ai/v1/responses
	require.Equal(t, "https://console.x.ai/v1/responses", upstream.lastReq.URL.String())

	// 认证：SSO cookie + DPoP scheme，不是 Bearer access_token
	require.Equal(t, "sso=sso-cookie-value; sso-rw=sso-cookie-value", upstream.lastReq.Header.Get("Cookie"))
	require.Equal(t, "DPoP mock-dpop-access-token", upstream.lastReq.Header.Get("Authorization"))
	require.NotEmpty(t, upstream.lastReq.Header.Get("DPoP"))

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

	// 成功路径不触发账号状态变更：返回 test_complete
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

// TestAccountTestService_GrokSearchUpstreamError 验证 401 → 持久 SetError（SSO 失效，需重导 SSO），
// 语义对齐转发链路 markGrokSearchReauthRequired；同时反馈前端（错误事件含 401）。
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
	repo := &grokSearchRecordingRepo{
		mockAccountRepoForGemini: &mockAccountRepoForGemini{
			accountsByID: map[int64]*Account{account.ID: account},
		},
	}
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"sso expired"}}`)),
	}}
	svc := &AccountTestService{
		accountRepo:    repo,
		httpUpstream:   upstream,
		grokSearchDPoP: newGrokSearchDPoPSessionManager(),
	}
	// 预填充 DPoP session：业务请求 401 → 重试触发 token 交换（mock 同样 401）→ 最终返回 401
	storeGrokSearchDPoPSessionForTest(t, svc.grokSearchDPoP, account, "sso-cookie-value")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/23/test", nil)

	err := svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault)
	require.Error(t, err)
	// 401 → 持久 SetError（SSO 失效），不走临时下线
	require.Len(t, repo.setErrorCalls, 1)
	require.Equal(t, account.ID, repo.setErrorCalls[0].ID)
	require.Contains(t, repo.setErrorCalls[0].Msg, "unauthorized")
	require.Empty(t, repo.setTempUnschedulableCalls)
	// 反馈给前端：错误事件含 401
	require.Contains(t, rec.Body.String(), "401")
}

// TestAccountTestService_GrokSearchErrorStateMapping 验证错误状态码 → 账号 DB 状态映射，
// 语义对齐转发链路 handleGrokSearchAccountUpstreamError（仅 DB 持久化，无内存调度）。
func TestAccountTestService_GrokSearchErrorStateMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name            string
		statusCode      int
		headers         http.Header
		body            string
		poolMode        bool
		wantSetError    bool
		wantErrorMatch  string
		wantTemp        bool
		wantTempAtLeast time.Duration // 临时下线 until 距 now 的最小阈值，区分 2min / 5min / 24h
	}{
		{
			name:           "401 SSO 失效 → SetError",
			statusCode:     http.StatusUnauthorized,
			body:           `{"error":"unauthorized"}`,
			wantSetError:   true,
			wantErrorMatch: "unauthorized",
		},
		{
			name:       "403 CF 挑战 → 不惩罚账号",
			statusCode: http.StatusForbidden,
			headers:    http.Header{"Cf-Mitigated": []string{"challenge"}},
			body:       `<html><title>Just a moment</title></html>`,
		},
		{
			name:       "403 dpop-required → 不惩罚账号（协议异常，SSO 仍有效）",
			statusCode: http.StatusForbidden,
			body:       `{"code":"unauthorized:dpop-required","error":"DPoP proof required but was not verified."}`,
		},
		{
			name:           "403 permission-denied → SetError",
			statusCode:     http.StatusForbidden,
			body:           `{"code":"permission-denied","error":"access to the chat endpoint is denied"}`,
			wantSetError:   true,
			wantErrorMatch: "permission denied",
		},
		{
			name:            "429 普通频率限制 → 短冷却 5min",
			statusCode:      http.StatusTooManyRequests,
			body:            `{"code":"resource-exhausted","error":"Too many requests for team"}`,
			wantTemp:        true,
			wantTempAtLeast: 4 * time.Minute,
		},
		{
			name:            "429 免费额度耗尽 → 长冷却 24h",
			statusCode:      http.StatusTooManyRequests,
			body:            `{"code":"resource-exhausted","error":"Free usage quota exceeded. Purchase credits"}`,
			wantTemp:        true,
			wantTempAtLeast: 23 * time.Hour,
		},
		{
			name:            "5xx 非池模式 → 短冷却 2min",
			statusCode:      http.StatusBadGateway,
			body:            `{"error":"bad gateway"}`,
			wantTemp:        true,
			wantTempAtLeast: 90 * time.Second,
		},
		{
			name:       "5xx 池模式 → 不惩罚",
			statusCode: http.StatusBadGateway,
			body:       `{"error":"bad gateway"}`,
			poolMode:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				ID:          2400,
				Name:        "grok-search-err-map",
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
			if tt.poolMode {
				account.Credentials["pool_mode"] = true
			}
			repo := &grokSearchRecordingRepo{
				mockAccountRepoForGemini: &mockAccountRepoForGemini{
					accountsByID: map[int64]*Account{account.ID: account},
				},
			}
			header := tt.headers
			if header == nil {
				header = http.Header{"Content-Type": []string{"application/json"}}
			}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: tt.statusCode,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}}
			svc := &AccountTestService{
				accountRepo:    repo,
				httpUpstream:   upstream,
				grokSearchDPoP: newGrokSearchDPoPSessionManager(),
			}
			storeGrokSearchDPoPSessionForTest(t, svc.grokSearchDPoP, account, "sso-cookie-value")

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/2400/test", nil)

			_ = svc.TestAccountConnection(c, account.ID, "", "", AccountTestModeDefault)

			if tt.wantSetError {
				require.Len(t, repo.setErrorCalls, 1, "应调用 SetError")
				require.Equal(t, account.ID, repo.setErrorCalls[0].ID)
				require.Contains(t, repo.setErrorCalls[0].Msg, tt.wantErrorMatch)
			} else {
				require.Empty(t, repo.setErrorCalls, "不应调用 SetError")
			}
			if tt.wantTemp {
				require.Len(t, repo.setTempUnschedulableCalls, 1, "应调用 SetTempUnschedulable")
				call := repo.setTempUnschedulableCalls[0]
				require.Equal(t, account.ID, call.ID)
				require.True(t, call.Until.After(time.Now().Add(tt.wantTempAtLeast)),
					"冷却 until=%v 应至少晚于 now+%v", call.Until, tt.wantTempAtLeast)
			} else {
				require.Empty(t, repo.setTempUnschedulableCalls, "不应调用 SetTempUnschedulable")
			}
		})
	}
}

// grokSearchRecordingRepo 嵌入 mockAccountRepoForGemini，重写 SetError / SetTempUnschedulable
// 以记录调用，用于断言测试连接对错误状态码的账号状态变更（其余方法委托给嵌入的 mock）。
type grokSearchRecordingRepo struct {
	*mockAccountRepoForGemini
	setErrorCalls             []grokSearchRepoSetErrorCall
	setTempUnschedulableCalls []grokSearchRepoSetTempCall
}

type grokSearchRepoSetErrorCall struct {
	ID  int64
	Msg string
}

type grokSearchRepoSetTempCall struct {
	ID     int64
	Until  time.Time
	Reason string
}

func (r *grokSearchRecordingRepo) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.setErrorCalls = append(r.setErrorCalls, grokSearchRepoSetErrorCall{ID: id, Msg: errorMsg})
	return nil
}

func (r *grokSearchRecordingRepo) SetTempUnschedulable(ctx context.Context, id int64, until time.Time, reason string) error {
	r.setTempUnschedulableCalls = append(r.setTempUnschedulableCalls, grokSearchRepoSetTempCall{ID: id, Until: until, Reason: reason})
	return nil
}
