package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件验证 grok_search 平台接入 OpenAI 兼容选号流程的 3 处平台归属判定
// （normalizeOpenAICompatiblePlatform / IsOpenAICompatible / isOpenAIAccount）。
//
// 背景：grok_search 账号在选号过滤阶段被 platform_mismatch 排除，导致调度池为空
// （pool=0，503 Service temporarily unavailable）。根因是建桶用真实 account.Platform
// （PlatformGrokSearch），但选号取桶/过滤用 normalizeOpenAICompatiblePlatform 归一后的值，
// 而 grok_search 被归一成 PlatformOpenAI，建桶/取桶平台 key 错位 → 取不到账号。
// 修复后 3 处对齐建桶语义：grok_search 返回自身（PlatformGrokSearch）。

// grokSearchSelectionTestAccount 构造一个标准 grok_search 账号用于选号相关测试。
func grokSearchSelectionTestAccount(id int64) *Account {
	return &Account{
		ID:          id,
		Name:        "grok-search-selection",
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

// TestNormalizeOpenAICompatiblePlatform_GrokSearchReturnsSelf 验证修复 1：
// grok_search 平台归一化后返回自身（PlatformGrokSearch），而非被归一成 PlatformOpenAI
// 或 PlatformGrok。建桶 key 是 PlatformGrokSearch，取桶必须一致。
func TestNormalizeOpenAICompatiblePlatform_GrokSearchReturnsSelf(t *testing.T) {
	// grok_search 必须返回自身
	require.Equal(t, PlatformGrokSearch, normalizeOpenAICompatiblePlatform(PlatformGrokSearch))

	// 现有 grok / openai 行为不应被破坏（回归）
	require.Equal(t, PlatformGrok, normalizeOpenAICompatiblePlatform(PlatformGrok))
	require.Equal(t, PlatformOpenAI, normalizeOpenAICompatiblePlatform(PlatformOpenAI))
	// 未知平台仍归一为 PlatformOpenAI
	require.Equal(t, PlatformOpenAI, normalizeOpenAICompatiblePlatform("unknown-platform"))
}

// TestIsOpenAICompatible_GrokSearch 验证修复 2：grok_search 账号被判定为 OpenAI 兼容。
// 选号过滤 openai_account_scheduler.go:1354 依赖 IsOpenAICompatible()，grok_search 命中前
// 因该返回 false 被排除；修复后返回 true，不再以 platform_mismatch 排除。
func TestIsOpenAICompatible_GrokSearch(t *testing.T) {
	account := grokSearchSelectionTestAccount(91)
	require.True(t, account.IsOpenAICompatible(), "grok_search 账号必须被视为 OpenAI 兼容")

	// 回归：openai / grok 仍兼容，其他平台不兼容
	require.True(t, (&Account{Platform: PlatformOpenAI}).IsOpenAICompatible())
	require.True(t, (&Account{Platform: PlatformGrok}).IsOpenAICompatible())
	require.False(t, (&Account{Platform: PlatformGemini}).IsOpenAICompatible())
	require.False(t, (&Account{}).IsOpenAICompatible(), "nil-safe：空账号不兼容")
}

// TestIsOpenAIAccount_GrokSearch 验证修复 3：isOpenAIAccount 对 grok_search 返回 true。
// BlockAccountScheduling / isOpenAIAccountRuntimeBlocked 均受 isOpenAIAccount 守卫，
// 修复前 grok_search 不在内 → BlockAccountScheduling no-op，内存即时下线失效
// （只能靠 DB SetTempUnschedulable + 快照重建，有延迟）。
// 修复后 grok_search 自身的 tempUnscheduleGrokSearch / markGrokSearchReauthRequired
// 调 BlockAccountScheduling 才能真正写入 runtime block。
func TestIsOpenAIAccount_GrokSearch(t *testing.T) {
	account := grokSearchSelectionTestAccount(92)
	require.True(t, isOpenAIAccount(account), "grok_search 账号必须被 isOpenAIAccount 认定")

	// 回归：openai / grok 仍命中，其他平台不命中
	require.True(t, isOpenAIAccount(&Account{Platform: PlatformOpenAI}))
	require.True(t, isOpenAIAccount(&Account{Platform: PlatformGrok}))
	require.False(t, isOpenAIAccount(&Account{Platform: PlatformGemini}))
	require.False(t, isOpenAIAccount(nil), "nil-safe")
}

// TestBlockAccountScheduling_GrokSearchNowEffective 验证修复 3 的端到端效果：
// grok_search 账号调用 BlockAccountScheduling 后能被 isOpenAIAccountRuntimeBlocked 命中
// （内存即时下线生效）。修复前该调用为 no-op，isOpenAIAccountRuntimeBlocked 永远返回 false。
// 这是 grok_search 的 handleGrokSearchAccountUpstreamError → tempUnscheduleGrokSearch /
// markGrokSearchReauthRequired 的预期行为，与 grok 平台对齐。
func TestBlockAccountScheduling_GrokSearchNowEffective(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := grokSearchSelectionTestAccount(93)

	// 修复前：no-op，永远不 blocked
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "初始状态不应被 block")

	svc.BlockAccountScheduling(account, time.Time{}, "grok_search_reauth")

	// 修复后：内存即时下线生效，与 grok 平台对齐
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "grok_search 账号经 BlockAccountScheduling 后必须被 runtime block")

	// 清理后恢复
	svc.ClearAccountSchedulingBlock(account.ID)
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(account), "ClearAccountSchedulingBlock 后应解除 block")
}

