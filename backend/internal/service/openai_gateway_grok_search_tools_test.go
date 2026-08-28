package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 本文件覆盖 grok_search 请求体归一化的对齐上游增强项（任务 08-27-align-grok-search-upstream-console）：
//   - tools：view_image 撞名兜底（AC1/AC2）、web_search enable_image_search 透传（AC6）、
//     x_search from_date/to_date 严格校验透传（AC5）、七种原生 xAI 工具类型透传（AC7）。
//   - tool_choice 收紧策略矩阵（AC3）。
//   - input patch 的 reasoning item 补 type:"reasoning_text"（AC8）。
//   - reasoning.effort auto 档透传（AC9）。
//   - max_output_tokens 缺省回正 1_000_000（AC4）。
//   - 429 瞬时频率限制精确冷却：Retry-After 头 / body "Resets in" 解析 + clamp（AC12）。
//
// 测试主走 normalizeGrokSearchRequestBody 端到端路径（验证 normalizeGrokSearchTools 返回值 →
// normalizeGrokSearchToolChoice 的接线），429 冷却断言复用 statuscode_test 的 runtime block 手法。

// grokSearchBuildNormalizedBody 构造基础请求体（可 mutate 注入客户端字段），走完整归一化链，
// 返回归一化后的 payload。
func grokSearchBuildNormalizedBody(t *testing.T, mutate func(payload map[string]any)) map[string]any {
	t.Helper()
	payload := map[string]any{
		"model":  "grok-4.20-multi-agent-0309",
		"input":  []map[string]any{{"role": "user", "content": []map[string]any{{"type": "text", "text": "hi"}}}},
		"stream": false,
	}
	if mutate != nil {
		mutate(payload)
	}
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	out, err := normalizeGrokSearchRequestBody(body, "grok-4.20-multi-agent-0309", "")
	require.NoError(t, err)
	var normalized map[string]any
	require.NoError(t, json.Unmarshal(out, &normalized))
	return normalized
}

// grokSearchFindTool 从归一化后的 payload.tools 中取第一个指定 type 的工具。
func grokSearchFindTool(t *testing.T, payload map[string]any, toolType string) map[string]any {
	t.Helper()
	for _, raw := range grokSearchToolsList(t, payload) {
		if typeName, _ := raw["type"].(string); typeName == toolType {
			return raw
		}
	}
	t.Fatalf("tools 中未找到 type=%s 的工具: %#v", toolType, payload["tools"])
	return nil
}

// grokSearchToolsList 取归一化后的 payload.tools（均为 map 断言）。
func grokSearchToolsList(t *testing.T, payload map[string]any) []map[string]any {
	t.Helper()
	rawTools, ok := payload["tools"].([]any)
	require.True(t, ok, "payload.tools 应为数组: %#v", payload["tools"])
	tools := make([]map[string]any, 0, len(rawTools))
	for _, raw := range rawTools {
		tool, ok := raw.(map[string]any)
		require.True(t, ok, "tool 应为对象: %#v", raw)
		tools = append(tools, tool)
	}
	return tools
}

// === tools-viewimage（AC1/AC2）：客户端带 view_image function 时强制关闭 web_search 图片理解 ===

