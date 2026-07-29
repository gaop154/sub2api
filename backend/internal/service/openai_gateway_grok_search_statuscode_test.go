package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件验证 handleGrokSearchAccountUpstreamError 对 403（CF 挑战 / SSO 权限失效）与
// 429（免费额度耗尽 / 普通瞬时频率限制 / CF 挑战）的差异化处理（design §2 状态码决策树），
// 以及与 grok 平台错误处理的物理隔离（REQ-3）。
//
// 关键判定顺序（最易出 bug 点）：CF 判定必须最前——403/429 都先排除 CF 挑战，再分别按
// 权限失效 / 免费额度耗尽 / 普通限流分支处理。

// grokSearchStatusTestAccount 构造一个标准 grok_search 账号用于状态码处理测试。
func grokSearchStatusTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Name:        "grok-search-status",
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
}

// grokSearchLoadRuntimeBlockUntil 读取内存调度 block 的 until 时间；未 blocked 时返回 (zero, false)。
// tempUnscheduleGrokSearch / markGrokSearchReauthRequired 内部都调 BlockAccountScheduling 写入此值，
// 测试据此断言冷却时长。accountRepo 为 nil 时跳过 DB 写入，仅留内存副作用，便于单测。
func grokSearchLoadRuntimeBlockUntil(t *testing.T, svc *OpenAIGatewayService, accountID int64) (time.Time, bool) {
	t.Helper()
	value, ok := svc.openaiAccountRuntimeBlockUntil.Load(accountID)
	if !ok {
		return time.Time{}, false
	}
	until, ok := value.(time.Time)
	require.True(t, ok, "runtime block 值应为 time.Time")
	return until, true
}

// cfChallengeHeaders 构造触发 IsCloudflareChallengeResponse 的响应头（cf-mitigated: challenge）。
// 注意：必须用 Set 写入（自动 canonical 化为 Cf-Mitigated），用 map 字面量 {"cf-mitigated":...}
// 会因大小写不匹配导致 headers.Get 找不到——真实 HTTP 响应头经 net/http 解析均为 canonical 形式。
func cfChallengeHeaders() http.Header {
	h := http.Header{}
	h.Set("cf-mitigated", "challenge")
	return h
}

// === 429 分支 ===

// TestHandleGrokSearchAccountUpstreamError_429FreeQuotaExhausted_LongCooldown 验证 REQ-1：
// 429 body 含 "free usage quota"（实测 console 免费额度耗尽形态）→ 长冷却 24h，
// 而非原有的 5min 短退避（恢复后又 429 的无效循环）。
func TestHandleGrokSearchAccountUpstreamError_429FreeQuotaExhausted_LongCooldown(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := grokSearchStatusTestAccount(101)
	body := []byte(`{"code":"resource-exhausted","error":"Free usage quota exceeded. Purchase credits or provision an API key at https://console.x.ai"}`)

	svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body)

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "免费额度耗尽应 block 账号")
	until, ok := grokSearchLoadRuntimeBlockUntil(t, svc, account.ID)
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(grokSearchFreeQuotaCooldown), until, 5*time.Second,
		"冷却时长应为 24h（grokSearchFreeQuotaCooldown）")
}

// TestHandleGrokSearchAccountUpstreamError_429NormalRateLimit_ShortCooldown 验证：
// 429 普通瞬时频率限制（RPS 速率限流，code 也是 resource-exhausted，但 error 文本不同）
// → 保持原有 5min 短退避。同时验证不因 code:resource-exhausted 被误判为额度耗尽。
func TestHandleGrokSearchAccountUpstreamError_429NormalRateLimit_ShortCooldown(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := grokSearchStatusTestAccount(102)
	// RPS 速率限流形态：code 也是 resource-exhausted，但 error 是 "Too many requests ... Requests per Second"
	body := []byte(`{"code":"resource-exhausted","error":"Too many requests for team grok-dev. Requests per Second exceeded"}`)

	svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, http.Header{}, body)

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "普通限流应 block 账号")
	until, ok := grokSearchLoadRuntimeBlockUntil(t, svc, account.ID)
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(grokSearchRateLimitCooldown), until, 5*time.Second,
		"冷却时长应为 5min（grokSearchRateLimitCooldown），不因 resource-exhausted code 被误判为 24h")
}