// TestGrokSearchSelectionFilterNotExcludedByPlatformMismatch 端到端验证选号过滤逻辑
// （openai_account_scheduler.go:1354 的过滤条件）：grok_search 账号不再因 platform_mismatch 被排除。
// 复刻过滤条件：account.Platform == normalizeOpenAICompatiblePlatform(req.Platform) && account.IsOpenAICompatible()。
func TestGrokSearchSelectionFilterNotExcludedByPlatformMismatch(t *testing.T) {
	account := grokSearchSelectionTestAccount(94)
	normalizedPlatform := normalizeOpenAICompatiblePlatform(PlatformGrokSearch)

	// 修复后两个条件都满足，不会被 platform_mismatch 排除
	require.Equal(t, PlatformGrokSearch, normalizedPlatform, "归一化后平台 key 必须与建桶 key 一致")
	require.Equal(t, account.Platform, normalizedPlatform, "账号平台必须等于归一化后的请求平台（过滤条件 1）")
	require.True(t, account.IsOpenAICompatible(), "账号必须 OpenAI 兼容（过滤条件 2）")
	// 等价于过滤逻辑不排除
	passesPlatformFilter := account.Platform == normalizedPlatform && account.IsOpenAICompatible()
	require.True(t, passesPlatformFilter, "grok_search 账号必须通过平台过滤，不再被 platform_mismatch 排除")

	// 回归：openai / grok 账号各自平台过滤仍通过
	openAIAccount := &Account{Platform: PlatformOpenAI}
	grokAccount := &Account{Platform: PlatformGrok}
	require.True(t, openAIAccount.Platform == normalizeOpenAICompatiblePlatform(PlatformOpenAI) && openAIAccount.IsOpenAICompatible())
	require.True(t, grokAccount.Platform == normalizeOpenAICompatiblePlatform(PlatformGrok) && grokAccount.IsOpenAICompatible())

	// 回归：gemini 账号仍被排除（不应误纳入 OpenAI 兼容选号）
	geminiAccount := &Account{Platform: PlatformGemini}
	require.False(t, geminiAccount.Platform == normalizeOpenAICompatiblePlatform(PlatformGemini) && geminiAccount.IsOpenAICompatible())
}
