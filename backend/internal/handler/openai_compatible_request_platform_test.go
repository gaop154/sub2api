package handler

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestOpenAICompatibleRequestPlatform_GrokSearchGroupReturnsSelf 验证 grok_search 分组
// 在普通请求场景（无 composite resolved platform）下返回 PlatformGrokSearch 自身。
//
// 背景：该函数返回值作为选号 requestPlatform 传入调度层，下游
// normalizeOpenAICompatiblePlatform 与调度桶 key 均使用 PlatformGrokSearch；
// 修复前 grok_search 分组在此被归一成 PlatformOpenAI，导致选号按 openai 平台查桶
// 取不到 grok_search 账号（pool=0 → 503）。
func TestOpenAICompatibleRequestPlatform_GrokSearchGroupReturnsSelf(t *testing.T) {
	apiKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformGrokSearch}}
	require.Equal(t, service.PlatformGrokSearch, openAICompatibleRequestPlatform(context.Background(), apiKey))
}

// TestOpenAICompatibleRequestPlatform_GroupPlatformRegression 验证现有分组平台归属
// 不被破坏：openai / grok 分组各自归位，其他平台仍归为 PlatformOpenAI。
func TestOpenAICompatibleRequestPlatform_GroupPlatformRegression(t *testing.T) {
	require.Equal(t, service.PlatformOpenAI,
		openAICompatibleRequestPlatform(context.Background(), &service.APIKey{Group: &service.Group{Platform: service.PlatformOpenAI}}))
	require.Equal(t, service.PlatformGrok,
		openAICompatibleRequestPlatform(context.Background(), &service.APIKey{Group: &service.Group{Platform: service.PlatformGrok}}))
	require.Equal(t, service.PlatformOpenAI,
		openAICompatibleRequestPlatform(context.Background(), &service.APIKey{Group: &service.Group{Platform: service.PlatformGemini}}))
	// nil 安全：无 apiKey / 无 Group 时默认 PlatformOpenAI
	require.Equal(t, service.PlatformOpenAI, openAICompatibleRequestPlatform(context.Background(), nil))
	require.Equal(t, service.PlatformOpenAI, openAICompatibleRequestPlatform(context.Background(), &service.APIKey{}))
}

// TestOpenAICompatibleRequestPlatform_CompositeResolvedGrokSearch 验证 composite 场景：
// ResolvedTargetPlatform=grok_search 时返回 PlatformGrokSearch 自身（composite 优先于 apiKey 分组）。
func TestOpenAICompatibleRequestPlatform_CompositeResolvedGrokSearch(t *testing.T) {
	ctx := service.WithResolvedTargetPlatform(context.Background(), service.PlatformGrokSearch)
	require.Equal(t, service.PlatformGrokSearch, openAICompatibleRequestPlatform(ctx, nil))
	// 即使 apiKey 挂在 composite 分组，也以 resolved platform 为准
	apiKey := &service.APIKey{Group: &service.Group{Platform: service.PlatformComposite}}
	require.Equal(t, service.PlatformGrokSearch, openAICompatibleRequestPlatform(ctx, apiKey))
}

// TestOpenAICompatibleRequestPlatform_CompositeResolvedRegression 验证 composite 场景下
// 现有平台的归属不被破坏：grok 返回自身，其他平台归为 PlatformOpenAI。
func TestOpenAICompatibleRequestPlatform_CompositeResolvedRegression(t *testing.T) {
	ctxGrok := service.WithResolvedTargetPlatform(context.Background(), service.PlatformGrok)
	require.Equal(t, service.PlatformGrok, openAICompatibleRequestPlatform(ctxGrok, nil))

	ctxOpenAI := service.WithResolvedTargetPlatform(context.Background(), service.PlatformOpenAI)
	require.Equal(t, service.PlatformOpenAI, openAICompatibleRequestPlatform(ctxOpenAI, nil))

	ctxGemini := service.WithResolvedTargetPlatform(context.Background(), service.PlatformGemini)
	require.Equal(t, service.PlatformOpenAI, openAICompatibleRequestPlatform(ctxGemini, nil))
}
