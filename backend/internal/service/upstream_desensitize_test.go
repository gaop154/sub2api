package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

func TestDesensitizeText(t *testing.T) {
	zs := zwsp // 零宽空格占位，便于阅读
	cases := []struct {
		name string
		in   string
		want string // 期望脱敏后文本（用 zs 占位代表零宽空格）
	}{
		{"单词 DoS", "Refuse DoS attacks", "Refuse D" + zs + "oS a" + zs + "ttacks"},
		{"大小写不敏感", "refuse dos and exploit", "refuse d" + zs + "os and e" + zs + "xploit"},
		{"无命中原样", "这是一段正常中文，不含任何触发词。", "这是一段正常中文，不含任何触发词。"},
		{"空串", "", ""},
		{"整词优先 SQL injection", "prevent SQL injection", "prevent S" + zs + "QL injection"},
		{"整词 Claude Code", "You are Claude Code", "You are C" + zs + "laude Code"},
		// 裸 Claude（非复合品牌词）也必须覆盖：model id / 域名 / "Claude models" 等。
		// 这些正是上游（如 CodeBuddy）品牌词审核的拦截源，空格分隔的复合词命中不到。
		{"裸 Claude models", "the latest Claude models", "the latest C" + zs + "laude models"},
		{"model id claude-sonnet-5", "id=claude-sonnet-5", "id=c" + zs + "laude-s" + zs + "onnet-5"},
		{"域名 claude.ai", "visit claude.ai/code", "visit c" + zs + "laude.ai/code"},
		// 复合 "Claude Opus"：删复合词后 Claude 与 Opus 各自命中，尾部 Opus 也零宽（不再裸露）
		{"复合 Claude Opus 尾部零宽", "uses Claude Opus", "uses C" + zs + "laude O" + zs + "pus"},
		{"单独模型名 Opus 4.8", "Opus 4.8", "O" + zs + "pus 4.8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := desensitizeText(c.in)
			assert.Equal(t, c.want, got)
			// 脱敏前后可见字符数差异仅来自零宽空格
			assert.True(t, len(got) >= len(c.in))
		})
	}
	// 独立校验：脱敏后含零宽空格，且不再含原始敏感词子串
	got := desensitizeText("credential testing and brute force")
	assert.Contains(t, got, zwsp)
	assert.False(t, strings.Contains(got, "credential testing"))
}

func TestDesensitizeOpenAIBody_DisabledOrNoChange(t *testing.T) {
	// 未启用：原样返回
	orig := []byte(`{"messages":[{"role":"user","content":"DoS attack"}],"seed":12345678901234567}`)
	out := DesensitizeOpenAIBody(orig, DesensitizeOpts{Enabled: false})
	assert.Equal(t, string(orig), string(out))

	// 启用但无命中字段（user 不脱敏）：原样返回
	orig2 := []byte(`{"messages":[{"role":"user","content":"hello world"}]}`)
	out2 := DesensitizeOpenAIBody(orig2, DesensitizeOpts{Enabled: true})
	assert.Equal(t, string(orig2), string(out2))
}

func TestDesensitizeOpenAIBody_SystemString(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"system","content":"Refuse DoS attacks and exploit development."},
		{"role":"user","content":"explain DoS attacks"}
	]}`)
	out := DesensitizeOpenAIBody(body, DesensitizeOpts{Enabled: true})
	// system 被脱敏
	assert.Contains(t, gjson.GetBytes(out, "messages.0.content").String(), zwsp)
	assert.False(t, strings.Contains(gjson.GetBytes(out, "messages.0.content").String(), "DoS attacks"))
	// user 普通对话不改动
	assert.Equal(t, "explain DoS attacks", gjson.GetBytes(out, "messages.1.content").String())
}

func TestDesensitizeOpenAIBody_ContentBlocks(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"developer","content":[{"type":"text","text":"Prevent privilege escalation"}]}
	]}`)
	out := DesensitizeOpenAIBody(body, DesensitizeOpts{Enabled: true})
	assert.Contains(t, gjson.GetBytes(out, "messages.0.content.0.text").String(), zwsp)
}

