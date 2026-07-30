# 设计: CodeBuddy 转发请求体脱敏

## 1. 总体边界

新增独立模块 `backend/internal/service/upstream_desensitize.go`，纯函数、无副作用、可单测。
职责：给定 OpenAI 请求体 `[]byte` + 脱敏选项，返回脱敏后的 `[]byte`（无变化则原样返回）。

不修改请求语义、不引入网络 / IO。内部异常时返回原 body（降级），绝不阻断请求。

## 2. 词表与正则（移植 desensitize.py）

- 常量 `sensitiveTerms []string`：1:1 移植 `codebuddy2api/desensitize.py:38-125` 的
  `SENSITIVE_TERMS`（攻击 / 安全术语 + Claude / Anthropic 品牌词，约 70 项）。
- 包级单例：`var sensitiveTermsRe = buildSensitiveTermsRe()`，按词长降序拼接、
  前缀 `(?i)` 等价大小写不敏感、词边界 `\b`。
- 零宽空格：`const zwsp = "​"`，插入位置 = 词首字符之后（`term[0] + zwsp + term[1:]`）。

## 3. 文本脱敏

`desensitizeText(s string) string`：用 `sensitiveTermsRe.ReplaceAllStringFunc`，
每个匹配项返回首字符后插 U+200B 的形式。空串 / 无命中原样返回。

## 4. 请求体脱敏（chat + responses 兼容）

```go
type DesensitizeOpts struct {
    Enabled bool
    Compact bool // true=压缩 harness system；false=仅零宽
}
func DesensitizeOpenAIBody(body []byte, opts DesensitizeOpts) []byte
```
`Enabled=false` 直接返回原 body。

字段处理（实现时择一：gjson 定位 + sjson 改写 贴合项目现状与性能；或反序列化
`map[string]any` 递归 更直观。倾向先 gjson/sjson，复杂分支再退回反序列化）：

- `messages[].content`：仅 role ∈ {system, developer}；user 消息仅当识别为 harness 时。
  - content 为 string：整段 `desensitizeText`（+ 可选 compact / prune）。
  - content 为 array：遍历 `{type:"text"}` 块，处理其 `text` 字段。
- `system`（顶层）：string 或 array of text blocks。
- `instructions`（responses 格式）：string。
- `input[]`（responses 格式）：同 messages 的 role / content 规则。
- `tools[].function.description` / responses tools 的 description：`desensitizeText`。

harness 识别（移植 `_HARNESS_USER_MARKERS` / `_CODEX_SYSTEM_MARKERS`）：markers 子串命中即判定为 harness。
- `Compact=true`：命中 markers 的 system / harness 替换为预置短摘要（移植 `_CODEX_CORE_SUMMARY` 等）。
- `Compact=false`：仅对原文 `desensitizeText` + `_pruneRuntimeFragments` 轻裁。

## 5. Account 开关（per-channel，Extra-based）

在 `account.go` 新增（参考 `IsOpenAIPassthroughEnabled` / `IsCustomBaseURLEnabled`，复用私有 `getExtraBool`，account.go:2121）：

```go
func (a *Account) IsUpstreamDesensitizeEnabled() bool {
    return a != nil && a.getExtraBool("upstream_desensitize_enabled")
}
// 默认不压缩：未配 compact（false）= 仅零宽、保留 system 原文；勾选才压缩。
func (a *Account) UpstreamDesensitizeCompact() bool {
    return a != nil && a.getExtraBool("upstream_desensitize_compact")
}
```

- 不限制 Platform / Type（通用能力）。
- MVP 不加保存路径 Normalize（参考 `NormalizeHeaderOverrideCredentials` 可后续补）。

## 6. 插入点

`openai_gateway_forward.go` 的 `Forward`：现有 body 规整段末尾（`flattenOpenAIResponsesNamespaces`
之后，约 L74）、`originalBody := body`（L76）之前，插入：

```go
if account.IsUpstreamDesensitizeEnabled() {
    if d := DesensitizeOpenAIBody(body, DesensitizeOpts{
        Enabled: true,
        Compact: account.UpstreamDesensitizeCompact(),
    }); d != nil {
        body = d
    }
}
```

- 位于 Grok / APIKey / passthrough 等 early-return / 分流之前 → 覆盖全部分支。
- `originalBody` 随后捕获脱敏后 body，与现有 normalize 步骤语义自洽
  （`originalBody` 本就是「规整后」的 body，非客户端最原始字节，参见 L41/L48/L65 各 normalize 步骤）。

## 7. 兼容性与降级

- 默认关闭（Extra 未设 → false）→ 零影响。
- `DesensitizeOpenAIBody` 内部 `recover` → 返回原 body（不阻断转发）。
- 仅改文本字段，不动 model / stream / tools 结构 → 不影响 `requestView` 解析、计费、failover hint。
- body 可能是 chat 或 responses：对两套字段都做存在性判断，缺失字段跳过。

## 8. 与 codebuddy2api/desensitize.py 的差异

- 语言 Go；body 为 `[]byte` 而非 Python dict。
- 仅覆盖 OpenAI 路径（chat / responses），不含 Anthropic 路径。
- 词表 / harness 逻辑 1:1 移植，行为等价。

## 9. 测试策略

`upstream_desensitize_test.go`：
- `desensitizeText`：词表命中、大小写、长短词优先、无命中原样、中文不误伤。
- `DesensitizeOpenAIBody`：system string / content blocks / tools description /
  responses 的 input+instructions / compact 开关两端 / 未启用原样返回。
- 插入点集成：构造含敏感词的 body + 开启渠道，断言下游收到的 body 已脱敏。
