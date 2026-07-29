package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// grokSearchChatBridgeTestAccount 构造一个启用 chat completions 桥接的 grok_search 账号。
func grokSearchChatBridgeTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Name:        "grok-search-sso-bridge",
		Platform:    PlatformGrokSearch,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"sso_token": "sso-cookie-value",
			"base_url":  "https://console.x.ai",
		},
		Extra: map[string]any{
			GrokSearchChatCompletionsExtraKey: true,
		},
	}
}

// grokSearchChatBridgeCompletedResponse 模拟 console.x.ai /v1/responses 的 SSE 完成响应
// （responses 格式，与 grokChatBridgeCompletedResponse 同源协议）。
func grokSearchChatBridgeCompletedResponse(responseID string) *http.Response {
	body := strings.Join([]string{
		`data: {"type":"response.output_text.delta","sequence_number":0,"delta":"console ok"}`,
		"",
		`data: {"type":"response.completed","sequence_number":1,"response":{"id":"` + responseID + `","object":"response","model":"grok-4.20-multi-agent-0309","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"console ok"}]}],"usage":{"input_tokens":10,"output_tokens":3,"total_tokens":13}}}`,
		"",
	}, "\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{responseID + "-request"},
		},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

// TestForwardGrokSearchChatCompletions_DisabledRejects400 验证：账号开关关闭时，
// ForwardAsChatCompletions 的 grok_search 分支返回 400 错误且不调用上游。
// 回归点：改前 grok_search /v1/chat/completions 在路由层硬拒；现在拒绝逻辑下沉到本分支，
// 关闭时必须仍拒绝（错误信息含 "disabled"）。
func TestForwardGrokSearchChatCompletions_DisabledRejects400(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok-4.20-multi-agent-0309","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grokChatRawEndpoint, bytes.NewReader(body))

	account := &Account{
		ID:          81,
		Name:        "grok-search-no-bridge",
		Platform:    PlatformGrokSearch,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"sso_token": "sso-cookie-value",
			"base_url":  "https://console.x.ai",
		},
		// 不设 Extra 开关 → 默认关闭
	}
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.Error(t, err)
	require.Nil(t, result)
	// 不调用上游
	require.Nil(t, upstream.lastReq)
	// 400 拒绝 + 错误信息含 disabled
	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "disabled")
	require.Contains(t, recorder.Body.String(), GrokSearchChatCompletionsExtraKey)
}

