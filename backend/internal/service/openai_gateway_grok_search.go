package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
)

// grok_search 平台走 SSO cookie + Grok Console 通道（console.x.ai/v1/responses），
// 与现有 grok 平台（OIDC access_token + cli-chat-proxy.grok.com）物理隔离。
// 目的：让 grok-4.20-multi-agent 搜索按 Console 网页订阅配额计费，绕开
// Responses API 的 personal-team-blocked:spending-limit (402)。
//
// 隔离性保证：
//   - 独立 forwarder 文件 + 独立分发分支（account.Platform == PlatformGrokSearch）
//   - 不复用 grok 的 token provider / buildGrokResponsesURL / quota snapshot / 402 冷却
//   - 共享出口层 httpUpstream.Do，但 applyGrokCLIProxyHeaders 仅对 cli-chat-proxy.grok.com
//     生效，对 console.x.ai 直接 return（repository/http_upstream.go:440），无副作用。
//   - validateRequestHost 是 SSRF 防护（非 host 白名单），console.x.ai 公网域名可正常通过。
const (
	grokSearchDefaultBaseURL  = "https://console.x.ai"
	grokSearchResponsesPath   = "/v1/responses"
	grokSearchXCluster        = "https://us-east-1.api.x.ai"
	grokSearchOrigin          = "https://console.x.ai"
	grokSearchUserAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	grokSearchSecChUa         = `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`
	grokSearchSecChUaMobile   = "?0"
	grokSearchSecChUaPlatform = `"Windows"`
)

// grokSearchChromeProfile 返回 Chrome 风格的 TLS 指纹 Profile，用于绕过 console.x.ai 的 Cloudflare。
//
// 实证（cfprobe，直连）：裸 Go TLS → CF 403 block；本 Chrome uTLS 指纹 → CF 放行、应用层返回 401。
// 关键点：
//   - 不含 compress_certificate(27)：buildClientHelloSpecFromProfile 对未知扩展只发空 GenericExtension，
//     compress_certificate 空 payload 会触发 server "error decoding message"，故删除（JA3 略差但 CF 放行）。
//   - ALPN 仅 http/1.1：已实证 console 应用层接受；同时避免 Go net/http 的 HTTP/2 帧顺序被 CF 检测。
//   - GREASE 扩展 2 个（0x0a0a/0x2a2a，utlas 上限 2）。
//
// design §8 CF 策略：L0 实证失败，L1 实证成功，无需 L2（FlareSolverr）。
func grokSearchChromeProfile() *tlsfingerprint.Profile {
	return &tlsfingerprint.Profile{
		Name:         "grok-search-chrome",
		EnableGREASE: true,
		CipherSuites: []uint16{
			0x0a0a,                 // GREASE
			0x1301, 0x1302, 0x1303, // TLS 1.3
			0xc02b, 0xc02f, 0xc02c, 0xc030, // ECDHE AES-GCM
			0xcca9, 0xcca8, // ECDHE ChaCha20
			0xc009, 0xc013, 0xc00a, 0xc014, // ECDHE AES-CBC
			0x009c, 0x009d, 0x002f, 0x0035, // RSA
		},
		Curves:              []uint16{29, 23, 24}, // X25519, secp256r1, secp384r1
		PointFormats:        []uint16{0},
		SignatureAlgorithms: []uint16{0x0403, 0x0804, 0x0401, 0x0503, 0x0805, 0x0501, 0x0806, 0x0601, 0x0201},
		ALPNProtocols:       []string{"http/1.1"},
		SupportedVersions:   []uint16{0x0304, 0x0303}, // TLS 1.3, TLS 1.2
		KeyShareGroups:      []uint16{29},             // X25519
		PSKModes:            []uint16{1},
		Extensions: []uint16{
			0x0a0a, // GREASE
			0,      // server_name
			23,     // extended_master_secret
			0xff01, // renegotiation_info
			10,     // supported_groups
			11,     // ec_point_formats
			35,     // session_ticket
			16,     // alpn
			5,      // status_request
			13,     // signature_algorithms
			18,     // sct
			51,     // key_share
			45,     // psk_key_exchange_modes
			43,     // supported_versions
			0x2a2a, // GREASE
			21,     // padding
		},
	}
}