// TestHandleGrokSearchAccountUpstreamError_429Cloudflare_NotBlocked 验证 REQ-2/决策树：
// 429 实为 CF 挑战（cf-mitigated: challenge 头）→ 不惩罚账号（不 block）。
func TestHandleGrokSearchAccountUpstreamError_429Cloudflare_NotBlocked(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := grokSearchStatusTestAccount(103)

	svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, cfChallengeHeaders(), []byte(`{}`))

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "CF 挑战伪装成 429 时不应 block 账号")
}

// TestHandleGrokSearchAccountUpstreamError_429CloudflareBeatsFreeQuota 验证判定顺序（最大易错点）：
// 429 同时含 CF 特征与免费额度耗尽文本时，CF 判定最优先 → 不惩罚账号。
// 若顺序错误（先判额度），CF 挑战会被误判为额度耗尽去冷却账号。
func TestHandleGrokSearchAccountUpstreamError_429CloudflareBeatsFreeQuota(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := grokSearchStatusTestAccount(104)
	body := []byte(`{"code":"resource-exhausted","error":"Free usage quota exceeded"}`)

	svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, cfChallengeHeaders(), body)

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account),
		"CF 判定必须最优先：即便 body 含 free usage quota，CF 挑战也不应惩罚账号")
}

// === 403 分支 ===

// TestHandleGrokSearchAccountUpstreamError_403Cloudflare_NotBlocked 验证 REQ-2：
// 403 为 CF 挑战（body 含 HTML challenge 标记 "just a moment"）→ 不惩罚账号（不 block、不 reauth）。
func TestHandleGrokSearchAccountUpstreamError_403Cloudflare_NotBlocked(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := grokSearchStatusTestAccount(105)
	// IsCloudflareChallengeResponse 对 body 含 HTML challenge 标记（"just a moment" 等）判定为 CF
	body := []byte(`<html><head><title>Just a moment...</title></head><body>challenge-platform</body></html>`)

	svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "CF 挑战 403 不应 block 账号")
}

// TestHandleGrokSearchAccountUpstreamError_403PermissionDenied_Reauth 验证 REQ-2：
// 403 body 含权限拒绝特征（SSO 权限丢失）→ markGrokSearchReauthRequired（与 401 同路径，
// 内部调 BlockAccountScheduling 24h 兜底，DB 层 status=error + schedulable=false）。
func TestHandleGrokSearchAccountUpstreamError_403PermissionDenied_Reauth(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := grokSearchStatusTestAccount(106)
	body := []byte(`{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`)

	svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)

	// markGrokSearchReauthRequired 内部调 BlockAccountScheduling（24h 兜底），以此作为 reauth 生效的内存信号
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "SSO 权限失效应 block 账号（markGrokSearchReauthRequired）")
	until, ok := grokSearchLoadRuntimeBlockUntil(t, svc, account.ID)
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(24*time.Hour), until, 5*time.Second,
		"markGrokSearchReauthRequired 兜底 block 24h")
}

// TestHandleGrokSearchAccountUpstreamError_403Other_NotBlocked 验证：
// 既非 CF 也非权限失效的 403 → 不特殊处理（保持 default 语义，不冷却不失效）。
func TestHandleGrokSearchAccountUpstreamError_403Other_NotBlocked(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := grokSearchStatusTestAccount(107)
	body := []byte(`{"error":"forbidden"}`)

	svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, body)

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "其它 403 不应 block 账号（保持 default 语义）")
}

// TestHandleGrokSearchAccountUpstreamError_403CloudflareBeatsPermissionDenied 验证判定顺序：
// 403 同时含 CF 特征与权限拒绝文本时，CF 判定最优先 → 不惩罚账号。
func TestHandleGrokSearchAccountUpstreamError_403CloudflareBeatsPermissionDenied(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := grokSearchStatusTestAccount(108)
	body := []byte(`{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`)

	svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusForbidden, cfChallengeHeaders(), body)

	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account),
		"CF 判定必须最优先：即便 body 含权限拒绝特征，CF 挑战也不应惩罚账号")
}

// === 401/5xx 不回归 ===

// TestHandleGrokSearchAccountUpstreamError_401StillReauth 验证 401 分支未受影响：仍走 markReauth。
func TestHandleGrokSearchAccountUpstreamError_401StillReauth(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := grokSearchStatusTestAccount(109)

	svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, []byte(`{"error":"unauthorized"}`))

	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "401 应走 markGrokSearchReauthRequired block 账号")
}