// TestNormalizeGrokSearchTools_ViewImageCollision 验证 view_image 撞名兜底：
// xAI 服务端在 web_search 开启 image understanding 时会附带同名 view_image 工具，
// 客户端自带 view_image function 时撞名整单被 mgw 拒绝，必须强制关闭（显式 true 也忽略）。
func TestNormalizeGrokSearchTools_ViewImageCollision(t *testing.T) {
	t.Run("客户端带 view_image：显式 enable_image_understanding=true 被强制忽略", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["tools"] = []any{
				map[string]any{"type": "function", "name": "view_image", "description": "inspect local file"},
				map[string]any{"type": "web_search", "enable_image_understanding": true},
			}
		})
		webSearch := grokSearchFindTool(t, normalized, "web_search")
		require.Equal(t, false, webSearch["enable_image_understanding"],
			"撞名时 web_search.enable_image_understanding 必须为 false（客户端显式 true 也忽略）")
	})

	t.Run("客户端带 view_image 且未带 web_search：注入的 web_search 同样关闭", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["tools"] = []any{
				map[string]any{"type": "function", "name": "view_image"},
			}
		})
		webSearch := grokSearchFindTool(t, normalized, "web_search")
		require.Equal(t, false, webSearch["enable_image_understanding"],
			"注入兜底的 web_search 也受撞名约束（否则同样撞名整单被拒）")
	})

	t.Run("无 view_image：显式 true 被尊重（回归保护）", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["tools"] = []any{
				map[string]any{"type": "web_search", "enable_image_understanding": true},
			}
		})
		webSearch := grokSearchFindTool(t, normalized, "web_search")
		require.Equal(t, true, webSearch["enable_image_understanding"])
	})

	t.Run("无 view_image：缺省默认 true（回归保护）", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["tools"] = []any{
				map[string]any{"type": "web_search"},
			}
		})
		webSearch := grokSearchFindTool(t, normalized, "web_search")
		require.Equal(t, true, webSearch["enable_image_understanding"])
	})

	t.Run("不带任何 tools：注入的 web_search 默认 true（回归保护）", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, nil)
		webSearch := grokSearchFindTool(t, normalized, "web_search")
		require.Equal(t, true, webSearch["enable_image_understanding"])
	})

	t.Run("name=view_image 但非 function 类型不算撞名", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["tools"] = []any{
				map[string]any{"type": "image_generation", "name": "view_image"},
			}
		})
		webSearch := grokSearchFindTool(t, normalized, "web_search")
		require.Equal(t, true, webSearch["enable_image_understanding"],
			"撞名兜底只认 type=function 的 view_image")
	})
}

// TestHasGrokSearchFunctionTool 单元测撞名检测 helper：type/name 大小写不敏感 + TrimSpace。
func TestHasGrokSearchFunctionTool(t *testing.T) {
	tools := []any{
		map[string]any{"type": "Function", "name": " ViewImage "},
		map[string]any{"type": "web_search"},
	}
	require.True(t, hasGrokSearchFunctionTool(tools, "viewimage"))
	require.False(t, hasGrokSearchFunctionTool(tools, "view_image"))
	require.False(t, hasGrokSearchFunctionTool(tools, "web_search"),
		"非 function 类型即使 name 匹配也不算")
	require.False(t, hasGrokSearchFunctionTool(nil, "view_image"))
}

// === tools-image-search（AC6）：web_search 的 enable_image_search 布尔透传 ===

// TestNormalizeGrokSearchTools_EnableImageSearch 验证图片搜索开关透传：显式布尔才写键，
// 缺省/非布尔值不产生该键。
func TestNormalizeGrokSearchTools_EnableImageSearch(t *testing.T) {
	t.Run("显式 true 透传", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["tools"] = []any{map[string]any{"type": "web_search", "enable_image_search": true}}
		})
		webSearch := grokSearchFindTool(t, normalized, "web_search")
		require.Equal(t, true, webSearch["enable_image_search"])
	})

	t.Run("显式 false 透传", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["tools"] = []any{map[string]any{"type": "web_search", "enable_image_search": false}}
		})
		webSearch := grokSearchFindTool(t, normalized, "web_search")
		require.Equal(t, false, webSearch["enable_image_search"])
	})

	t.Run("未设不产生该键", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["tools"] = []any{map[string]any{"type": "web_search"}}
		})
		webSearch := grokSearchFindTool(t, normalized, "web_search")
		_, exists := webSearch["enable_image_search"]
		require.False(t, exists, "未显式传 enable_image_search 时不应产生该键")
	})

	t.Run("非布尔值丢弃", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["tools"] = []any{map[string]any{"type": "web_search", "enable_image_search": "true"}}
		})
		webSearch := grokSearchFindTool(t, normalized, "web_search")
		_, exists := webSearch["enable_image_search"]
		require.False(t, exists, "字符串等非布尔值不透传")
	})
}

