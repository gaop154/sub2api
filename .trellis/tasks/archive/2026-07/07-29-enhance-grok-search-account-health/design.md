# Design: grok_search 账号健壮性增强（403/429 状态码识别）

## 1. 边界与契约

- **改动文件**：仅 `backend/internal/service/openai_gateway_grok_search.go` + 新增测试文件。
- **不改签名**：`handleGrokSearchAccountUpstreamError(ctx, account, statusCode, headers, responseBody)` 当前已接收 `headers http.Header` 和 `responseBody []byte`，但被 `_ = responseBody; _ = headers` 忽略（:502-503）。本次**启用这两个已有参数**，签名零变更。
- **不碰**：其它平台错误处理、调度层、forwarder 主体、选号链路。

## 2. 核心设计：状态码决策树

扩展 `handleGrokSearchAccountUpstreamError` 的 `switch statusCode`：

```
401 → markGrokSearchReauthRequired                    （现状，不动）
403 → [新增]
       ├─ IsCloudflareChallengeResponse(403,h,body)?  → 不惩罚账号，return（CF 出口问题，非账号问题）
       ├─ isGrokSearchPermissionDenied(body)?         → markGrokSearchReauthRequired（SSO 权限丢失，同 401）
       └─ 否则                                         → 落 default（不特殊处理）
429 → [改造，原为一律 5min]
       ├─ IsCloudflareChallengeResponse(429,h,body)?  → 不惩罚账号，return（CF 挑战可能伪装成 429）
       ├─ isGrokSearchFreeQuotaExhausted(body)?       → tempUnscheduleGrokSearch(24h)（免费额度耗尽）
       └─ 否则（普通瞬时频率限制）                      → tempUnscheduleGrokSearch(5min)（保持现状）
5xx → 非 pool mode tempUnschedule(2min)               （现状，不动）
default → 现状
```

**判定顺序关键**：CF 判定必须**最前**——`IsCloudflareChallengeResponse` 对 403 和 429 都会命中（CF 挑战可能以 429 形态出现），CF 是出口/指纹问题，绝不能当账号额度/权限问题去冷却或失效账号。

## 3. 辅助识别函数（grok_search 专属，同文件内）

### 3.1 CF 挑战（复用工具，不新写）
直接调 `httputil.IsCloudflareChallengeResponse(statusCode, headers, body)`：
- 对 403/429 判定；`cf-mitigated: challenge` 头 或 body 含 HTML challenge 标记（"just a moment" 等）→ true。
- **复用 utils 层工具，非业务耦合**：判断后的处理（不惩罚账号）仍是 grok_search 自己的。

### 3.2 免费额度耗尽 `isGrokSearchFreeQuotaExhausted(body []byte) bool`
- 关键词：body（lower）含 `free usage quota`（匹配实测的 `"Free usage quota exceeded"`）。
- **不单看 `code:resource-exhausted`**：grok2api 经验显示 console 的 RPS 速率限流也是 `resource-exhausted`（error 是 "Too many requests for team... Requests per Second"）。单看 code 会把 RPS 限流误判成额度耗尽。用 error 文本 `free usage quota` 精确区分。
- 大小写不敏感（lower 后匹配）。

### 3.3 SSO 权限失效 `isGrokSearchPermissionDenied(body []byte) bool`
- 关键词：body（lower）含 `permission-denied` / `permission_denied` / `access to the chat endpoint is denied`（参照 grok2api permanent denial 语义）。
- 命中即视为 SSO 权限丢失，走 markGrokSearchReauthRequired（与 401 同）。

## 4. 常量

```go
// grok_search 免费额度耗尽冷却：console 网页订阅免费额度按周期重置，
// 短冷却（5min）无效（恢复后又 429），对齐 grok2api defaultFreeQuotaRecoveryPause。
grokSearchFreeQuotaCooldown = 24 * time.Hour
```
现有 `grokSearchRateLimitCooldown = 5 * time.Minute`（普通限流，保持）。若 5min 当前是内联字面量，提取为命名常量。

## 5. 数据流

```
console.x.ai 返回 >=400
  → forwardGrokSearch :155 / 桥接路径 调 handleGrokSearchAccountUpstreamError(ctx, account, status, headers, body)
  → switch：
      CF?        → 不动账号状态（账号仍在调度池，由上层既有重试/换号机制处理）
      免费额度?  → tempUnscheduleGrokSearch(24h)（内存 BlockAccountScheduling + DB SetTempUnschedulable）
      权限失效?  → markGrokSearchReauthRequired（status=error + schedulable=false，管理员重导）
      普通限流?  → tempUnscheduleGrokSearch(5min)
```

## 6. 隔离保证（呼应 prd REQ-3）

- 所有逻辑封闭在 `openai_gateway_grok_search.go`。
- CF 判定调 utils 工具（`httputil`）——工具函数跨平台共享是正当的，**处理决策仍是 grok_search 专属**。
- 不引入 grok2api 的 `failure.go` 分类层 / `selector` 多 Mark 方法 / `QuotaRecovery` 探测队列——保持 sub2api `switch statusCode` 简洁结构，24h 冷却复用现有 `tempUnscheduleGrokSearch`（内存 block + DB SetTempUnschedulable），不新增恢复队列。
- 桥接路径 `forwardGrokSearchChatCompletionsViaResponses`（:647 注释提到错误分流）同样调 `handleGrokSearchAccountUpstreamError`，自动受益，无需另改。

## 7. 关键决策与权衡

| 决策 | 选择 | 理由 |
|---|---|---|
| 403 CF 重试 | 不在 grok_search 内重试，返回原 403 让上层处理 | 上层选号-转发循环是通用层（所有 openai 兼容平台共用），在 grok_search 内加重试会耦合通用层。CF 挑战是临时出口问题，客户端/上层既有机制重试即可；最小耦合 |
| 429 冷却时长 | 固定 24h | console 该错误可能不带 Retry-After；24h 对齐 grok2api `defaultFreeQuotaRecoveryPause`，确定可预期；不增配置项 |
| 免费额度识别 | error 文本 `free usage quota`，非 code | `resource-exhausted` code 也用于 RPS 速率限流，单看 code 误判 |
| CF 判定顺序 | 最前（403/429 都先判 CF） | CF 挑战可能以 403 或 429 形态出现，必须优先排除，否则误冷却/误失效 |
| 失效账号清理 | 不做 | sub2api 多用户 SaaS，物理删账号语义重；失效账号留 status=error 等管理员重导 |

## 8. 回归风险

- **启用 headers/body 参数**：当前被忽略，启用后不影响 401/5xx/default 分支（它们不读 body）。
- **429 分支顺序**：必须 CF → 免费额度 → 普通限流。顺序错则 CF 挑战会被误判。测试覆盖三种 429 body。
- **403 新增 case**：原 403 落 default（不处理），新增 CF/权限分支后，非 CF 非权限的 403 仍落 default，行为不变。
- **隔离回归**：grok/openai 错误处理在各自 `handleXxxAccountUpstreamError`，零交集。