// TestForwardGrokSearchChatCompletions_EnabledBridgesViaConsoleResponses 验证：账号开关开启时，
// ForwardAsChatCompletions 走 forwardGrokSearchChatCompletionsViaResponses 桥：
//   - 上游 URL 为 console.x.ai/v1/responses（非 cli-chat-proxy）；
//   - 认证为 SSO cookie（sso=...; sso-rw=...）+ Bearer anonymous，非 OAuth Bearer；
//   - 用 Chrome TLS 指纹（DoWithTLS）；
//   - body 经 console 契约归一（store=false、tools 含 web_search/x_search、input_text）；
//   - 响应转为 chat completions 格式（choices.0.message.content）。
func TestForwardGrokSearchChatCompletions_EnabledBridgesViaConsoleResponses(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 用结构化 content（text part）触发 patchGrokSearchInput 的 input_text 归一路径；
	// 纯字符串 content 会被 ChatCompletionsToResponses 原样保留为字符串（console 也接受），
	// 此处专门验证结构化输入的归一。
	body := []byte(`{"model":"grok-4.20-multi-agent-0309","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 8201})

	account := grokSearchChatBridgeTestAccount(82)
	upstream := &httpUpstreamRecorder{resp: grokSearchChatBridgeCompletedResponse("resp_grok_search_chat")}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)

	// 上游 URL：console.x.ai/v1/responses
	require.Equal(t, "https://console.x.ai/v1/responses", upstream.lastReq.URL.String())
	// 认证：SSO cookie + anonymous（非 OAuth Bearer access_token）
	require.Equal(t, "sso=sso-cookie-value; sso-rw=sso-cookie-value", upstream.lastReq.Header.Get("Cookie"))
	require.Equal(t, "Bearer anonymous", upstream.lastReq.Header.Get("Authorization"))
	// 上游 endpoint 标记为 console responses 路径
	require.Equal(t, grokSearchResponsesPath, result.UpstreamEndpoint)
	require.Equal(t, grokSearchResponsesPath, GetActualOpenAIUpstreamEndpoint(c))

	// console 契约：store=false、tools 含 web_search/x_search、input 已 patch 为 input_text
	require.False(t, gjson.GetBytes(upstream.lastBody, "store").Bool())
	require.True(t, gjson.GetBytes(upstream.lastBody, "stream").Bool())
	require.Equal(t, "input_text", gjson.GetBytes(upstream.lastBody, "input.0.content.0.type").String())
	toolTypes := gjson.GetBytes(upstream.lastBody, "tools.#.type").Array()
	var typeStrs []string
	for _, r := range toolTypes {
		typeStrs = append(typeStrs, r.String())
	}
	require.Contains(t, typeStrs, "web_search")
	require.Contains(t, typeStrs, "x_search")

	// 响应转为 chat completions 格式
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "console ok", gjson.Get(recorder.Body.String(), "choices.0.message.content").String())
	// 用量从 responses usage 透传到 chat completions usage
	require.Equal(t, int64(10), gjson.Get(recorder.Body.String(), "usage.prompt_tokens").Int())
	require.Equal(t, int64(3), gjson.Get(recorder.Body.String(), "usage.completion_tokens").Int())
}

// TestForwardGrokSearchChatCompletions_EnabledStreamingPropagatesChat 验证流式分支：
// 客户端 stream=true 时走 handleChatStreamingResponse，输出 chat completions SSE。
func TestForwardGrokSearchChatCompletions_EnabledStreamingPropagatesChat(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok-4.20-multi-agent-0309","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 8301})

	account := grokSearchChatBridgeTestAccount(83)
	upstream := &httpUpstreamRecorder{resp: grokSearchChatBridgeCompletedResponse("resp_grok_search_chat_stream")}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, grokSearchResponsesPath, result.UpstreamEndpoint)
	require.Equal(t, "https://console.x.ai/v1/responses", upstream.lastReq.URL.String())
	// chat completions SSE 格式
	require.Contains(t, recorder.Header().Get("Content-Type"), "text/event-stream")
	require.Contains(t, recorder.Body.String(), `"content":"console ok"`)
	require.Contains(t, recorder.Body.String(), "data: [DONE]")
}

// TestForwardGrokSearchChatCompletions_MissingSSOTokenReturnsError 验证开启桥接但缺 sso_token 时报错。
func TestForwardGrokSearchChatCompletions_MissingSSOTokenReturnsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok-4.20-multi-agent-0309","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grokChatRawEndpoint, bytes.NewReader(body))

	account := &Account{
		ID:          84,
		Name:        "grok-search-no-sso-bridge",
		Platform:    PlatformGrokSearch,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"base_url": "https://console.x.ai",
		},
		Extra: map[string]any{
			GrokSearchChatCompletionsExtraKey: true,
		},
	}
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.Error(t, err)
	require.Nil(t, result)
	require.Nil(t, upstream.lastReq)
	require.Contains(t, err.Error(), "sso_token")
}

// TestForwardGrokSearchChatCompletions_UsesChromeTLSProfile 验证桥接走 DoWithTLS 且 profile 非空。
// 通过自定义 recorder 捕获传入的 TLS profile。
func TestForwardGrokSearchChatCompletions_UsesChromeTLSProfile(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"grok-4.20-multi-agent-0309","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, grokChatRawEndpoint, bytes.NewReader(body))
	c.Set("api_key", &APIKey{ID: 8401})

	account := grokSearchChatBridgeTestAccount(84)
	upstream := &grokSearchChatTLSCapturingRecorder{
		httpUpstreamRecorder: &httpUpstreamRecorder{resp: grokSearchChatBridgeCompletedResponse("resp_grok_search_tls")},
	}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.capturedProfile)
	require.Equal(t, "grok-search-chrome", upstream.capturedProfile.Name)
}

// grokSearchChatTLSCapturingRecorder 包装 httpUpstreamRecorder 并捕获 DoWithTLS 的 profile，
// 用于验证 grok_search 桥走 Chrome TLS 指纹（而非裸 Do）。
type grokSearchChatTLSCapturingRecorder struct {
	*httpUpstreamRecorder
	capturedProfile *tlsfingerprint.Profile
}

func (u *grokSearchChatTLSCapturingRecorder) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, profile *tlsfingerprint.Profile) (*http.Response, error) {
	u.capturedProfile = profile
	return u.httpUpstreamRecorder.DoWithTLS(req, proxyURL, accountID, accountConcurrency, profile)
}