// === xsearch-dates（AC5）：x_search 的 from_date/to_date 严格 YYYY-MM-DD 校验透传 ===

// TestNormalizeGrokSearchTools_XSearchDates 验证时间范围透传规则：
// 严格 YYYY-MM-DD（parse 后回写比对全等）才透传；非法/空串/非字符串丢弃；from > to 成对丢弃。
func TestNormalizeGrokSearchTools_XSearchDates(t *testing.T) {
	xSearchWith := func(fields map[string]any) map[string]any {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			tool := map[string]any{"type": "x_search"}
			for k, v := range fields {
				tool[k] = v
			}
			p["tools"] = []any{tool}
		})
		return grokSearchFindTool(t, normalized, "x_search")
	}

	t.Run("合法 from/to 透传", func(t *testing.T) {
		xSearch := xSearchWith(map[string]any{"from_date": "2026-01-01", "to_date": "2026-02-01"})
		require.Equal(t, "2026-01-01", xSearch["from_date"])
		require.Equal(t, "2026-02-01", xSearch["to_date"])
	})

	t.Run("from > to 成对丢弃（避免上游 400）", func(t *testing.T) {
		xSearch := xSearchWith(map[string]any{"from_date": "2026-03-01", "to_date": "2026-02-01"})
		_, hasFrom := xSearch["from_date"]
		_, hasTo := xSearch["to_date"]
		require.False(t, hasFrom, "倒序区间 from_date 应被丢弃")
		require.False(t, hasTo, "倒序区间 to_date 应被丢弃")
	})

	t.Run("from == to 保留（闭区间相等合法）", func(t *testing.T) {
		xSearch := xSearchWith(map[string]any{"from_date": "2026-02-01", "to_date": "2026-02-01"})
		require.Equal(t, "2026-02-01", xSearch["from_date"])
		require.Equal(t, "2026-02-01", xSearch["to_date"])
	})

	t.Run("非法月份丢弃", func(t *testing.T) {
		xSearch := xSearchWith(map[string]any{"from_date": "2026-13-01"})
		_, exists := xSearch["from_date"]
		require.False(t, exists)
	})

	t.Run("紧凑格式丢弃（非严格 YYYY-MM-DD）", func(t *testing.T) {
		xSearch := xSearchWith(map[string]any{"from_date": "20260101"})
		_, exists := xSearch["from_date"]
		require.False(t, exists)
	})

	t.Run("空串丢弃", func(t *testing.T) {
		xSearch := xSearchWith(map[string]any{"from_date": ""})
		_, exists := xSearch["from_date"]
		require.False(t, exists)
	})

	t.Run("非字符串丢弃", func(t *testing.T) {
		xSearch := xSearchWith(map[string]any{"from_date": 20260101})
		_, exists := xSearch["from_date"]
		require.False(t, exists)
	})

	t.Run("仅 from 合法保留", func(t *testing.T) {
		xSearch := xSearchWith(map[string]any{"from_date": "2026-01-01"})
		require.Equal(t, "2026-01-01", xSearch["from_date"])
		_, hasTo := xSearch["to_date"]
		require.False(t, hasTo, "未设 to_date 不应产生该键")
	})

	t.Run("from 合法 to 非法：保留 from 丢弃 to", func(t *testing.T) {
		xSearch := xSearchWith(map[string]any{"from_date": "2026-01-01", "to_date": "tomorrow"})
		require.Equal(t, "2026-01-01", xSearch["from_date"])
		_, hasTo := xSearch["to_date"]
		require.False(t, hasTo)
	})

	t.Run("不设日期行为不变（无两键）", func(t *testing.T) {
		xSearch := xSearchWith(nil)
		_, hasFrom := xSearch["from_date"]
		_, hasTo := xSearch["to_date"]
		require.False(t, hasFrom)
		require.False(t, hasTo)
	})
}

