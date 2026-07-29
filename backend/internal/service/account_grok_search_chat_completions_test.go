package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestAccount_IsGrokSearchChatCompletionsEnabled 验证 grok_search 账号的 chat completions
// 桥接开关：仅 PlatformGrokSearch 生效；字段缺失/非 bool/关闭时返回 false；开启时返回 true。
// 默认关闭——引导用户改用 /v1/responses 端点。
func TestAccount_IsGrokSearchChatCompletionsEnabled(t *testing.T) {
	t.Run("grok_search 开启", func(t *testing.T) {
		account := &Account{
			Platform: PlatformGrokSearch,
			Type:     AccountTypeAPIKey,
			Extra: map[string]any{
				GrokSearchChatCompletionsExtraKey: true,
			},
		}
		require.True(t, account.IsGrokSearchChatCompletionsEnabled())
	})

	t.Run("grok_search 显式关闭", func(t *testing.T) {
		account := &Account{
			Platform: PlatformGrokSearch,
			Type:     AccountTypeAPIKey,
			Extra: map[string]any{
				GrokSearchChatCompletionsExtraKey: false,
			},
		}
		require.False(t, account.IsGrokSearchChatCompletionsEnabled())
	})

	t.Run("grok_search 字段缺失默认关闭", func(t *testing.T) {
		account := &Account{
			Platform: PlatformGrokSearch,
			Type:     AccountTypeAPIKey,
			Extra:    map[string]any{},
		}
		require.False(t, account.IsGrokSearchChatCompletionsEnabled())
	})

	t.Run("grok_search Extra 为 nil 默认关闭", func(t *testing.T) {
		account := &Account{
			Platform: PlatformGrokSearch,
			Type:     AccountTypeAPIKey,
		}
		require.False(t, account.IsGrokSearchChatCompletionsEnabled())
	})

	t.Run("grok_search 非 bool 类型默认关闭", func(t *testing.T) {
		account := &Account{
			Platform: PlatformGrokSearch,
			Type:     AccountTypeAPIKey,
			Extra: map[string]any{
				GrokSearchChatCompletionsExtraKey: "true",
			},
		}
		require.False(t, account.IsGrokSearchChatCompletionsEnabled())
	})

	t.Run("非 grok_search 平台始终关闭", func(t *testing.T) {
		grok := &Account{
			Platform: PlatformGrok,
			Type:     AccountTypeOAuth,
			Extra: map[string]any{
				GrokSearchChatCompletionsExtraKey: true,
			},
		}
		require.False(t, grok.IsGrokSearchChatCompletionsEnabled())

		openai := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Extra: map[string]any{
				GrokSearchChatCompletionsExtraKey: true,
			},
		}
		require.False(t, openai.IsGrokSearchChatCompletionsEnabled())
	})

	t.Run("nil receiver 安全返回 false", func(t *testing.T) {
		var account *Account
		require.False(t, account.IsGrokSearchChatCompletionsEnabled())
	})
}
