# grok vs grok_search 路径隔离边界与额度机制

> 同一 xAI 账号在 sub2api 可绑成两条独立转发路径（`grok` / `grok_search`）。二者在 **sub2api 层物理隔离**，但在 **xAI 上游层共享账号免费配额**。本文固化两者的隔离边界、额度共享、重置/冷却与测试连接契约。

---

## 1. Scope / Trigger

何时需要查本文：
- 排查「同一 xAI 账号绑了 grok 和 grok_search 两个平台，一边额度用完，另一边也报 429」。
- 疑问「grok 和 grok_search 是不是共用免费额度」「两边冷却/重置逻辑是否一样」。
- 排查「测试账号连接」是否会影响调度状态。

**不重复**：接入步骤见 [new-platform.md](./new-platform.md)；出口层 / CF / uTLS 指纹见 [upstream-egress.md](./upstream-egress.md)。本文只讲两条 grok 路径的**关系边界**。

---

## 2. 两条路径的通道隔离（sub2api 层，可证）

sub2api 把 grok 和 grok_search 当**两条独立链路**处理。关键事实：**不同 account 记录、不同 `account.ID`、不同 platform**，一方 429 **不会**让 sub2api 把另一方下线。

| 维度 | grok | grok_search |
|---|---|---|
| platform 常量 | `PlatformGrok` | `PlatformGrokSearch` |
| account 记录 | 独立一条 | 独立一条 |
| 上游端点 | 官方网关 `cli-chat-proxy.grok.com`（Responses API）| `console.x.ai/v1/responses`（Console 网页通道）|
| 凭据 | OAuth `access_token`（`grokTokenProvider.GetAccessToken`）/ API key | SSO `sso_token`（`account.GetCredential("sso_token")`）|
| 配额快照 | 解析 `x-ratelimit-*` 头，落 `grok_usage_snapshot` extra | **不解析** OIDC 配额快照 |
| 错误处理 | `handleGrokAccountUpstreamError`（`openai_gateway_grok.go:1350`）| `handleGrokSearchAccountUpstreamError`（`openai_gateway_grok_search.go:490`）|
| 限流/冷却持久字段 | `RateLimitResetAt`（持久限流，`account.go:49`）| `TempUnschedulableUntil`（临时下线，`account.go:52`）|

转发链路隔离的完整清单见 new-platform.md §grok_search 案例。

---

## 3. 额度共享契约（xAI 上游层）

两条路径背后是**同一个 xAI 账号**，消耗的是**同一份账号级免费配额**。

**证据**：`openai_gateway_grok_search.go:20-23` 注释明言 grok_search 的目的是「让搜索按 **Console 网页订阅配额**计费，**绕开 Responses API 的 personal-team-blocked:spending-limit (402)**」——「绕开」的是 Responses API 的**付费墙**，**不是另起一份独立免费额度**。两通道背靠同一账号的免费层。

**耗尽形态不同**（同一份额度的两种表现）：

| 通道 | 免费层耗尽时的表现 |
|---|---|
| grok（Responses API）| `402 payment required`（spending-limit）/ 429 带 `x-ratelimit-*` 头 |
| grok_search（Console）| `429` body 含 `free usage quota`（`resource-exhausted`）|

**Free 档窗口**：`GrokFreeRolling24hTokenLimit = 1_000_000`（`pkg/xai/quota.go:10`），**滚动 24h token 窗口**（非"每天定点重置"），2026-07 前曾为 200 万。

> **诚实边界**：sub2api **可证**通道隔离（第 2 节）；xAI 服务端把两通道配额合并到同一账号是**强推断 + 实证现象**（一边耗尽、另一边随之 429），代码无法 100% 证实 xAI 计费模型。坐实方法：新账号只绑一边跑到耗尽，看另一边（未使用）是否也立即 429。

**要独立额度 → 必须换不同的 xAI 账号**。同一账号绑两平台，免费额度必然共享，sub2api 隔离不了。

---

## 4. 额度重置 / 冷却矩阵

「重置逻辑」要分两层，**不一样**：
- **xAI 额度本身恢复**：一样（同一份额度，同时回血，Free 档滚动 24h）。
- **sub2api 对账号的调度冷却**：**两套完全不同的代码**。

### 4.1 grok（429：读上游头，动态算 resetAt）

