package service

import (
	"encoding/json"
	"testing"
)

// splitGrokSearchEffortSuffix 把「真模型名 + effort 后缀」拆成 base 与 effort，
// 让只能填模型名的客户端（如 smart Research）也能控制 multi-agent 协同强度。
// 本测试覆盖：四档后缀、大小写不敏感、从长到短匹配（-high 不误吃 -xhigh）、
// 无后缀、非法档（none/minimal 不作为后缀）、max→xhigh、整串即后缀的边界。

func TestSplitGrokSearchEffortSuffix(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		wantBase    string
		wantEffort  string
		wantStriped bool // 是否发生了剥离（用于显式断言）
	}{
		{
			name:        "xhigh 后缀",
			model:       "grok-4.20-multi-agent-0309-xhigh",
			wantBase:    "grok-4.20-multi-agent-0309",
			wantEffort:  "xhigh",
			wantStriped: true,
		},
		{
			name:        "high 后缀",
			model:       "grok-4.20-multi-agent-0309-high",
			wantBase:    "grok-4.20-multi-agent-0309",
			wantEffort:  "high",
			wantStriped: true,
		},
		{
			name:        "medium 后缀",
			model:       "grok-4.20-multi-agent-0309-medium",
			wantBase:    "grok-4.20-multi-agent-0309",
			wantEffort:  "medium",
			wantStriped: true,
		},
		{
			name:        "low 后缀",
			model:       "grok-4.20-multi-agent-0309-low",
			wantBase:    "grok-4.20-multi-agent-0309",
			wantEffort:  "low",
			wantStriped: true,
		},
		{
			name:        "大小写不敏感：XHIGH",
			model:       "grok-4.20-multi-agent-0309-XHIGH",
			wantBase:    "grok-4.20-multi-agent-0309", // base 保留原大小写前缀
			wantEffort:  "xhigh",
			wantStriped: true,
		},
		{
			name:        "大小写不敏感：High",
			model:       "grok-4.20-multi-agent-0309-High",
			wantBase:    "grok-4.20-multi-agent-0309",
			wantEffort:  "high",
			wantStriped: true,
		},
		{
			name:        "max 后缀映射 xhigh",
			model:       "grok-4.20-multi-agent-0309-max",
			wantBase:    "grok-4.20-multi-agent-0309",
			wantEffort:  "xhigh",
			wantStriped: true,
		},
		{
			name:        "-high 不应误吃 -xhigh（从长到短匹配）",
			model:       "grok-4.20-multi-agent-0309-xhigh",
			wantBase:    "grok-4.20-multi-agent-0309",
			wantEffort:  "xhigh",
			wantStriped: true,
		},
		{
			name:        "无后缀：原样返回",
			model:       "grok-4.20-multi-agent-0309",
			wantBase:    "grok-4.20-multi-agent-0309",
			wantEffort:  "",
			wantStriped: false,
		},
		{
			name:        "none 不作为后缀支持",
			model:       "grok-4.20-multi-agent-0309-none",
			wantBase:    "grok-4.20-multi-agent-0309-none",
			wantEffort:  "",
			wantStriped: false,
		},
		{
			name:        "minimal 不作为后缀支持（normalize 虽识别但不纳入后缀白名单）",
			model:       "grok-4.20-multi-agent-0309-minimal",
			wantBase:    "grok-4.20-multi-agent-0309-minimal",
			wantEffort:  "",
			wantStriped: false,
		},
		{
			name:        "整串即后缀：base 空，不剥离",
			model:       "-xhigh",
			wantBase:    "-xhigh",
			wantEffort:  "",
			wantStriped: false,
		},
		{
			name:        "整串即后缀：-low",
			model:       "-low",
			wantBase:    "-low",
			wantEffort:  "",
			wantStriped: false,
		},
		{
			name:        "非合法后缀（如 -ultra）",
			model:       "grok-4.20-multi-agent-0309-ultra",
			wantBase:    "grok-4.20-multi-agent-0309-ultra",
			wantEffort:  "",
			wantStriped: false,
		},
		{
			name:        "空串",
			model:       "",
			wantBase:    "",
			wantEffort:  "",
			wantStriped: false,
		},
		{
			name:        "仅空白",
			model:       "   ",
			wantBase:    "",
			wantEffort:  "",
			wantStriped: false,
		},
		{
			name:        "带前后空白应被 trim",
			model:       "  grok-4.20-multi-agent-0309-xhigh  ",
			wantBase:    "grok-4.20-multi-agent-0309",
			wantEffort:  "xhigh",
			wantStriped: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base, effort := splitGrokSearchEffortSuffix(tt.model)
			if base != tt.wantBase {
				t.Fatalf("base = %q, want %q", base, tt.wantBase)
			}
			if effort != tt.wantEffort {
				t.Fatalf("effort = %q, want %q", effort, tt.wantEffort)
			}
			// 剥离发生 ⇔ effort 非空（且 base 与原模型不同）
			stripped := effort != ""
			if stripped != tt.wantStriped {
				t.Fatalf("stripped = %v, want %v", stripped, tt.wantStriped)
			}
		})
	}
}