// === 辅助识别函数（pure classifier）===

// TestIsGrokSearchFreeQuotaExhausted 验证免费额度耗尽识别：用 error 文本 "free usage quota"
// 精确区分，不单看 code:resource-exhausted（RPS 限流也是该 code）。
func TestIsGrokSearchFreeQuotaExhausted(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"实测免费额度耗尽形态", `{"code":"resource-exhausted","error":"Free usage quota exceeded. Purchase credits"}`, true},
		{"大小写不敏感", `FREE USAGE QUOTA exceeded`, true},
		{"RPS 速率限流（同 code 但非额度耗尽）", `{"code":"resource-exhausted","error":"Too many requests for team. Requests per Second"}`, false},
		{"普通限流文本", `{"error":"rate limited"}`, false},
		{"空 body", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokSearchFreeQuotaExhausted([]byte(tt.body)))
		})
	}
}

// TestIsGrokSearchPermissionDenied 验证 SSO 权限失效识别（多种特征 + 大小写不敏感）。
func TestIsGrokSearchPermissionDenied(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"permission-denied (kebab)", `{"code":"permission-denied"}`, true},
		{"permission_denied (snake)", `permission_denied`, true},
		{"chat endpoint denied 长句", `Access to the chat endpoint is denied`, true},
		{"大小写不敏感", `PERMISSION-DENIED`, true},
		{"无关 403 forbidden", `{"error":"forbidden"}`, false},
		{"空 body", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isGrokSearchPermissionDenied([]byte(tt.body)))
		})
	}
}

// === 隔离回归（REQ-3）===

// TestHandleGrokSearchErrorHandling_IsolatedFromGrok 验证物理隔离：
// 同样的 403 权限拒绝 body，grok 平台走自己的 handleGrokAccountUpstreamError
// （403 非内容策略 → applyGrokForbiddenPolicy 未配规则时回落 tempUnscheduleGrok 30min），
// 不会触发 grok_search 的 markGrokSearchReauthRequired（24h 兜底 block）。
// 两平台 handler 产出冷却时长不同，证明 grok_search 状态码识别逻辑未泄漏到 grok 平台。
func TestHandleGrokSearchErrorHandling_IsolatedFromGrok(t *testing.T) {
	body := []byte(`{"code":"permission-denied","error":"Access to the chat endpoint is denied"}`)

	// grok_search：403 权限失效 → markGrokSearchReauthRequired（24h 兜底 block）
	grokSearchSvc := &OpenAIGatewayService{}
	grokSearchAccount := grokSearchStatusTestAccount(110)
	grokSearchSvc.handleGrokSearchAccountUpstreamError(
		context.Background(), grokSearchAccount, http.StatusForbidden, http.Header{}, body)
	require.True(t, grokSearchSvc.isOpenAIAccountRuntimeBlocked(grokSearchAccount),
		"grok_search 403 权限失效应 block")
	gsUntil, _ := grokSearchLoadRuntimeBlockUntil(t, grokSearchSvc, grokSearchAccount.ID)

	// grok：403（非内容策略）→ tempUnscheduleGrok 30min（entitlement cooldown）
	grokSvc := &OpenAIGatewayService{}
	grokAccount := &Account{
		ID:          111,
		Name:        "grok-isolation",
		Platform:    PlatformGrok,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
	}
	grokSvc.handleGrokAccountUpstreamError(
		context.Background(), grokAccount, http.StatusForbidden, http.Header{}, body)
	require.True(t, grokSvc.isOpenAIAccountRuntimeBlocked(grokAccount),
		"grok 403 应走自己的 entitlement cooldown block")
	grokUntil, _ := grokSearchLoadRuntimeBlockUntil(t, grokSvc, grokAccount.ID)

	// 冷却时长不同：grok_search ≈ 24h（reauth 兜底），grok ≈ 30min（entitlement cooldown）
	require.WithinDuration(t, time.Now().Add(24*time.Hour), gsUntil, 5*time.Second,
		"grok_search reauth 兜底应为 24h")
	require.WithinDuration(t, time.Now().Add(30*time.Minute), grokUntil, 5*time.Second,
		"grok 403 entitlement cooldown 应为 30min")
	require.True(t, gsUntil.After(grokUntil),
		"grok_search reauth 兜底应远长于 grok 403 退避，证明两个 handler 相互隔离、未共用 grok_search 识别逻辑")
}
