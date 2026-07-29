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

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/util/httputil"
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
	// 识别 effort 后缀：部分客户端（如 smart Research）只能填模型名、无法单独传 reasoning.effort，
	// 允许用「真模型名 + -xhigh 等」表达 Agent 协同强度。剥离后缀得到真模型名，
	// 并把 effort 作为归一化优先值注入 reasoning.effort（客户端显式 effort 仍优先于后缀值）。
	baseModel, effortFromSuffix := splitGrokSearchEffortSuffix(upstreamModel)
	upstreamModel = baseModel

	// 3. body 施加 console 契约
	patchedBody, err := normalizeGrokSearchRequestBody(body, upstreamModel, effortFromSuffix)
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
//   - reasoning.effort 归一（minimal/max 等 → console 档位），缺省 medium；preferredEffort
//     为模型名 effort 后缀剥离值（见 splitGrokSearchEffortSuffix），客户端未显式带 effort 时生效。
//   - include 补 reasoning.encrypted_content（multi-agent 链路需要）。
//   - tools 归一 + 注入 web_search/x_search（multi-agent 搜索能力）。
//   - tool_choice 缺省 auto。
func normalizeGrokSearchRequestBody(body []byte, upstreamModel string, preferredEffort string) ([]byte, error) {
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
	normalizeGrokSearchReasoning(payload, preferredEffort)
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

// normalizeGrokSearchReasoning 归一 reasoning.effort。
// 优先级：客户端请求体显式带的 effort > preferredEffort（模型名后缀剥离值）> 默认 medium。
// multi-agent 默认档为 medium；preferredEffort 让「真模型名 + effort 后缀」的写法生效。
func normalizeGrokSearchReasoning(payload map[string]any, preferredEffort string) {
	reasoning, _ := payload["reasoning"].(map[string]any)
	if reasoning == nil {
		reasoning = map[string]any{}
	}
	effort, _ := reasoning["effort"].(string)
	effort = normalizeGrokSearchEffort(effort) // 客户端显式带的优先
	if effort == "" {
		effort = normalizeGrokSearchEffort(preferredEffort) // 模型名后缀剥离值次之
	}
	if effort == "" {
		effort = "medium" // 最后兜底默认 medium（multi-agent 默认档）
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

// splitGrokSearchEffortSuffix 从模型名末尾识别 reasoning effort 后缀
// （-low/-medium/-high/-xhigh/-max），返回剥离后缀的真实模型名与对应的 effort
// （无后缀、后缀非法、或剥离后 base 为空时 effort 返回空串）。
//
// 背景：部分客户端（如 smart Research）只能填模型名、无法单独传 reasoning.effort。
// 允许用「真模型名 + effort 后缀」表达 multi-agent 协同强度，例如
// grok-4.20-multi-agent-0309-xhigh → 模型 grok-4.20-multi-agent-0309 + reasoning.effort=xhigh。
//
// 规则：
//   - 大小写不敏感识别后缀（grok-...-XHIGH 同样生效），但剥离时保留 base 前缀的原始大小写。
//   - 从长到短匹配，避免 -high 误吃 -xhigh 等（各后缀虽以 '-' 开头天然不冲突，仍保持防御性顺序）。
//   - 后缀必须是 normalizeGrokSearchEffort 认识的 low/medium/high/xhigh(max) 之一才剥离；
//     none 不作为后缀支持（none 是显式关闭推理的特殊值，不该用模型名后缀表达）。
//   - 剥离后 base 必须非空（不能整个模型名就是后缀）。
func splitGrokSearchEffortSuffix(model string) (baseModel string, effort string) {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return trimmed, ""
	}
	// 从长到短：-medium(7) > -xhigh(6) > -high(5) > -low/-max(4)。
	suffixes := []string{"-medium", "-xhigh", "-high", "-low", "-max"}
	lower := strings.ToLower(trimmed)
	for _, sfx := range suffixes {
		if !strings.HasSuffix(lower, sfx) {
			continue
		}
		idx := len(trimmed) - len(sfx)
		if idx <= 0 {
			// 整串就是后缀，base 为空，不剥离
			continue
		}
		base := trimmed[:idx]
		// 跳过前导 '-'，取 effort 词交给 normalize 判定合法性（normalize 不识别带 '-' 的串）
		effortWord := trimmed[idx+1:]
		normalized := normalizeGrokSearchEffort(effortWord)
		if normalized == "" || normalized == "none" {
			continue
		}
		return base, normalized
	}
	return trimmed, ""
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
// 策略（design §2 状态码决策树）：
//   - 401：SSO 失效，持久标记需重认证（无 refresh，管理员重导 SSO）。
//   - 403：CF 挑战 → 不惩罚账号（出口/指纹问题）；SSO 权限失效 → 同 401 重认证；其它 → 不特殊处理。
//   - 429：CF 挑战 → 不惩罚账号；免费额度耗尽 → 长冷却 24h；普通瞬时频率限制 → 短退避 5min。
//   - 5xx：非池模式下短冷却。
//   - 其余：不冷却，仅由调用方透传错误。
//
// 判定顺序关键：CF 判定必须最前——IsCloudflareChallengeResponse 对 403 和 429 都会命中
// （CF 挑战可能以 403 或 429 形态出现），CF 是出口/指纹问题，绝不能当账号额度/权限问题去冷却或失效账号。
func (s *OpenAIGatewayService) handleGrokSearchAccountUpstreamError(ctx context.Context, account *Account, statusCode int, headers http.Header, responseBody []byte) {
	if s == nil || account == nil {
		return
	}
	switch statusCode {
	case http.StatusUnauthorized:
		// SSO 无 refresh，失效只能重导：持久标记需重认证（status=error + schedulable=false），
		// 不用 temp 短时冷却（会 401 循环——10 分钟后恢复调度又用失效 SSO 打）。管理员重导 SSO 后账号恢复 active。
		s.markGrokSearchReauthRequired(ctx, account)
	case http.StatusForbidden:
		// CF 挑战最优先：console.x.ai 在 Cloudflare 后，403 常为出口/指纹问题而非账号问题，
		// 绝不能当账号额度/权限问题去冷却或失效账号。不惩罚账号，交由上层既有机制处理。
		if httputil.IsCloudflareChallengeResponse(statusCode, headers, responseBody) {
			return
		}
		// SSO 权限丢失（console 权限拒绝）：同 401 处理，需管理员重导 SSO。
		if isGrokSearchPermissionDenied(responseBody) {
			s.markGrokSearchReauthRequired(ctx, account)
			return
		}
		// 其它 403：不特殊处理（保持 default 语义，不冷却不失效）
	case http.StatusTooManyRequests:
		// CF 挑战可能伪装成 429，最优先排除，不惩罚账号（避免把出口问题误判为额度/频率问题去冷却账号）
		if httputil.IsCloudflareChallengeResponse(statusCode, headers, responseBody) {
			return
		}
		// 免费额度耗尽（非瞬时频率限制）：长冷却 24h，避免 5min 恢复后又 429 的无效循环。
		// console 网页订阅免费额度按周期重置，对齐 grok2api defaultFreeQuotaRecoveryPause。
		if isGrokSearchFreeQuotaExhausted(responseBody) {
			s.tempUnscheduleGrokSearch(ctx, account, grokSearchFreeQuotaCooldown, "grok_search free usage quota exhausted")
			return
		}
		// 普通瞬时频率限制：短退避（保持原有行为）
		s.tempUnscheduleGrokSearch(ctx, account, grokSearchRateLimitCooldown, "grok_search rate limited")
	default:
		if statusCode >= 500 && !account.IsPoolMode() {
			s.tempUnscheduleGrokSearch(ctx, account, 2*time.Minute, "grok_search upstream temporary error")
		}
	}
}

// grok_search 错误处理冷却时长。
const (
	// grokSearchFreeQuotaCooldown：console 网页订阅"免费额度耗尽"冷却时长。
	// 该错误本质是免费配额按周期耗尽（非瞬时频率限制），短冷却（5min）无效——
	// 恢复调度后又 429，形成无效循环。固定 24h 对齐 grok2api defaultFreeQuotaRecoveryPause，
	// 确定可预期（console 该错误可能不带 Retry-After，本期不增加配置项）。
	grokSearchFreeQuotaCooldown = 24 * time.Hour
	// grokSearchRateLimitCooldown：普通瞬时频率限制（RPS 等）的短退避，保持原有 5min 行为。
	grokSearchRateLimitCooldown = 5 * time.Minute
)

// isGrokSearchFreeQuotaExhausted 识别 console 免费额度耗尽的 429 响应。
// 实测 body 形如：{"code":"resource-exhausted","error":"Free usage quota exceeded. Purchase credits..."}。
//
// 注意：不单看 code:resource-exhausted——grok2api 经验显示 console 的 RPS 速率限流也是这个 code
// （其 error 文本为 "Too many requests for team... Requests per Second"）。单看 code 会把 RPS 限流
// 误判成额度耗尽、走 24h 长冷却（实际只需 5min）。这里用 error 文本 "free usage quota" 精确区分，
// 大小写不敏感。
func isGrokSearchFreeQuotaExhausted(body []byte) bool {
	return strings.Contains(strings.ToLower(string(body)), "free usage quota")
}

// isGrokSearchPermissionDenied 识别 console SSO 权限失效的 403 响应（参照 grok2api permanent denial 语义）。
// 命中即视为 SSO 权限丢失（与 401 同处理，需管理员重导 SSO）。大小写不敏感。
func isGrokSearchPermissionDenied(body []byte) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "permission-denied") ||
		strings.Contains(lower, "permission_denied") ||
		strings.Contains(lower, "access to the chat endpoint is denied")
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

// forwardGrokSearchChatCompletionsViaResponses 把 grok_search 的 /v1/chat/completions
// 请求桥接到 console.x.ai /v1/responses 通道。
//
// 设计参照 grok 平台的 forwardGrokChatCompletionsViaResponses（openai_gateway_grok_chat_bridge.go:506），
// 但关键差异（grok 与 grok_search 物理隔离）：
//   - 凭证：自读 sso_token（account.GetCredential("sso_token")），不调 getRequestCredential/GetAccessToken
//     （grok_search 身份由 SSO cookie 唯一决定，无 OIDC token）。
//   - 端点+请求构造：复用 buildGrokSearchRequest（console.x.ai + SSO cookie + Chrome headers），
//     不用 buildGrokResponsesRequest（后者打 cli-chat-proxy.grok.com + OAuth Bearer）。
//   - body：apicompat.ChatCompletionsToResponses 转 responses body → normalizeGrokSearchRequestBody
//     （forwardGrokSearch 用的 console 契约归一化：store=false、tools 注入 web_search/x_search 等）。
//   - 发送：DoWithTLS + grokSearchChromeProfile()（过 console.x.ai 的 Cloudflare），
//     不用裸 Do（grok 桥走 cli-chat-proxy 不需要 Chrome 指纹）。
//   - 错误处理：handleGrokSearchAccountUpstreamError（grok_search 独立策略，不含 402 冷却），
//     不用 handleGrokAccountUpstreamError（后者会解析 grok OIDC 配额快照并触发 402 冷却）。
//   - 响应：复用 handleChatStreamingResponse/handleChatBufferedStreamingResponse
//     （chat 格式响应处理，客户端要的是 chat completions 而非 responses 格式）。
//
// 上游通道仍是 console.x.ai/v1/responses（responses 格式 SSE），但网关把它转回 chat completions
// 格式写给客户端，因此与 forwardGrokSearch 的 responses 直写不同——本方法面向 chat completions 入口。
func (s *OpenAIGatewayService) forwardGrokSearchChatCompletionsViaResponses(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	promptCacheKey string,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	// 1. 解析 chat completions 请求
	var chatReq apicompat.ChatCompletionsRequest
	if err := json.Unmarshal(body, &chatReq); err != nil {
		return nil, fmt.Errorf("parse grok_search chat completions request: %w", err)
	}
	originalModel := chatReq.Model
	clientStream := chatReq.Stream
	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	// grok_search multi-agent 靠 model 名进入对应模式；account.GetMappedModel 在 forwardGrokSearch
	// 里已用于覆盖，这里同样以 GetMappedModel 为准（覆盖到 normalize 后的 upstreamModel）。
	if mapped := account.GetMappedModel(originalModel); strings.TrimSpace(mapped) != "" {
		upstreamModel = mapped
	}
	// 识别 effort 后缀（与 forwardGrokSearch 同源）：剥离后得到真模型名 + 优先 effort。
	// 注意：剥离只影响发给上游的 upstreamModel 与 reasoning.effort，不影响 billingModel 计费。
	baseModel, effortFromSuffix := splitGrokSearchEffortSuffix(upstreamModel)
	upstreamModel = baseModel

	// 2. chat body → responses body
	responsesReq, err := apicompat.ChatCompletionsToResponses(&chatReq)
	if err != nil {
		return nil, fmt.Errorf("convert grok_search chat completions to responses: %w", err)
	}
	responsesReq.Model = upstreamModel
	// 上游始终流式（与 grok 桥、forwardGrokSearch 一致），由 handleChatBufferedStreamingResponse 聚合
	responsesReq.Stream = true
	responsesReq.Include = nil
	responsesReq.Store = nil
	responsesBody, err := json.Marshal(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("marshal grok_search responses bridge request: %w", err)
	}

	// 3. 施加 console 契约（store=false、删 metadata/service_tier 等、tools 注入 web_search/x_search、
	// include 补 reasoning.encrypted_content、reasoning.effort 归一）——与 forwardGrokSearch 同源。
	responsesBody, err = normalizeGrokSearchRequestBody(responsesBody, upstreamModel, effortFromSuffix)
	if err != nil {
		return nil, fmt.Errorf("normalize grok_search chat bridge request body: %w", err)
	}

	// 4. 自读 SSO 凭证 + 拼 console.x.ai/v1/responses（不走 GetAccessToken / grokTokenProvider）
	ssoToken := strings.TrimSpace(account.GetCredential("sso_token"))
	if ssoToken == "" {
		return nil, fmt.Errorf("grok_search account missing sso_token credential")
	}
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	if baseURL == "" {
		baseURL = grokSearchDefaultBaseURL
	}
	upstreamURL := strings.TrimSuffix(baseURL, "/") + grokSearchResponsesPath

	// 5. 解耦客户端 context（客户端断开后上游仍可跑完用于计费/日志）
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	defer releaseUpstreamCtx()
	req, err := buildGrokSearchRequest(upstreamCtx, responsesBody, ssoToken, upstreamURL)
	if err != nil {
		return nil, err
	}
	SetActualOpenAIUpstreamEndpoint(c, grokSearchResponsesPath)

	// 6. 代理
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	// 7. 发送（CF 策略 L1：Chrome uTLS 指纹 + 一致 headers，与 forwardGrokSearch 同源）
	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, grokSearchChromeProfile())
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	// 8. 错误分流（grok_search 独立策略，不复用 grok 的 402 冷却 / quota snapshot）
	if resp.StatusCode >= http.StatusBadRequest {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if upstreamMsg == "" {
			upstreamMsg = fmt.Sprintf("grok_search upstream returned status %d", resp.StatusCode)
		}
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: resp.StatusCode,
			UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
			Kind:               "http_error",
			Message:            upstreamMsg,
		})
		s.handleGrokSearchAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		return s.handleChatCompletionsErrorResponse(resp, c, account, billingModel)
	}

	// 9. 成功响应：上游是 responses 格式 SSE，转为 chat completions 格式写给客户端
	var result *OpenAIForwardResult
	if clientStream {
		result, err = s.handleChatStreamingResponse(resp, c, account, originalModel, billingModel, upstreamModel, startTime, len(body))
	} else {
		result, err = s.handleChatBufferedStreamingResponse(resp, c, account, originalModel, billingModel, upstreamModel, startTime)
	}
	if result != nil {
		result.UpstreamEndpoint = grokSearchResponsesPath
		result.ResponseHeaders = resp.Header.Clone()
		if result.RequestID == "" {
			result.RequestID = firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id"))
		}
		result.ReasoningEffort = extractOpenAIReasoningEffortFromBody(body, upstreamModel, billingModel, originalModel)
	}
	return result, err
}