// === native-tools（AC7）：七种原生 xAI 工具类型原样透传 ===

// TestNormalizeGrokSearchTools_NativeToolTypes 验证原生工具类型（mcp/shell/image_generation/
// collections_search/file_search/code_execution/code_interpreter）整对象原样保留并计数为客户端工具。
func TestNormalizeGrokSearchTools_NativeToolTypes(t *testing.T) {
	nativeTools := []any{
		map[string]any{"type": "mcp", "server_label": "deepwiki", "server_url": "https://mcp.example/s"},
		map[string]any{"type": "shell", "command": "ls -la"},
		map[string]any{"type": "image_generation", "size": "1024x1024"},
		map[string]any{"type": "collections_search", "collection": "docs"},
		map[string]any{"type": "file_search", "vector_store_ids": []any{"vs_1"}},
		map[string]any{"type": "code_execution", "sandbox": "secure"},
		map[string]any{"type": "code_interpreter", "format": "python"},
	}
	normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
		p["tools"] = append([]any{}, nativeTools...)
	})

	tools := grokSearchToolsList(t, normalized)
	require.Len(t, tools, len(nativeTools)+2, "七个原生工具 + 注入的 web_search/x_search")
	for _, expected := range nativeTools {
		expectedType, _ := expected.(map[string]any)["type"].(string)
		found := false
		for _, tool := range tools {
			if typeName, _ := tool["type"].(string); typeName == expectedType {
				found = true
				require.Equal(t, expected, tool,
					"原生工具 %s 应整对象原样透传（不裁剪字段）", expectedType)
			}
		}
		require.True(t, found, "原生工具 %s 被丢弃", expectedType)
	}

	// retainedClientTools 传导：原生工具应让 tool_choice=required 放行（见下）。
	t.Run("原生工具使 tool_choice required 放行", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["tools"] = []any{map[string]any{"type": "shell", "command": "ls"}}
			p["tool_choice"] = "required"
		})
		require.Equal(t, "required", normalized["tool_choice"],
			"原生 xAI 工具计为客户端工具，required 应放行")
	})
}

// === tool-choice（AC3）：收紧策略矩阵 ===

// TestNormalizeGrokSearchToolChoice 验证 tool_choice 收紧规则（对齐上游 normalizeConsoleToolChoice）：
// required / function 对象仅在保留了客户端工具（function 或原生类型）时放行，否则降级 auto。
func TestNormalizeGrokSearchToolChoice(t *testing.T) {
	clientFunction := func(name string) map[string]any {
		return map[string]any{"type": "function", "name": name, "description": "client tool"}
	}
	tests := []struct {
		name       string
		tools      []any // nil 表示不带 tools 键
		toolChoice any   // nil 表示不带 tool_choice 键
		want       any
	}{
		{"required + function 工具 → 保留", []any{clientFunction("foo")}, "required", "required"},
		{"required + 原生工具 → 保留", []any{map[string]any{"type": "code_execution"}}, "required", "required"},
		{"required 无客户端工具 → auto", nil, "required", "auto"},
		{"required 仅 web_search（非客户端工具）→ auto", []any{map[string]any{"type": "web_search"}}, "required", "auto"},
		{"function 对象 + 工具 → 标准形态", []any{clientFunction("foo")},
			map[string]any{"type": "function", "name": "foo"},
			map[string]any{"type": "function", "name": "foo"}},
		{"function 对象无客户端工具 → auto", nil,
			map[string]any{"type": "function", "name": "foo"}, "auto"},
		{"function 对象嵌套 name", []any{clientFunction("bar")},
			map[string]any{"type": "function", "function": map[string]any{"name": "bar"}},
			map[string]any{"type": "function", "name": "bar"}},
		{"function 对象 name 空 → auto", []any{clientFunction("foo")},
			map[string]any{"type": "function"}, "auto"},
		{"function 对象嵌套 name 空 → auto", []any{clientFunction("foo")},
			map[string]any{"type": "function", "function": map[string]any{}}, "auto"},
		{"非 function 对象 → auto", []any{clientFunction("foo")},
			map[string]any{"type": "web_search"}, "auto"},
		{"none 归一小写", nil, "None", "none"},
		{"auto 保留", nil, "auto", "auto"},
		{"未知串 → auto", []any{clientFunction("foo")}, "forced", "auto"},
		{"非字符串非对象 → auto", []any{clientFunction("foo")}, 42, "auto"},
		{"缺省 → auto（回归保护）", nil, nil, "auto"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
				if tt.tools != nil {
					p["tools"] = tt.tools
				}
				if tt.toolChoice != nil {
					p["tool_choice"] = tt.toolChoice
				}
			})
			require.Equal(t, tt.want, normalized["tool_choice"])
		})
	}

	t.Run("tools 缺失 → 删除 tool_choice（防御分支，直接单元测）", func(t *testing.T) {
		payload := map[string]any{"tool_choice": "auto"}
		normalizeGrokSearchToolChoice(payload, false)
		_, exists := payload["tool_choice"]
		require.False(t, exists, "tools 不存在时 tool_choice 应被删除")
	})
}

