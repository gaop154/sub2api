# 技术设计：grok_search 对齐上游 console 行为

> 上游参照：grok2api @ `62d2775c`（v3.1.5）。行为语义以其为唯一权威，函数命名以 sub2api 既有风格为准。

## 1. 改动面总览

只动两个实现文件 + 测试；不改分发入口/调度/DB。

| 文件 | 改动 |
|---|---|
| `backend/internal/service/openai_gateway_grok_search.go` | tools 归一增强（view_image/原生类型/参数透传）、tool_choice 策略、input patch 补 reasoning 分支、effort auto、常量回正、429 精确冷却、注释修正 |
| `backend/internal/service/openai_gateway_grok_search_dpop.go` | mint 传 proxyURL、缓存键含出口、session clockSkew、proof iat 偏差 |
| 新建 `openai_gateway_grok_search_tools_test.go` + dpop 测试扩充 | AC1-AC12 用例 |

三个调用点（`forwardGrokSearch`、`forwardGrokSearchChatCompletionsViaResponses`、`testGrokSearchAccountConnection`）都经 `normalizeGrokSearchRequestBody` 与 `doGrokSearchDPoPRequest`，改动自动全覆盖，**外部签名不变**。

## 2. 归一化链路改造（grok_search.go）

### 2.1 normalizeGrokSearchTools

```
现：func normalizeGrokSearchTools(payload map[string]any)
改：func normalizeGrokSearchTools(payload map[string]any) bool   // 返回 retainedClientTools
```

调用点接收返回值传给新的 tool_choice 函数（对应上游 `normalizeConsoleToolChoice(payload, retainedClientTools)` 模式）。

- 函数开头计算 `hasClientViewImage := hasGrokSearchFunctionTool(tools, "view_image")`（新 helper：EqualFold+TrimSpace 匹配 type=function 且 name=target）。
- **web_search 分支**：初始 `enable_image_understanding: !hasClientViewImage`；客户端显式值仅 `!hasClientViewImage` 时采纳；新增透传 `enable_image_search`（bool，存在才写键）。
- **x_search 分支**：video_understanding 逻辑不变；新增 `from_date`/`to_date` 循环（string 非空 → `time.Parse("2006-01-02")` + 回写比对全等 → 写入）；结尾双值齐备且 from.After(to) 成对 delete。
- **function 分支**：现状不变；命中即 `retainedClientTools = true`。
- **新增原生类型分支**：`mcp/shell/image_generation/collections_search/file_search/code_execution/code_interpreter` → `result = append(result, tool)` 原样保留 + `retainedClientTools = true`（对应上游 normalize.go:323-328）。

### 2.2 新函数 normalizeGrokSearchToolChoice

```go
func normalizeGrokSearchToolChoice(payload map[string]any, retainedClientTools bool)
```

- tools 键不存在 → delete tool_choice 后 return（本实现恒注入，防御性对齐上游）。
- 字符串：none/auto → 小写写回；required → retainedClientTools 才保留否则 auto；default → auto。
- map：type==function 且 retainedClientTools；name 为空回落读嵌套 `object["function"].(map)["name"]`；仍空或条件不满足 → auto；合法写回 `{type:function,name}` 标准形态。
- 非 string 非 map → auto。

### 2.3 patchGrokSearchInput 补 reasoning 分支

item 循环开头增加（对齐上游 patchConsoleInput:126-129）：

```go
if item["type"] == "reasoning" {
    patchGrokSearchReasoningContent(item)
    continue
}
```

`patchGrokSearchReasoningContent`：遍历 content，part 无 `type` 且有 `text` → 补 `type:"reasoning_text"`（多轮回放时客户端回传的 reasoning part 无 type 会被上游拒）。

### 2.4 effort auto 档

`normalizeGrokSearchEffort` switch 增加 `case "auto": return "auto"`。效果：客户端显式 auto 透传；preferredEffort/medium 兜底不受影响（splitGrokSearchEffortSuffix 后缀表无 "-auto"，模型名后缀不触发）。

### 2.5 常量与注释

- `grokSearchDefaultMaxOutputTokens = 1_000_000`；注释补"上游 console catalog multi-agent 实际值；上游按模型上限校验，勿放大"。
- `handleGrokSearchAccountUpstreamError` doc"长冷却 24h"→"30 天"；doc 尾部加分叉说明："默认注入 web_search/x_search 为本平台刻意设计（搜索通道定位），与上游 339617a2 之后的行为相反，勿'对齐'移除"。
- `normalizeGrokSearchRequestBody` doc 同步补充新能力要点。

## 3. DPoP 改造（grok_search_dpop.go）

### 3.1 出口一致性（R5）

- `fetchGrokSearchDPoPSession(ctx, httpUpstream, account, ssoToken)` → 增加 `proxyURL string` 参数；内部 `DoWithTLS(req, proxyURL, ...)`。
- `manager.get(ctx, httpUpstream, account, ssoToken)` → 增加 `proxyURL string`；透传给 fetch。
- `doGrokSearchDPoPRequest` 已有 proxyURL，直接下传（外部 3 个调用点零改动）。
- 缓存键 `baseURL|accountID|hash(sso)` → `baseURL|accountID|proxyURL|hash(sso)`（对齐上游键含 lease.NodeID 的出口绑定语义）。空 proxy（直连）也是一种出口，参与键构成。