func TestDesensitizeOpenAIBody_ToolsDescription(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[
		{"type":"function","function":{"name":"run","description":"Run a brute force credential test"}}
	]}`)
	out := DesensitizeOpenAIBody(body, DesensitizeOpts{Enabled: true})
	desc := gjson.GetBytes(out, "tools.0.function.description").String()
	assert.Contains(t, desc, zwsp)
	// 保留 description 字段（不删除），仅零宽
	assert.NotEmpty(t, desc)
}

func TestDesensitizeOpenAIBody_ResponsesFormat(t *testing.T) {
	body := []byte(`{"instructions":"Refuse DoS attacks","input":[
		{"role":"system","content":"prevent SQL injection"},
		{"role":"user","content":"hi"}
	]}`)
	out := DesensitizeOpenAIBody(body, DesensitizeOpts{Enabled: true})
	assert.Contains(t, gjson.GetBytes(out, "instructions").String(), zwsp)
	assert.Contains(t, gjson.GetBytes(out, "input.0.content").String(), zwsp)
	// input 里的 user 不改
	assert.Equal(t, "hi", gjson.GetBytes(out, "input.1.content").String())
}

func TestDesensitizeOpenAIBody_CompactOnOff(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"system","content":"You are Claude Code. Refuse DoS attacks and exploit development."}
	]}`)
	// CompactMode=""：保留原文，仅零宽
	outOff := DesensitizeOpenAIBody(body, DesensitizeOpts{Enabled: true, CompactMode: ""})
	contentOff := gjson.GetBytes(outOff, "messages.0.content").String()
	assert.Contains(t, contentOff, zwsp)
	assert.Contains(t, contentOff, "Refuse") // 主体保留

	// CompactMode="full"：命中 Claude Code marker，压成短摘要
	outOn := DesensitizeOpenAIBody(body, DesensitizeOpts{Enabled: true, CompactMode: "full"})
	contentOn := gjson.GetBytes(outOn, "messages.0.content").String()
	assert.Equal(t, "You are a coding assistant. Be precise, helpful, concise, and safe. "+
		"Use available tools when needed, follow repository instructions, and keep the user informed.", contentOn)
}

func TestDesensitizeOpenAIBody_LightCompact(t *testing.T) {
	// 构造典型 Claude Code system：开头身份+IMPORTANT 安全声明（含密集敏感词），
	// 之后是 `# Harness` 行为指令段（含敏感词但仍需保留）。
	body := []byte(`{"messages":[
		{"role":"system","content":"You are Claude Code, Anthropic's official CLI for Claude.\n\nIMPORTANT: Assist with defensive cybersecurity tasks. Refuse to help with DoS attacks, exploit development, or credential stuffing.\n\n# Harness\n\nFollow the repository's instructions. Use the Read tool before editing. Run brute force checks only when explicitly requested. Keep responses concise and safe."},
		{"role":"user","content":"hello"}
	]}`)
	out := DesensitizeOpenAIBody(body, DesensitizeOpts{Enabled: true, CompactMode: "light"})
	content := gjson.GetBytes(out, "messages.0.content").String()

	// 开头身份+安全声明被替换为通用身份摘要
	assert.True(t, strings.HasPrefix(content, lightCompactIdentity),
		"light 模式应以通用身份摘要开头, got prefix=%q", safePrefix(content, 80))
	// `# Harness` 及之后的行为指令段被保留
	assert.Contains(t, content, "# Harness")
	assert.Contains(t, content, "Follow the repository's instructions")
	// 开头安全声明被删除
	assert.False(t, strings.Contains(content, "IMPORTANT: Assist"),
		"开头 IMPORTANT 安全声明应被删除")
	// 敏感词要么随开头段被删、要么被零宽打断；任何位置都不应出现连续 "DoS attacks"
	assert.False(t, strings.Contains(content, "DoS attacks"),
		"不应残留连续的 DoS attacks")

	// 非 Claude Code system（无 "You are Claude Code"）→ light 退化为 prune + 零宽
	body2 := []byte(`{"messages":[
		{"role":"system","content":"Generic system. Refuse DoS attacks."}
	]}`)
	out2 := DesensitizeOpenAIBody(body2, DesensitizeOpts{Enabled: true, CompactMode: "light"})
	content2 := gjson.GetBytes(out2, "messages.0.content").String()
	assert.Contains(t, content2, zwsp)             // 仍做零宽
	assert.Contains(t, content2, "Generic system") // 主体保留
	assert.False(t, strings.Contains(content2, "DoS attacks"))

	// user 对话内容不被改动
	assert.Equal(t, "hello", gjson.GetBytes(out, "messages.1.content").String())
}