// forwardGrokSearch 用 SSO cookie 走 console.x.ai/v1/responses。
// 签名与 forwardGrokResponses 对齐，便于分发分支统一调用。
func (s *OpenAIGatewayService) forwardGrokSearch(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	originalModel string,
	reqStream bool,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	// 1. 自读 credentials：SSO token + base_url（不走 GetAccessToken / grokTokenProvider）
	ssoToken := strings.TrimSpace(account.GetCredential("sso_token"))
	if ssoToken == "" {
		return nil, fmt.Errorf("grok_search account missing sso_token credential")
	}
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	if baseURL == "" {
		baseURL = grokSearchDefaultBaseURL
	}
	upstreamURL := strings.TrimSuffix(baseURL, "/") + grokSearchResponsesPath

	// 2. 模型映射（multi-agent 靠 model 名进入对应模式）
	upstreamModel := account.GetMappedModel(originalModel)
	if strings.TrimSpace(upstreamModel) == "" {
		upstreamModel = originalModel
	}

	// 3. body 施加 console 契约
	patchedBody, err := normalizeGrokSearchRequestBody(body, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("normalize grok_search request body: %w", err)
	}

	// 4. 解耦客户端 context（客户端断开后上游仍可跑完用于计费）
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	defer releaseUpstreamCtx()

	req, err := buildGrokSearchRequest(upstreamCtx, patchedBody, ssoToken, upstreamURL)
	if err != nil {
		return nil, err
	}

	// 5. 代理
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// 6. 发送（CF 策略 L1：Chrome uTLS 指纹 + 一致 headers）
	// 实证（cfprobe）：裸 Go TLS 被 CF 403 拦截；Chrome 指纹绕过 CF、应用层返回 SSO 错误。
	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, grokSearchChromeProfile())
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	// 7. 错误分流（独立策略，不复用 grok 的 402 冷却 / quota snapshot）
	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		logger.LegacyPrintf("service.openai_gateway_grok_search",
			"grok_search upstream error account_id=%d status=%d body=%s",
			account.ID, resp.StatusCode, truncateString(string(respBody), 1024))
		s.handleGrokSearchAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		return s.handleErrorResponse(ctx, resp, c, account, patchedBody, upstreamModel)
	}

	// 8. 成功响应：复用 Responses SSE / 非流式处理（与 cli-chat-proxy 同源协议）
	var usage *OpenAIUsage
	var firstTokenMs *int
	responseID := ""
	if reqStream {
		streamResult, streamErr := s.handleStreamingResponse(ctx, resp, c, account, startTime, originalModel, upstreamModel)
		if streamErr != nil {
			return nil, streamErr
		}
		usage = streamResult.usage
		firstTokenMs = streamResult.firstTokenMs
		responseID = strings.TrimSpace(streamResult.responseID)
	} else {
		nonStreamResult, nonStreamErr := s.handleNonStreamingResponse(ctx, resp, c, account, originalModel, upstreamModel)
		if nonStreamErr != nil {
			return nil, nonStreamErr
		}
		usage = nonStreamResult.usage
		responseID = strings.TrimSpace(nonStreamResult.responseID)
	}
	if usage == nil {
		usage = &OpenAIUsage{}
	}

	reasoningEffort := extractOpenAIReasoningEffortFromBody(patchedBody, originalModel)
	return &OpenAIForwardResult{
		RequestID:       firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
		ResponseID:      responseID,
		Usage:           *usage,
		Model:           originalModel,
		UpstreamModel:   upstreamModel,
		ReasoningEffort: reasoningEffort,
		Stream:          reqStream,
		OpenAIWSMode:    false,
		ResponseHeaders: resp.Header.Clone(),
		Duration:        time.Since(startTime),
		FirstTokenMs:    firstTokenMs,
	}, nil
}