// === reasoning-patch（AC8）：reasoning item 无 type 文本 part 补 reasoning_text ===

// TestPatchGrokSearchInput_ReasoningContent 验证 reasoning item 的 content patch：
// 无 type 但有 text 的 part 补 type:"reasoning_text"（多轮回放时客户端回传的 part 缺 type 会被上游拒），
// 其它 item 的处理不受影响。
func TestPatchGrokSearchInput_ReasoningContent(t *testing.T) {
	reasoningPart := func(t *testing.T, payload map[string]any, itemIndex int) map[string]any {
		t.Helper()
		items, ok := payload["input"].([]any)
		require.True(t, ok)
		item, ok := items[itemIndex].(map[string]any)
		require.True(t, ok)
		content, ok := item["content"].([]any)
		require.True(t, ok)
		part, ok := content[0].(map[string]any)
		require.True(t, ok)
		return part
	}

	t.Run("无 type 有 text → 补 reasoning_text", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["input"] = []any{
				map[string]any{"type": "reasoning", "content": []any{map[string]any{"text": "thinking..."}}},
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
			}
		})
		part := reasoningPart(t, normalized, 0)
		require.Equal(t, "reasoning_text", part["type"])
		require.Equal(t, "thinking...", part["text"], "补 type 不应改动 text 内容")
	})

	t.Run("已有 type 不改写", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["input"] = []any{
				map[string]any{"type": "reasoning", "content": []any{map[string]any{"type": "summary_text", "text": "s"}}},
			}
		})
		part := reasoningPart(t, normalized, 0)
		require.Equal(t, "summary_text", part["type"], "已有 type 的 part 不改写")
	})

	t.Run("无 text 不补 type", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["input"] = []any{
				map[string]any{"type": "reasoning", "content": []any{map[string]any{"summary": "no text field"}}},
			}
		})
		part := reasoningPart(t, normalized, 0)
		_, hasType := part["type"]
		require.False(t, hasType, "无 text 的 part 不应被补 type")
	})

	t.Run("reasoning item 不做 text→input_text 改写", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["input"] = []any{
				map[string]any{"type": "reasoning", "content": []any{map[string]any{"type": "text", "text": "why"}}},
			}
		})
		part := reasoningPart(t, normalized, 0)
		require.Equal(t, "text", part["type"], "reasoning content 内已有 type 的 part 保持原 type（不改写为 input_text）")
	})

	t.Run("普通 message item 的 text patch 不受影响（回归保护）", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["input"] = []any{
				map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
			}
		})
		part := reasoningPart(t, normalized, 0)
		require.Equal(t, "input_text", part["type"])
	})
}

// === effort-auto（AC9）：reasoning.effort auto 档原样透传 ===