- 入口 `handleGrokAccountUpstreamError` → `updateGrokUsageSnapshot` 安装 runtime + durable 限流。
- reset 计算 `grokRateLimitResetAt`（`openai_gateway_grok.go:1193`），优先级：①上游 `retry-after` ②配额窗口 `x-ratelimit-reset-*`（`Remaining==0`）③兜底 `grokRateLimitFallbackCooldown = 2min`。
- **自适应阶梯** `grokRateLimitResetAtForAccount`（`:1246`）：短时间（静默期 `grokRateLimitBackoffQuietPeriod = 1h` 内）反复 429，在上游 reset 基础上取较晚者升级：

  | 上次冷却 | 这次至少 |
  |---|---|
  | < 10min | 10min（`grokRateLimitRepeatCooldown`）|
  | [10min, 30min) | 30min（`grokRateLimitSustainedCooldown`）|
  | ≥ 30min | 1h（`grokRateLimitMaxAdaptiveCooldown`）|

- 落 `RateLimitResetAt`，单调递增（`SetRateLimitedIfLater`）。

### 4.2 grok_search（429：不读上游头，固定时长）

- 入口 `handleGrokSearchAccountUpstreamError`（`openai_gateway_grok_search.go:490`）。
- 免费额度耗尽（`isGrokSearchFreeQuotaExhausted`：body 含 `free usage quota`）→ `tempUnscheduleGrokSearch(grokSearchFreeQuotaCooldown = 24h)`。
- 普通 RPS 限流 → `grokSearchRateLimitCooldown = 5min`。
- **固定 24h 是刻意的**：console 的 429 常不带 `Retry-After`，且 5min 恢复后又 429 形成无效循环；24h 对齐 grok2api `defaultFreeQuotaRecoveryPause`，贴近 Free 真实周期。
- 落 `TempUnschedulableUntil`。

### 4.3 恢复机制（两边都有）

| 机制 | grok | grok_search |
|---|---|---|
| 到期自动放行 | ✅ `account.go:196` `now.Before(RateLimitResetAt)` 为 false 即回池 | ✅ `account.go:199` `now.Before(TempUnschedulableUntil)` 为 false 即回池 |
| 探测成功提前清除 | ✅ `isSuccessfulGrokRateLimitRecovery`（`:1295`）+ `clearGrokRateLimitAfterRecovery`，带 generation 保护（`ClearRateLimitIfObserved`，`account_repo.go:2105`）| ✅ 管理员手动测试/探测 2xx（但测试函数本身不改状态，见 §5）|
| 凭据自愈 | ✅ OAuth 有 token refresh（`grok_token_refresher.go`），401 通常自愈 | ❌ SSO 无 refresh，401/403 权限失效 → `markGrokSearchReauthRequired`，需管理员重导 SSO |

### 4.4 状态码 → 动作总矩阵

| 状态码 | grok（`handleGrokAccountUpstreamError`）| grok_search（`handleGrokSearchAccountUpstreamError`）|
|---|---|---|
| 401 | temp 10min（凭据未授权，通常 refresh 自愈）| `markGrokSearchReauthRequired`（持久 status=error，24h 兜底 block，需重导 SSO）|
| 402 | temp 30min（payment required）| 不冷却（grok_search 目标正是绕开 402；console 返回 402 按未知透传）|
| 403 | 30min 或 `applyGrokForbiddenPolicy`（entitlement）| CF 挑战→不惩罚 / SSO 权限失效→markReauthRequired / 其它→不处理 |
| 429 | `RateLimitResetAt`（读头 / 兜底 2min / 阶梯 10min~1h）| CF→不惩罚 / 免费额度→24h / RPS→5min |
| 5xx | temp 2min（非 pool mode）| temp 2min（非 pool mode）|

> grok_search 的 **CF 判定必须在最前**（403/429 都可能命中 `IsCloudflareChallengeResponse`），CF 是出口/指纹问题，绝不冷却或失效账号。

---

## 5. 测试连接契约（副作用差异）

入口 `TestAccountConnection`（`account_test_service.go:180`）按 platform 分流到两个**独立**函数。两者共用测试框架（SSE 推进、`test_start` 事件、`httpUpstream`、默认模型 `grokDefaultResponsesModel`），但实现与副作用完全不同。