// buildGrokSearchRequest 构建 console.x.ai/v1/responses 请求。
// 认证完全由 SSO cookie 控制（sso 与 sso-rw 同值），不依赖 OIDC access_token。
// 不调 account.ApplyHeaderOverrides：cookie/authorization 在账号级覆写禁止列表内，
// grok_search 的身份由 SSO 唯一决定，避免外部配置污染。
func buildGrokSearchRequest(ctx context.Context, body []byte, ssoToken, upstreamURL string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create grok_search upstream request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	// SSO cookie：console 网页态认证（sso 与 sso-rw 同值）。PoC 暂不带 cf_clearance——
	// cfprobe 实证 Chrome uTLS 指纹单独即可过 CF；完整版可再叠加 cf_clearance（参照 grok2api BuildSSOCookie）。
	req.Header.Set("Cookie", "sso="+ssoToken+"; sso-rw="+ssoToken)
	// console 网页态占位认证头（真实身份在 cookie）
	req.Header.Set("Authorization", "Bearer anonymous")
	// 网页态来源
	req.Header.Set("Origin", grokSearchOrigin)
	req.Header.Set("Referer", grokSearchOrigin+"/")
	// 浏览器 fetch 语义头（参照 grok2api console/headers.go）
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Priority", "u=1, i")
	// xAI 集群路由
	req.Header.Set("x-cluster", grokSearchXCluster)
	// Chrome 指纹：UA 与 Sec-Ch-Ua 大版本必须一致，否则 CF 指纹矛盾（design §4）
	req.Header.Set("User-Agent", grokSearchUserAgent)
	req.Header.Set("Sec-Ch-Ua", grokSearchSecChUa)
	req.Header.Set("Sec-Ch-Ua-Mobile", grokSearchSecChUaMobile)
	req.Header.Set("Sec-Ch-Ua-Platform", grokSearchSecChUaPlatform)
	return req, nil
}

// grokSearchDefaultMaxOutputTokens 是 multi-agent 模型（grok-4.20-multi-agent-0309）的
// console 默认 max_output_tokens（参照 grok2api catalog.go ModelSpec.MaxOutputTokens）。
const grokSearchDefaultMaxOutputTokens = 2_000_000

// normalizeGrokSearchRequestBody 施加 console.x.ai/v1/responses 的请求体契约。
// 逻辑参照 grok2api（C:\idealProject\github\grok2api）console/normalize.go，精简为 grok_search 的
// multi-agent 搜索路径所需：
//   - model = upstreamModel；store=false（console 无状态）。
//   - 删 metadata/previous_response_id/service_tier/prompt_cache_key/background/conversation。
//   - patchInput：text/output_text → input_text；image_url → input_image（展平 url）。
//   - max_output_tokens 缺省补 multi-agent 默认。
//   - reasoning.effort 归一（minimal/max 等 → console 档位），缺省 medium。
//   - include 补 reasoning.encrypted_content（multi-agent 链路需要）。
//   - tools 归一 + 注入 web_search/x_search（multi-agent 搜索能力）。
//   - tool_choice 缺省 auto。
func normalizeGrokSearchRequestBody(body []byte, upstreamModel string) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse grok_search request: %w", err)
	}
	payload["model"] = upstreamModel
	payload["store"] = false
	for _, f := range []string{"metadata", "previous_response_id", "service_tier", "prompt_cache_key", "background", "conversation"} {
		delete(payload, f)
	}
	patchGrokSearchInput(payload)
	if _, exists := payload["max_output_tokens"]; !exists {
		payload["max_output_tokens"] = grokSearchDefaultMaxOutputTokens
	}
	normalizeGrokSearchReasoning(payload)
	ensureGrokSearchReasoningInclude(payload)
	normalizeGrokSearchTools(payload)
	if _, exists := payload["tool_choice"]; !exists {
		payload["tool_choice"] = "auto"
	}
	return json.Marshal(payload)
}