// TestNormalizeGrokSearchEffort_Auto 验证客户端显式 effort:"auto" 原样透传（不落 medium 兜底），
// 不传 effort 仍兜底 medium；模型名 -auto 后缀不触发剥离。
func TestNormalizeGrokSearchEffort_Auto(t *testing.T) {
	t.Run("客户端显式 auto 原样透传", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["reasoning"] = map[string]any{"effort": "auto"}
		})
		require.Equal(t, "auto", reasoningEffort(t, normalized))
	})

	t.Run("Auto 大小写不敏感归一为小写 auto", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["reasoning"] = map[string]any{"effort": "Auto"}
		})
		require.Equal(t, "auto", reasoningEffort(t, normalized))
	})

	t.Run("不传 effort 仍兜底 medium（回归保护）", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, nil)
		require.Equal(t, "medium", reasoningEffort(t, normalized))
	})

	t.Run("模型名 -auto 后缀不剥离（后缀表不含 -auto）", func(t *testing.T) {
		base, effort := splitGrokSearchEffortSuffix("grok-4.20-multi-agent-0309-auto")
		require.Equal(t, "grok-4.20-multi-agent-0309-auto", base)
		require.Equal(t, "", effort)
	})
}

// === max-tokens（AC4）：缺省回正 1_000_000 ===

// TestNormalizeGrokSearchRequestBody_MaxOutputTokens 验证缺省补上游 multi-agent 实际值 1_000_000，
// 客户端显式值不被覆盖。
func TestNormalizeGrokSearchRequestBody_MaxOutputTokens(t *testing.T) {
	t.Run("缺省补 1000000", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, nil)
		require.EqualValues(t, grokSearchDefaultMaxOutputTokens, normalized["max_output_tokens"])
		require.EqualValues(t, 1_000_000, normalized["max_output_tokens"],
			"常量应为上游 catalog multi-agent 实际值 1_000_000（历史抄录 2_000_000 已回正）")
	})

	t.Run("显式值不被覆盖", func(t *testing.T) {
		normalized := grokSearchBuildNormalizedBody(t, func(p map[string]any) {
			p["max_output_tokens"] = 500
		})
		require.EqualValues(t, 500, normalized["max_output_tokens"])
	})
}

// === retry-after（AC12）：429 瞬时频率限制精确冷却 ===

// TestGrokSearchRetryAfterFromHeader 验证 Retry-After 头解析（整数秒 / HTTP 日期）。
func TestGrokSearchRetryAfterFromHeader(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"整数秒", "90", 90 * time.Second},
		{"带空白", " 120 ", 120 * time.Second},
		// HTTP 日期须用 http.TimeFormat（"…GMT"）；time.RFC1123 在 UTC 时区产出 "…UTC" 后缀，
		// http.ParseTime 首选格式不认（真实 Retry-After 按 HTTP 规范带 GMT）。
		{"HTTP 日期（RFC1123）", now.Add(2 * time.Hour).UTC().Format(http.TimeFormat), 2 * time.Hour},
		{"过去的日期", now.Add(-1 * time.Hour).UTC().Format(http.TimeFormat), 0},
		{"零秒", "0", 0},
		{"负数", "-5", 0},
		{"非数字非日期", "soon", 0},
		{"空串", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, grokSearchRetryAfterFromHeader(tt.value, now))
		})
	}
}

// TestGrokSearchRetryAfterFromBody 验证 body "Resets in: 3m 4s" 形态解析（大小写不敏感、多段累加）。
func TestGrokSearchRetryAfterFromBody(t *testing.T) {
	tests := []struct {
		name string
		body string
		want time.Duration
	}{
		{"实测形态 3m 4s", `{"code":"resource-exhausted","error":"Too many requests. Resets in: 3m 4s"}`, 3*time.Minute + 4*time.Second},
		{"多段累加", `Resets in: 1d 2h 3m 4s`, 26*time.Hour + 3*time.Minute + 4*time.Second},
		{"大写 RESETS IN", `RESETS IN: 5m`, 5 * time.Minute},
		{"无 resets in 标记", `{"error":"rate limited"}`, 0},
		{"空 body", ``, 0},
		{"标记后无时长片段", `Resets in: soon`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, grokSearchRetryAfterFromBody([]byte(tt.body)))
		})
	}
}