// 额外显式验证「-high 不误吃 -xhigh」：-xhigh 剥离后 base 末尾绝不能残留 -x。
func TestSplitGrokSearchEffortSuffix_XhighNotEatenByHigh(t *testing.T) {
	base, effort := splitGrokSearchEffortSuffix("grok-4.20-multi-agent-0309-xhigh")
	if effort != "xhigh" {
		t.Fatalf("effort = %q, want xhigh（应优先匹配更长的 -xhigh）", effort)
	}
	if base == "grok-4.20-multi-agent-0309-x" {
		t.Fatalf("base 错误地被 -high 剥离为 ...-x，说明未按从长到短匹配：base=%q", base)
	}
	if base != "grok-4.20-multi-agent-0309" {
		t.Fatalf("base = %q, want grok-4.20-multi-agent-0309", base)
	}
}

// normalizeGrokSearchRequestBody 的 reasoning.effort 优先级测试：
//   - 客户端显式 effort > preferredEffort（后缀剥离值）> 默认 medium。
func TestNormalizeGrokSearchRequestBody_EffortPriority(t *testing.T) {
	baseBody := func() map[string]any {
		return map[string]any{
			"model": "grok-4.20-multi-agent-0309",
			"input": []map[string]any{
				{"role": "user", "content": []map[string]any{{"type": "text", "text": "hi"}}},
			},
			"stream": false,
		}
	}

	t.Run("客户端不带 effort + preferredEffort=xhigh → xhigh", func(t *testing.T) {
		body, _ := json.Marshal(baseBody())
		out, err := normalizeGrokSearchRequestBody(body, "grok-4.20-multi-agent-0309", "xhigh")
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := reasoningEffort(t, payload); got != "xhigh" {
			t.Fatalf("reasoning.effort = %q, want xhigh", got)
		}
		if got, _ := payload["model"].(string); got != "grok-4.20-multi-agent-0309" {
			t.Fatalf("model = %q, want 真模型名", got)
		}
	})

	t.Run("客户端显式 low + preferredEffort=xhigh → low（客户端优先）", func(t *testing.T) {
		req := baseBody()
		req["reasoning"] = map[string]any{"effort": "low"}
		body, _ := json.Marshal(req)
		out, err := normalizeGrokSearchRequestBody(body, "grok-4.20-multi-agent-0309", "xhigh")
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := reasoningEffort(t, payload); got != "low" {
			t.Fatalf("reasoning.effort = %q, want low（客户端显式值应优先于后缀剥离值）", got)
		}
	})

	t.Run("客户端不带 effort + preferredEffort 空 → medium（默认兜底）", func(t *testing.T) {
		body, _ := json.Marshal(baseBody())
		out, err := normalizeGrokSearchRequestBody(body, "grok-4.20-multi-agent-0309", "")
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := reasoningEffort(t, payload); got != "medium" {
			t.Fatalf("reasoning.effort = %q, want medium", got)
		}
	})

	t.Run("客户端带非法 effort + preferredEffort=xhigh → xhigh（客户端非法值回退到后缀值）", func(t *testing.T) {
		req := baseBody()
		req["reasoning"] = map[string]any{"effort": "ultra"} // 非法档
		body, _ := json.Marshal(req)
		out, err := normalizeGrokSearchRequestBody(body, "grok-4.20-multi-agent-0309", "xhigh")
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := reasoningEffort(t, payload); got != "xhigh" {
			t.Fatalf("reasoning.effort = %q, want xhigh（客户端非法 effort 应回退到后缀剥离值）", got)
		}
	})

	t.Run("max 后缀剥离值同样生效（preferredEffort=max → xhigh）", func(t *testing.T) {
		body, _ := json.Marshal(baseBody())
		out, err := normalizeGrokSearchRequestBody(body, "grok-4.20-multi-agent-0309", "max")
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got := reasoningEffort(t, payload); got != "xhigh" {
			t.Fatalf("reasoning.effort = %q, want xhigh（max 应被 normalize 成 xhigh）", got)
		}
	})
}