// patchGrokSearchInput 把客户端 Responses input 归一为 console 接受的形态。
func patchGrokSearchInput(payload map[string]any) {
	items, ok := payload["input"].([]any)
	if !ok {
		return
	}
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		content, ok := item["content"].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range content {
			part, ok := rawPart.(map[string]any)
			if !ok {
				continue
			}
			switch part["type"] {
			case "text", "output_text":
				part["type"] = "input_text"
			case "image_url":
				if image, ok := part["image_url"].(map[string]any); ok {
					if url, _ := image["url"].(string); strings.TrimSpace(url) != "" {
						part["type"] = "input_image"
						part["image_url"] = url
					}
				}
			}
		}
	}
}

// normalizeGrokSearchReasoning 归一 reasoning.effort，缺省 medium（multi-agent 默认档）。
func normalizeGrokSearchReasoning(payload map[string]any) {
	reasoning, _ := payload["reasoning"].(map[string]any)
	if reasoning == nil {
		reasoning = map[string]any{}
	}
	effort, _ := reasoning["effort"].(string)
	effort = normalizeGrokSearchEffort(effort)
	if effort == "" {
		effort = "medium"
	}
	reasoning["effort"] = effort
	payload["reasoning"] = reasoning
}

func normalizeGrokSearchEffort(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "none":
		return "none"
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "max":
		return "xhigh"
	default:
		return ""
	}
}