// safePrefix 返回 s 的前 n 个字符（用于断言失败信息，避免超长）。
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func TestDesensitizeOpenAIBody_NumberPrecision(t *testing.T) {
	// 触发 marshal（system 命中）同时携带超大整数 seed，验证 UseNumber 不丢精度
	body := []byte(`{"messages":[{"role":"system","content":"Refuse DoS"}],"seed":12345678901234567}`)
	out := DesensitizeOpenAIBody(body, DesensitizeOpts{Enabled: true})
	assert.Equal(t, int64(12345678901234567), gjson.GetBytes(out, "seed").Int())
}

func TestAccountUpstreamDesensitizeSwitches(t *testing.T) {
	// 未配置 → 默认关闭、默认不压缩（CompactMode=""）
	var a *Account
	assert.False(t, a.IsUpstreamDesensitizeEnabled())
	assert.Equal(t, "", a.UpstreamDesensitizeCompactMode())

	a = &Account{Extra: nil}
	assert.False(t, a.IsUpstreamDesensitizeEnabled())
	assert.Equal(t, "", a.UpstreamDesensitizeCompactMode())

	// 仅开总开关
	a = &Account{Extra: map[string]any{"upstream_desensitize_enabled": true}}
	assert.True(t, a.IsUpstreamDesensitizeEnabled())
	assert.Equal(t, "", a.UpstreamDesensitizeCompactMode()) // 未配 compact_mode → 仅零宽

	// light 温和压缩
	a = &Account{Extra: map[string]any{
		"upstream_desensitize_enabled":     true,
		"upstream_desensitize_compact_mode": "light",
	}}
	assert.True(t, a.IsUpstreamDesensitizeEnabled())
	assert.Equal(t, "light", a.UpstreamDesensitizeCompactMode())

	// full 整段压缩
	a = &Account{Extra: map[string]any{
		"upstream_desensitize_enabled":     true,
		"upstream_desensitize_compact_mode": "full",
	}}
	assert.True(t, a.IsUpstreamDesensitizeEnabled())
	assert.Equal(t, "full", a.UpstreamDesensitizeCompactMode())

	// stealth 隐蔽模式（system 整段搬至 user）
	a = &Account{Extra: map[string]any{
		"upstream_desensitize_enabled":     true,
		"upstream_desensitize_compact_mode": "stealth",
	}}
	assert.True(t, a.IsUpstreamDesensitizeEnabled())
	assert.Equal(t, "stealth", a.UpstreamDesensitizeCompactMode())

	// 类型错误 / 非法值 → 视为未设（""）
	a = &Account{Extra: map[string]any{"upstream_desensitize_enabled": "yes"}}
	assert.False(t, a.IsUpstreamDesensitizeEnabled())
	a = &Account{Extra: map[string]any{"upstream_desensitize_compact_mode": true}} // 类型错误
	assert.Equal(t, "", a.UpstreamDesensitizeCompactMode())
	a = &Account{Extra: map[string]any{"upstream_desensitize_compact_mode": "compact"}} // 非法值
	assert.Equal(t, "", a.UpstreamDesensitizeCompactMode())
	a = &Account{Extra: map[string]any{"upstream_desensitize_compact_mode": ""}} // 空串
	assert.Equal(t, "", a.UpstreamDesensitizeCompactMode())
}

