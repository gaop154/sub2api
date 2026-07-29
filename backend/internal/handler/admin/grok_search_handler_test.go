package admin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGrokSearchSSOImportAccountName 锁住 grok_search 批量导入的账号命名规则：
// 用户填了 name 就用 name（多 token 追加 #N），没填回落到默认名。
// 回归点：早期实现忽略前端 form.name，账号名恒为 "Grok Search SSO Account"。
func TestGrokSearchSSOImportAccountName(t *testing.T) {
	tests := []struct {
		name     string
		index    int
		total    int
		nameBase string
		want     string
	}{
		{"单 token 用用户填的名", 1, 1, "我的 grok 账号", "我的 grok 账号"},
		{"单 token 未填名回落默认", 1, 1, "", "Grok Search SSO Account"},
		{"多 token 用用户名追加序号 1", 1, 3, "团队号", "团队号 #1"},
		{"多 token 用用户名追加序号 2", 2, 3, "团队号", "团队号 #2"},
		{"多 token 未填名用默认追加序号", 1, 2, "", "Grok Search SSO Account #1"},
		{"用户名两侧空格被 trim", 1, 1, "  spaced  ", "spaced"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, grokSearchSSOImportAccountName(tt.index, tt.total, tt.nameBase))
		})
	}
}