| 维度 | `testGrokAccountConnection`（`:715`）| `testGrokSearchAccountConnection`（`:844`）|
|---|---|---|
| 认证 | `GetAccessTokenForManualTest` / API key | `sso_token` |
| 上游 URL | `buildGrokResponsesURL`（走白名单）| 自拼 `base_url + /v1/responses`（绕白名单）|
| TLS | 裸 `httpUpstream.Do`（cli-chat-proxy 无 CF）| `DoWithTLS` + `grokSearchChromeProfile()`（过 CF）|
| 流式 | 流式 SSE（`processOpenAIStream`）| 非流式（`stream:false`，读 JSON 提取文本）|
| **是否改账号状态** | **会**（见下）| **不会**（纯连通性，注释 `:842-843`）|

**grok 测试的副作用清单**（`account_test_service.go:796-826`）：
- 写 quota snapshot 到 extra
- 命中限流 → `persistGrokRateLimit`（可能把账号压成限流）
- 探测 2xx → `clearGrokRateLimitAfterRecovery`（提前解除限流）
- 返回 **402 → `SetTempUnschedulable` 30min**

> 注：grok 测试「不走调度资格门」（限流/冷却中的账号也能测，`:737`），但测试本身仍会更新配额/限流状态。**测 grok 账号可能把刚恢复的账号又压成限流，或 402 时临时下线 30min**；测 grok_search 账号是纯只读探测，不影响调度。

---

## 6. Good / Base / Bad Cases

- **Good**：grok 和 grok_search 分别绑**两个不同 xAI 账号** → 额度独立，互不影响，且 sub2api 通道本就隔离。
- **Base**：同一 xAI 账号绑两平台 → sub2api 通道隔离（各自独立限流/冷却/状态），但 xAI 额度共享，一边耗尽另一边也 429。
- **Bad**：期望「同账号绑两平台 = 额度翻倍」或「sub2api 能隔离两通道额度」→ 不成立，额度是 xAI 账号级的。

---

## 7. Wrong vs Correct

#### Wrong
- 「同账号绑 grok + grok_search，免费额度翻倍 / 互不影响」—— 额度是账号级共享。
- 「grok 的 reset 逻辑（读头、2min~1h）也适用于 grok_search」—— 两套独立代码，grok_search 固定 24h。
- 「测试 grok_search 账号会像 grok 一样改账号状态」—— grok_search 测试无副作用。

#### Correct
- 要独立额度 → 换不同 xAI 账号。
- grok_search 的 24h 冷却是**刻意贴近 Free 档真实恢复周期**的设计（grok 的 2min~1h 反而短于 Free 真实周期，Free 耗尽后会「恢复→打→挂→再限」循环）。
- 排查 grok 类 402/429 反复：先确认账号档位；Free 档 grok 通道冷却短于真实恢复，循环属预期，真正恢复看 xAI 滚动 24h 窗口。

---

## 8. Common Mistakes / Gotchas

1. **把「sub2api 通道隔离」当成「额度隔离」** → 通道隔离可证（§2），额度共享是 xAI 账号级行为（§3），sub2api 隔离不了。
2. **grok 冷却（2min~1h）短于 Free 真实恢复（~24h）** → Free 档耗尽后 grok 通道会反复「恢复→打→402/429→再限」；grok_search 的 24h 更贴真实节奏，空转更少。
3. **测 grok 账号有副作用** → 测试可能写 snapshot、触发 `persistGrokRateLimit`、或 402 时 `SetTempUnschedulable` 30min，干扰调度；grok_search 测试纯只读。
4. **grok_search 24h 不读上游头是刻意设计** → console 429 常不带 `Retry-After`，且 5min 短冷却会无效循环；不要为「对齐 grok 行为」改成读头/短冷却。
5. **混淆"额度重置"两层** → xAI 额度恢复（一样，同时回血）≠ sub2api 调度冷却（两套不同代码）。用户问"多久重置"要先确认问的是哪层。
6. **grok_search SSO 失效不会自愈** → 无 refresh，401/403 权限失效需管理员重导 SSO；grok OAuth 有 refresh 可自愈。

---

## 关联
- [New Platform Onboarding](./new-platform.md) —— grok_search 接入清单、grok_search 错误处理决策树（§grok_search 案例、Common Mistakes #18）
- [Upstream Egress & TLS Fingerprint](./upstream-egress.md) —— grok_search 过 console.x.ai CF 的 Chrome uTLS 指纹