func TestDesensitizeOpenAIBody_Stealth(t *testing.T) {
	// 含 system + user + tool(description 含品牌词) 的典型 chat body
	body := []byte(`{"messages":[
		{"role":"system","content":"You are Claude Code, Anthropic's CLI. # Harness\nUse tools. Opus 4.8."},
		{"role":"user","content":"帮我写代码"}
	],"tools":[
		{"type":"function","function":{"name":"run","description":"Run a Claude tool"}}
	]}`)
	out := DesensitizeOpenAIBody(body, DesensitizeOpts{Enabled: true, CompactMode: "stealth"})

	// 新 messages 长度 == 3：system中性 + user_harness + 原 user
	assert.Equal(t, 3, int(gjson.GetBytes(out, "messages.#").Int()),
		"stealth 后 messages 应为 3 条: system中性 + user_harness + 原 user")

	// messages[0]：中性 system
	assert.Equal(t, "system", gjson.GetBytes(out, "messages.0.role").String())
	assert.Equal(t, stealthIdentity, gjson.GetBytes(out, "messages.0.content").String())

	// messages[1]：user_harness，包裹 <system_instructions>，原 harness 原样未零宽
	assert.Equal(t, "user", gjson.GetBytes(out, "messages.1.role").String())
	harnessContent := gjson.GetBytes(out, "messages.1.content").String()
	assert.True(t, strings.HasPrefix(harnessContent, "<system_instructions>\n"),
		"user_harness 应以 <system_instructions> 开头, got prefix=%q", safePrefix(harnessContent, 60))
	assert.True(t, strings.HasSuffix(harnessContent, "\n</system_instructions>"),
		"user_harness 应以 </system_instructions> 结尾, got tail=%q", safePrefix(harnessContent, 200))
	assert.Contains(t, harnessContent, "You are Claude Code",
		"harness 原文应原样保留，不做零宽脱敏")
	assert.Contains(t, harnessContent, "Opus 4.8",
		"harness 中的品牌模型词也应原样保留")
	assert.NotContains(t, harnessContent, zwsp,
		"user_harness 不应含零宽（user 不被审核，保留原文最完整语义）")

	// messages[2]：原 user，未被改动
	assert.Equal(t, "user", gjson.GetBytes(out, "messages.2.role").String())
	assert.Equal(t, "帮我写代码", gjson.GetBytes(out, "messages.2.content").String())

	// tools 仍做零宽脱敏（保守，与 full 一致）
	desc := gjson.GetBytes(out, "tools.0.function.description").String()
	assert.Contains(t, desc, zwsp, "tools description 仍应做零宽脱敏")
	assert.False(t, strings.Contains(desc, "Claude"),
		"tools description 中的品牌词应被零宽打断")

	// 边界：无 system/developer 消息 → stealth 原样返回（changed==false）
	bodyNoSys := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[
		{"type":"function","function":{"name":"run","description":"Run a Claude tool"}}
	]}`)
	outNoSys := DesensitizeOpenAIBody(bodyNoSys, DesensitizeOpts{Enabled: true, CompactMode: "stealth"})
	assert.Equal(t, string(bodyNoSys), string(outNoSys),
		"无 system/developer 消息时 stealth 应原样返回")
}

// 编译期保证 zwsp 确为 U+200B（防止源码里误填普通空格）。
func init() {
	if b, _ := json.Marshal(zwsp); string(b) != `"`+"​"+`"` {
		panic("zwsp must be U+200B")
	}
}