// ensureGrokSearchReasoningInclude 保证 include 含 reasoning.encrypted_content。
func ensureGrokSearchReasoningInclude(payload map[string]any) {
	value, _ := payload["include"].([]any)
	seen := make(map[string]struct{})
	result := make([]any, 0, len(value)+1)
	for _, item := range value {
		name, ok := item.(string)
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	if _, exists := seen["reasoning.encrypted_content"]; !exists {
		result = append(result, "reasoning.encrypted_content")
	}
	payload["include"] = result
}

// normalizeGrokSearchTools 归一 tools：web_search/x_search 补全子字段、function 保留白名单字段；
// 若缺 web_search/x_search 则注入（multi-agent 搜索能力，参照 grok2api mergeSearchTools）。
func normalizeGrokSearchTools(payload map[string]any) {
	result := make([]any, 0, 4)
	hasWebSearch, hasXSearch := false, false
	if tools, ok := payload["tools"].([]any); ok {
		for _, raw := range tools {
			tool, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			typeName, _ := tool["type"].(string)
			switch strings.ToLower(strings.TrimSpace(typeName)) {
			case "web_search", "web_search_preview", "web_search_preview_2025_03_11", "web_search_2025_08_26":
				clean := map[string]any{"type": "web_search", "enable_image_understanding": true}
				if v, ok := tool["enable_image_understanding"].(bool); ok {
					clean["enable_image_understanding"] = v
				}
				result = append(result, clean)
				hasWebSearch = true
			case "x_search":
				clean := map[string]any{"type": "x_search", "enable_video_understanding": true}
				if v, ok := tool["enable_video_understanding"].(bool); ok {
					clean["enable_video_understanding"] = v
				}
				result = append(result, clean)
				hasXSearch = true
			case "function":
				name := strings.TrimSpace(getStringFromMap(tool, "name"))
				if name == "" {
					continue
				}
				clean := map[string]any{"type": "function", "name": name}
				for _, f := range []string{"description", "parameters", "strict"} {
					if v, exists := tool[f]; exists {
						clean[f] = v
					}
				}
				result = append(result, clean)
			}
		}
	}
	if !hasWebSearch {
		result = append(result, map[string]any{"type": "web_search", "enable_image_understanding": true})
	}
	if !hasXSearch {
		result = append(result, map[string]any{"type": "x_search", "enable_video_understanding": true})
	}
	payload["tools"] = result
}

// getStringFromMap 安全读取 map 中的字符串字段。
func getStringFromMap(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// handleGrokSearchAccountUpstreamError 是 grok_search 独立的账号级错误处理，与 grok 平台
// （handleGrokAccountUpstreamError）物理隔离：不解析 grok OIDC 配额快照、不触发 402→账号冷却
// （grok_search 的目标正是绕开 402；若 console 仍返回 402，按未知错误透传，不冷却账号）。
//
// 策略（PoC，design §8/§9）：
//   - 401：SSO 失效，短时冷却并标记需重认证（无 refresh，管理员重导 SSO）。
//   - 429：限流冷却。
//   - 5xx：非池模式下短冷却。
//   - 403：疑似 CF 挑战 / egress 拦截，PoC 不惩罚账号（L1 实证后再定）。
//   - 其余：不冷却，仅由调用方透传错误。
func (s *OpenAIGatewayService) handleGrokSearchAccountUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, responseBody []byte) {
	if s == nil || account == nil {
		return
	}
	switch statusCode {
	case http.StatusUnauthorized:
		// SSO 无 refresh，失效只能重导：持久标记需重认证（status=error + schedulable=false），
		// 不用 temp 短时冷却（会 401 循环——10 分钟后恢复调度又用失效 SSO 打）。管理员重导 SSO 后账号恢复 active。
		s.markGrokSearchReauthRequired(ctx, account)
	case http.StatusTooManyRequests:
		s.tempUnscheduleGrokSearch(ctx, account, 5*time.Minute, "grok_search rate limited")
	default:
		if statusCode >= 500 && !account.IsPoolMode() {
			s.tempUnscheduleGrokSearch(ctx, account, 2*time.Minute, "grok_search upstream temporary error")
		}
	}
	_ = responseBody
	_ = headers
}

// tempUnscheduleGrokSearch 临时下线 grok_search 账号（结构同 tempUnscheduleGrok，独立 reason）。
func (s *OpenAIGatewayService) tempUnscheduleGrokSearch(ctx context.Context, account *Account, cooldown time.Duration, reason string) {
	if s == nil || account == nil {
		return
	}
	until := time.Now().Add(cooldown)
	if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(until) {
		until = *account.TempUnschedulableUntil
	}
	s.BlockAccountScheduling(account, until, reason)
	if s.accountRepo != nil {
		stateCtx, cancel := openAIAccountStateContext(ctx)
		defer cancel()
		_ = s.accountRepo.SetTempUnschedulable(stateCtx, account.ID, until, reason)
	}
}

// markGrokSearchReauthRequired 持久标记 grok_search 账号"需重认证"：
// DB SetError（status=error + error_message + schedulable=false，不自动恢复）+ 内存调度即时下线。
// 适用于 SSO 401 失效（SSO 无 refresh，只能管理员重导）。
func (s *OpenAIGatewayService) markGrokSearchReauthRequired(ctx context.Context, account *Account) {
	if s == nil || account == nil {
		return
	}
	const reason = "grok_search SSO token unauthorized; re-import SSO required"
	// 内存调度即时下线（24h 兜底，等 DB snapshot 同步；SSO 无 refresh 不会自愈）
	s.BlockAccountScheduling(account, time.Now().Add(24*time.Hour), reason)
	if s.accountRepo != nil {
		stateCtx, cancel := openAIAccountStateContext(ctx)
		defer cancel()
		if err := s.accountRepo.SetError(stateCtx, account.ID, reason); err != nil {
			logger.LegacyPrintf("service.openai_gateway_grok_search",
				"mark grok_search reauth failed account_id=%d err=%v", account.ID, err)
		}
	}
}