// grokSearchRequireCooldown 断言 handleGrokSearchAccountUpstreamError 429 分支产出的
// 内存 runtime block 冷却时长（复用 statuscode_test 的手法）。
func grokSearchRequireCooldown(t *testing.T, accountID int64, headers http.Header, body string, want time.Duration) {
	t.Helper()
	svc := &OpenAIGatewayService{}
	account := grokSearchStatusTestAccount(accountID)
	svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, headers, []byte(body))
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(account), "429 普通限流应 block 账号")
	until, ok := grokSearchLoadRuntimeBlockUntil(t, svc, account.ID)
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(want), until, 5*time.Second,
		"冷却时长应为 %v", want)
}

// TestHandleGrokSearchAccountUpstreamError_429PreciseCooldown 验证 429 瞬时频率限制的精确冷却：
// Retry-After 头优先、body "Resets in" 次之，clamp [1min, 24h]，无信号 5min 兜底。
func TestHandleGrokSearchAccountUpstreamError_429PreciseCooldown(t *testing.T) {
	rateLimitBody := `{"code":"resource-exhausted","error":"Too many requests for team grok-dev. Requests per Second exceeded"}`

	t.Run("Retry-After 头秒数生效", func(t *testing.T) {
		h := http.Header{}
		h.Set("Retry-After", "120")
		grokSearchRequireCooldown(t, 201, h, rateLimitBody, 2*time.Minute)
	})

	t.Run("头值过短 clamp 到 1min", func(t *testing.T) {
		h := http.Header{}
		h.Set("Retry-After", "10")
		grokSearchRequireCooldown(t, 202, h, rateLimitBody, grokSearchRetryAfterMinCooldown)
	})

	t.Run("头值超大 clamp 到 24h", func(t *testing.T) {
		h := http.Header{}
		h.Set("Retry-After", "999999")
		grokSearchRequireCooldown(t, 203, h, rateLimitBody, grokSearchRetryAfterMaxCooldown)
	})

	t.Run("无头时 body Resets in 生效", func(t *testing.T) {
		grokSearchRequireCooldown(t, 204, http.Header{},
			`{"error":"Too many requests. Resets in: 3m 4s"}`, 3*time.Minute+4*time.Second)
	})

	t.Run("头优先于 body", func(t *testing.T) {
		h := http.Header{}
		h.Set("Retry-After", "60")
		grokSearchRequireCooldown(t, 205, h, `Resets in: 1h`, time.Minute)
	})

	t.Run("无精确信号维持 5min 兜底", func(t *testing.T) {
		grokSearchRequireCooldown(t, 206, http.Header{}, rateLimitBody, grokSearchRateLimitCooldown)
	})

	t.Run("免费额度耗尽仍 30d（优先于 Retry-After 头）", func(t *testing.T) {
		h := http.Header{}
		h.Set("Retry-After", "120")
		svc := &OpenAIGatewayService{}
		account := grokSearchStatusTestAccount(207)
		body := `{"code":"resource-exhausted","error":"Free usage quota exceeded. Purchase credits"}`
		svc.handleGrokSearchAccountUpstreamError(context.Background(), account, http.StatusTooManyRequests, h, []byte(body))
		require.True(t, svc.isOpenAIAccountRuntimeBlocked(account))
		until, ok := grokSearchLoadRuntimeBlockUntil(t, svc, account.ID)
		require.True(t, ok)
		require.WithinDuration(t, time.Now().Add(grokSearchFreeQuotaCooldown), until, 5*time.Second,
			"免费额度耗尽应保持 30d 长冷却，不受 Retry-After 头影响")
	})
}
