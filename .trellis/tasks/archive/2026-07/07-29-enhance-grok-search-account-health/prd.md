# PRD: grok_search 账号健壮性增强（403/429 状态码识别）

## 目标

补齐 grok_search 平台 `handleGrokSearchAccountUpstreamError` 对 **403**（Cloudflare 挑战 + SSO 权限失效）和 **429**（免费额度耗尽）的识别与差异化处理，让账号状态码处理更健壮，**保持 grok_search 平台物理隔离**（不耦合其它平台、不照搬 grok2api 分层架构）。

## 背景

当前 `handleGrokSearchAccountUpstreamError`（`openai_gateway_grok_search.go:486`）的状态码处理：

| 状态码 | 当前处理 | 问题 |
|---|---|---|
| 401 | markGrokSearchReauthRequired（持久失效） | ✅ 合理 |
| 429 | 一律 `tempUnscheduleGrokSearch(5min)`，**不读 body** | ❌ 免费额度耗尽（`Free usage quota exceeded`）被当瞬时限流，5min 后恢复又 429，无效循环 |
| 403 | **无 case，落 default 不处理** | ❌ console.x.ai 在 CF 后，403 常见（CF 挑战 / SSO 权限失效），当前直接丢给客户端、且无差异化处理 |
| 5xx | 非 pool mode `tempUnschedule(2min)` | 基本可用 |

实测 case：`grok-4.20-multi-agent` 请求 console.x.ai 返回
`429 {"code":"resource-exhausted","error":"Free usage quota exceeded. Purchase credits or provision an API key at https://console.x.ai"}`
—— 这是**免费额度耗尽**（非瞬时频率限制），当前被误判，应长冷却。

## 范围（本期）

### REQ-1：429 免费额度耗尽识别 + 长冷却
- 读 429 响应 body，识别 console 免费额度耗尽特征：`free usage quota exceeded` / `resource-exhausted`（且非 RPS 速率文本）。
- 命中 → 长冷却 **24h**（`tempUnscheduleGrokSearch(24h)`）。
- 未命中（普通瞬时频率限制）→ 保持现有短退避（5min）。

### REQ-2：403 差异化处理
- **CF 挑战 403**：用 `httputil.IsCloudflareChallengeResponse`（已有工具）识别 → **不惩罚账号**（不冷却、不失效），返回原 403 让上层既有机制处理。
- **SSO 权限失效 403**：识别 console 权限拒绝特征（如 `permission-denied` / `permission_denied` / 明确的 access denied）→ `markGrokSearchReauthRequired`（与 401 同处理，SSO 权限丢了需重导）。
- 其它 403 → 维持现状（不特殊处理，落 default）。

### REQ-3：隔离与零回归
- 所有改动封闭在 `handleGrokSearchAccountUpstreamError` + grok_search 专属辅助识别函数内。
- **不碰** grok / openai / gemini / anthropic 等任何其它平台的错误处理。
- **不照搬** grok2api 的 failure.go/selector 分层架构 + QuotaRecovery 探测队列，保持 sub2api `switch statusCode` 简洁结构。
- CF 检测复用 utils 层 `httputil.IsCloudflareChallengeResponse`（工具函数共享是正当的，非业务耦合）。

## 不做（本期排除，已确认）

- **auto-clean 失效账号自动清理**：sub2api 是多用户 SaaS，物理删账号语义过重；失效账号（status=error）保持现状等管理员重导 SSO 恢复。如未来账号规模增大可再评估。
- **429 Retry-After 头对齐**：console 该错误可能不带 Retry-After，固定 24h 更确定。
- **429 可配时长**：本期固定 24h，不增加配置项。
- **402 处理**：grok_search 的设计目标就是绕开 402（走 console 网页订阅配额），保持不复用 grok 402 语义。

## 验收标准

1. **429 免费额度耗尽**：body 含 `free usage quota exceeded` → 账号冷却 24h（不是 5min）；body 是普通限流文本 → 仍 5min。
2. **403 CF 挑战**：`httputil.IsCloudflareChallengeResponse` 判定为 CF → 账号**不冷却不失效**（tempUnschedule/markReauth 均不调用）。
3. **403 SSO 权限失效**：body 含权限拒绝特征 → markGrokSearchReauthRequired（status=error + schedulable=false）。
4. **隔离**：改动只在 grok_search 文件，其它平台错误处理零改动。
5. **测试**：429 两分支（额度耗尽/普通限流）、403 三分支（CF/权限失效/其它）、隔离回归（grok/openai 错误处理不受影响）单测全过。
6. `go build ./...` + `go vet` + 目标测试通过。

## 约束

- 简体中文注释，参照 `openai_gateway_grok_search.go` 现有注释密度与风格。
- 工具类/集合优先 hutool/guava 是 Java 规范；Go 这边照周围代码风格（strings 标准库）。
- 遵循 grok_search 物理隔离原则（见 `.trellis/spec/backend/new-platform.md`）。