// TestSplitAndNormalize_EquivalentToForwardPath 验证「split 后缀 + normalize 注入」
// 组合在 forwardGrokSearch 转发路径中的等价语义：最终 body 的 model 是真模型名、
// reasoning.effort 是后缀档位。无需 mock 整条转发（HTTP/gin），用 split + normalize
// 组合即可覆盖转发路径中 effort 后缀处理的全部逻辑。
func TestSplitAndNormalize_EquivalentToForwardPath(t *testing.T) {
	cases := []struct {
		name         string
		inputModel   string // 客户端填的（带后缀的）模型名，等价于 forwardGrokSearch 的 originalModel
		wantUpstream string // 期望发给上游的真模型名
		wantEffort   string // 期望注入的 reasoning.effort
	}{
		{"multi-agent xhigh", "grok-4.20-multi-agent-0309-xhigh", "grok-4.20-multi-agent-0309", "xhigh"},
		{"multi-agent high", "grok-4.20-multi-agent-0309-high", "grok-4.20-multi-agent-0309", "high"},
		{"multi-agent low", "grok-4.20-multi-agent-0309-low", "grok-4.20-multi-agent-0309", "low"},
		{"multi-agent medium", "grok-4.20-multi-agent-0309-medium", "grok-4.20-multi-agent-0309", "medium"},
		{"multi-agent max→xhigh", "grok-4.20-multi-agent-0309-max", "grok-4.20-multi-agent-0309", "xhigh"},
		{"无后缀保持原样且默认 medium", "grok-4.20-multi-agent-0309", "grok-4.20-multi-agent-0309", "medium"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 等价于 forwardGrokSearch 中的处理：
			//   upstreamModel = originalModel（假设 account 无 model_mapping）
			//   baseModel, effortFromSuffix = splitGrokSearchEffortSuffix(upstreamModel)
			//   normalizeGrokSearchRequestBody(body, baseModel, effortFromSuffix)
			upstreamModel := tc.inputModel // account 无 mapping 时回退到 originalModel
			baseModel, effortFromSuffix := splitGrokSearchEffortSuffix(upstreamModel)
			req := map[string]any{
				"model": baseModel,
				"input": []map[string]any{
					{"role": "user", "content": []map[string]any{{"type": "text", "text": "搜一下今天的新闻"}}},
				},
				"stream": false,
			}
			body, _ := json.Marshal(req)
			out, err := normalizeGrokSearchRequestBody(body, baseModel, effortFromSuffix)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(out, &payload); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got, _ := payload["model"].(string); got != tc.wantUpstream {
				t.Fatalf("payload.model = %q, want %q", got, tc.wantUpstream)
			}
			if got := reasoningEffort(t, payload); got != tc.wantEffort {
				t.Fatalf("reasoning.effort = %q, want %q", got, tc.wantEffort)
			}
		})
	}
}

// reasoningEffort 从归一化后的 payload 中读取 reasoning.effort，失败时 fatal。
func reasoningEffort(t *testing.T, payload map[string]any) string {
	t.Helper()
	reasoning, ok := payload["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("payload 缺少 reasoning 对象: %#v", payload["reasoning"])
	}
	effort, _ := reasoning["effort"].(string)
	return effort
}