### 3.2 iat 时钟偏差（R10）

完全移植上游算法：

- `grokSearchDPoPSession` 增加 `clockSkew time.Duration`（server − local，秒级取整；0 合法）。
- mint：请求前记 `localBefore`、读完响应记 `localAfter`；`clockSkew := grokSearchDPoPClockSkewFromDateHeader(resp.Header.Get("Date"), localBefore, localAfter)`；随成功路径存入 session。
- helper `grokSearchDPoPClockSkewFromDateHeader`：trim 空 → 0；`http.ParseTime` 失败 → 0；local 窗口退化收敛后取中点 `localMid`；结果 `.Round(time.Second)`。
- helper `grokSearchDPoPProofIAT(session, localNow)`：零值兜底；返回 `localNow.Add(session.clockSkew)`。
- `applyGrokSearchDPoPAuthorization` 的 iat 改用上述 helper。
- `expiresAt` 继续本地墙钟（缓存判定自洽）；skew 只作用于 proof iat——与上游一致。
- 缓存键**不掺 skew**（每 mint 重学，不持久化）。

## 4. 429 精确冷却（grok_search.go）

### 4.1 新增两个包级纯函数（便于单测，测试连接路径可复用）

```go
func grokSearchRetryAfterFromHeader(value string, now time.Time) time.Duration  // 整数秒 或 http.ParseTime 日期差
func grokSearchRetryAfterFromBody(body []byte) time.Duration                    // "resets in:" 后 (\d+)\s*([dhms]) 累加
```

移植上游 `parseConsoleRetryAfterHeader` / `consoleRetryAfter`（normalize.go:414-447）。

### 4.2 handleGrokSearchAccountUpstreamError 429 分支

现有顺序不变（CF → 免费额度 30d → 瞬时限流），瞬时限流分支改为：

```
d := grokSearchRetryAfterFromHeader(Retry-After 头, now)
if d <= 0 { d = grokSearchRetryAfterFromBody(body) }
if d > 0 { d = clamp(d, 1min, 24h) } else { d = 5min }
tempUnscheduleGrokSearch(ctx, account, d, ...)
```

函数已有 `headers http.Header` 参数，无需改签名。测试连接路径 `applyGrokSearchTestAccountErrorState` 的 429 分支同语义更新（复用同一对纯函数，遵守 spec §5.1"复用包级原子、内联组合"约定）。

## 5. 测试设计

新建 `openai_gateway_grok_search_tools_test.go`（表驱动）：

| 组 | 用例 | AC |
|---|---|---|
| tools-viewimage | 带 view_image 显式 true → false；无 view_image 显式 true → true；都不带 → 默认 true | AC1/AC2 |
| tools-image-search | enable_image_search true/false 透传；未设不产键 | AC6 |
| xsearch-dates | 合法透传；空串/"2026-13-01"/"20260101"/非 string 丢弃；from>to 成对丢；仅 from 合法保留 | AC5 |
| native-tools | 七种原生类型原样透传；tool_choice required 因之放行 | AC7/AC3 |
| tool-choice | required±工具 ×2；function 对象±工具 ×2；嵌套 name；none/auto；未知串；非 function map | AC3 |
| reasoning-patch | reasoning item 无 type 有 text → reasoning_text；普通 item 不受影响 | AC8 |
| effort-auto | auto 透传；缺省 medium；后缀路径不受影响 | AC9 |
| max-tokens | 缺省断言 1000000 | AC4 |

dpop 测试（现有文件扩充）：

| 组 | 用例 | AC |
|---|---|---|
| egress-consistency | mint 请求经代理（httptest 断言 proxyURL 传入）；不同 proxyURL 缓存键不同 | AC10 |
| clock-skew | Date 正/负偏差、缺失/坏格式、RTT 中点固定输入 | AC11 |

429 冷却测试（tools_test 或独立组）：

| 组 | 用例 | AC |
|---|---|---|
| retry-after | Retry-After 头（秒/HTTP 日期）；body "Resets in: 3m 4s"；两者皆无 → 5min；超大值 clamp 24h；免费额度仍 30d | AC12 |

既有回归：`go test ./internal/service/ -count=1` 全量，重点 `account_test_service_grok_search_test.go`、`account_grok_search_chat_completions_test.go` 等 body/冷却断言。

## 6. 兼容性与回滚

- 行为变化集中在"客户端发送了特定字段/配了代理"的场景；裸请求产物几乎不变（唯 max_output_tokens 2M→1M、effort auto 不再落 medium）。
- 无 DB/配置/schema 变更；缓存键变更仅致进程内 session 重建一次。
- Step 边界即回滚边界：tools 族 / dpop 族 / 429 族各自独立 commit。

## 7. 风险

| 风险 | 缓解 |
|---|---|
| 原生工具类型上游契约若变化 | 原样透传不构造，最坏与上游 console 网页行为一致 |
| skew Round 边界理解偏差 | 直接移植实现 + 固定输入测试锁定 |
| tool_choice 收紧改变依赖 forced function 的调用方 | 仅原本即上游必拒的形态有差异；结果为可用（auto）而非报错 |
| 429 解析值异常（如 0/负/超大） | clamp [1min,24h] + 5min 兜底 |
| mint 代理修复后某些账号 CF 行为变化 | 上游本就同出口 mint；修复是回归正确语义，且 CF 分支不惩罚账号逻辑兜底 |
