# 新平台落地 sub2api（隔离模式）

> 接入一个新 AI 平台（如 grok_search）时的隔离约定，确保零回归现有平台。

---

## 概述

sub2api 按平台（platform）分发请求到对应 forwarder。新平台必须**物理隔离**：独立 forwarder、独立分发分支、独立错误处理，不污染现有平台链路。

---

## 落地清单

### 1. 平台常量（双源，缺一不可）
- `internal/domain/constants.go` Platform constants 块加 `PlatformXxx = "xxx"`
- `internal/service/domain_constants.go` re-export：`PlatformXxx = domain.PlatformXxx`
- service 层用**无前缀**名（`PlatformGrok`，不是 `domain.PlatformGrok`），必须同步 re-export，否则 service 包 `undefined: PlatformXxx`

### 2. forwarder + 分发分支
- 新建 `service/openai_gateway_<platform>.go`，签名对齐 `forwardGrokResponses`：
  ```go
  func (s *OpenAIGatewayService) forwardXxx(ctx context.Context, c *gin.Context, account *Account,
      body []byte, originalModel string, reqStream bool, startTime time.Time) (*OpenAIForwardResult, error)
  ```
- 分发点 `service/openai_gateway_forward.go:101` 附近加字面量分支：
  ```go
  if account.Platform == PlatformXxx {
      return s.forwardXxx(ctx, c, account, body, originalModel, reqStream, startTime)
  }
  ```

### 3. 独立账号级错误处理
- 新平台**不要复用**其他平台的 `handleXxxAccountUpstreamError`（如 grok 的含 402 冷却 + quota snapshot，会带入不想要的语义）。
- 写独立的 `handleXxxAccountUpstreamError`，参考 `tempUnscheduleGrokSearch`：`BlockAccountScheduling` + `accountRepo.SetTempUnschedulable`。

### 4. platform CHECK 约束现状（建账号/分组前必查）

| 表 | 有 CHECK？ | 说明 |
|---|---|---|
| `accounts.platform` | **否** | 直接可写新 platform（`ent/schema/account.go:63` 仅 MaxLen+NotEmpty）|
| `groups.platform` | **否** | 直接可写 |
| `user_platform_quotas.platform` | **是** | 启用 quota 才需 migration 放开（仿 157 幂等 DROP+ADD）+ 同步 `ent/schema/user_platform_quota.go` Validate + `service/domain_constants.go` AllowedQuotaPlatforms |
| `channel_monitors.provider` / `channel_monitor_request_templates.provider` | **是** | 启用监控才需 migration（仿 176）+ 同步 ent `Values(...)` |
| `composite_model_routes.target_platform` | **是** | 接入 composite 路由才需（仿 172）|

> **基础可用（建账号 + 转发）不需要任何 migration**——accounts/groups 无 CHECK。
>
> ⚠️ 但 CHECK 通过 ≠ 调度能选中。见 §5 调度快照门槛——`schedulerSnapshotPlatforms()` 是比 CHECK 更前置、更隐蔽的硬门槛：账号能建、分组能建、关联能挂，但平台不在此列表则账号永远进不了调度池 → 请求"无可用账号"。

### 5. 调度隔离
- `matchingPlatforms(groupPlatform)`（`channel_service.go:358`）：concrete 平台返回自身；composite 列表硬编码 `[anthropic, openai, gemini, antigravity, grok]`，**不含新平台** → 新平台默认不被 composite 选中（天然隔离）。
- 建独立 group（`platform=新平台`）+ `account_groups` 挂账号。
- **调度快照门槛（必查，否则账号进不了调度池）**：`schedulerSnapshotPlatforms()`（`scheduler_snapshot_service.go`）返回平台列表，被全量重建 `schedulerCanonicalBuckets`、增量重建 `rebuildByGroupIDs`、bulk 事件遍历。**新平台必须加入此列表**，否则账号 `Schedulable:true` + 关联分组都正常，请求仍选不到账号 → "无可用账号"。同文件 bulk event switch case（`case PlatformAnthropic, ..., PlatformGrok:`）也要加新平台走精确快速路径。

### 6. admin 导入接口（若需 UI 建账号）
- 通用 `POST /admin/accounts` 的 platform 字段无后端枚举校验，技术上可直接发新 platform。
- 但若新平台凭证有特殊处理（如 SSO 归一、不走某兑换链路），建**独立导入接口**（独立 handler + 独立路由组），与现有链路物理隔离。
- **凭证 key 必须与 forwarder 的 `account.GetCredential("xxx")` 对齐**——前后端契约的单一事实源是 forwarder 读取的 key。

### 7. 前端 UI + 后端校验落地清单（缺一就报"选项缺失/400"）
新平台要在**所有带平台选择的 view** + **后端 binding 校验**同步加，不只建账号：

**前端**：
- **建账号** `components/account/CreateAccountModal.vue`：segmented control 加新平台按钮 + 凭证输入子块（`v-if="form.platform === 'xxx'"`，按平台切凭证字段）
- **建分组** `views/admin/GroupsView.vue`：`platformOptions`（建分组 Select）+ `platformFilterOptions`（列表筛选）+ `cell-platform` badge 颜色（三元链加新平台分支，否则 fallback 默认蓝色）
- **图标/标签** `components/common/PlatformIcon.vue`（`v-else-if="platform === 'xxx'"` 加 svg）+ `PlatformTypeBadge.vue`（`platformLabel` + `platformClass`，否则 fallback 'Gemini'/蓝色）
- **types** `types/index.ts`：`AccountPlatform`（:813）+ `GroupPlatform`（:495）联合类型加 `| 'xxx'`
- **i18n**：见下 namespace gotcha

**后端**：
- **建分组/更新分组的 platform oneof 校验** `handler/admin/group_handler.go`：`CreateGroupRequest.Platform` + `UpdateGroupRequest.Platform` 的 `binding:"omitempty,oneof=..."` 列表加新平台，否则前端发新平台值被 gin validator 拦截 → 400 `Field validation for 'Platform' failed on the 'oneof' tag`
- composite `CompositeRouteRequest.TargetPlatform` 的 oneof **不动**（concrete 平台不进 composite 路由，保持隔离，与 §5 一致）
- 通用 `POST /admin/accounts` 的 platform **无** oneof 校验，不必改；新平台若走独立导入接口（如 `POST /admin/grok-search/sso`）更与此无关

> Gotcha：前端 grep 平台名扫 `views/admin/*.vue` + `components/**/*.vue`；后端 grep `oneof=anthropic` 扫所有 platform/target_platform binding tag。两者都要过。

### i18n namespace 定位 gotcha
i18n key（如 `admin.groups.platforms.grok`）的 namespace **不一定有同名文件**：
- `admin.groups` **不在** `i18n/locales/zh/admin/groups.ts`（该文件不存在）。
- 实际在 `i18n/locales/zh/admin/overview.ts` 的 `groups:` section——`admin/index.ts` 用 `...overview` 展开，overview.ts 导出的 `groups` key 成为 `admin.groups`。
- **定位方法**：读 `i18n/locales/<lang>/admin/index.ts` 的 import + 展开顺序，grep 目标 key（如 `createFirstGroup:`）找它在哪个文件的哪个 section。
- 加新平台 i18n：`admin.groups.platforms.xxx`（overview.ts 的 groups section）+ `admin.accounts.*`（accounts.ts，建账号/状态文案）。

### 8. 运行时分发点清单（多套独立分发，漏一处即报错）
新平台在 service/handler 层有**多套互相独立的 platform 分发点**，forwarder（转发）只是其中之一。只接 forwarder 会让"建号/分组/关联都正常，但测试/同步/调度/模型列表某环节报错或返回错平台数据"：

| 分发点 | 文件 | 漏掉的后果 |
|---|---|---|
| **转发分发** | `openai_gateway_forward.go:101` 附近 `if account.Platform == PlatformXxx` | 请求 fallthrough 到别的 forwarder |
| **调度快照分发** | `scheduler_snapshot_service.go` `schedulerSnapshotPlatforms()` + bulk event switch | 账号进不了调度池 → "无可用账号"（见 §5）|
| **测试账号分发** | `account_test_service.go` `TestAccountConnection` 的 if 链 | fallthrough 到 `testClaudeAccountConnection` → 用错凭证打错端点 → "测试"报错 |
| **同步上游模型分发** | `upstream_models.go` `buildUpstreamModelsRequest` switch + `FetchUpstreamSupportedModels` | "同步模型"按钮返回 unsupported；新平台若上游无标准 /v1/models 端点（如网页态 console），应在 `FetchUpstreamSupportedModels` 早期静态返回模型列表，不打上游 |
| **账号可用模型分发** | `handler/admin/account_handler.go` `GetAvailableModels`（GET /admin/accounts/:id/models） | 测试弹窗模型下拉 fallthrough 到末尾 Claude 分支 → 下拉全是 `claude.DefaultModels`（grok_search 实例：测试 grok 账号下拉却是 Claude 模型）。与"同步上游模型"是**两个独立端点**，别只改 `FetchUpstreamSupportedModels` |
| **网关路由层 platform 分发** | `server/routes/gateway.go` `isOpenAIResponsesCompatibleGatewayPlatform` 等 `getGroupPlatform(c)` switch（`/v1/responses`、`/v1/chat/completions`、`/v1/messages` 闭包） | 请求路由到错的 gateway handler：grok_search 漏加 → `/v1/responses` 走 `h.Gateway.Responses`、`/v1/chat/completions` 走 `GatewayService.ForwardAsChatCompletions` → `GetAccessToken` 对 apikey 取 `api_key` → 502 `api_key not found in credentials`（grok_search 凭证是 sso_token）|
| **选号运行时过滤层 platform 分发** | `openai_gateway_scheduling.go` `normalizeOpenAICompatiblePlatform` + `account.go` `IsOpenAICompatible` + `openai_account_runtime_block_fastpath.go` `isOpenAIAccount`（选号过滤 `openai_account_scheduler.go:1354` `account.Platform != normalizeOpenAICompatiblePlatform(req.Platform) \|\| !account.IsOpenAICompatible()`） | 账号进了调度快照池（建桶用真实 `account.Platform`）但**选号取桶/过滤用归一化后的 platform**：归一函数漏新平台 → 被归一成 default platform → 建桶/取桶平台 key 错位 + `IsOpenAICompatible`/`isOpenAIAccount` 漏新平台 → 账号被 `platform_mismatch` 过滤排除 → `pool=0` → 503 `Service temporarily unavailable`（**报错被 `classifyNoAccountError` 包成误导性 503**：DB 诊断查持久状态说"有账号"不返回 404，走 503 fallback，看似"服务不可用"实为"选号过滤把账号排了"）。`isOpenAIAccount` 漏新平台还会让 `BlockAccountScheduling` 对新平台 no-op → 错误处理的内存即时下线失效 |
| **选号 platform 源头（最前置、最隐蔽）** | `handler/openai_gateway_handler.go` `openAICompatibleRequestPlatform`（Responses/ChatCompletions/CountTokens handler 算 `requestPlatform` 的入口，按 `apiKey.Group.Platform` 或 composite `ResolvedTargetPlatform` 归一） | 选号链路 platform 的**最初来源**：handler 入口只认 `PlatformGrok` 返回自身，其他全归 `PlatformOpenAI`。新平台分组在此被归一成 openai → **后面 `normalizeOpenAICompatiblePlatform`/`IsOpenAICompatible` 修得再对，传进来的 platform 已经是 openai，根本到不了选号过滤层** → `listSchedulableAccounts` 按 openai 查桶取不到新平台账号 → `pool=0`。修复：新平台分组/resolved platform 返回自身（与下游归一函数语义对齐）。grok_search 实例：修了选号过滤 3 处仍 `pool=0`，就是卡在源头这层 |

> Gotcha：这多套分发点**互不复用**，各自的 platform 判断独立。落地时逐一 grep：`account.Platform ==` / `case Platform` / `getGroupPlatform` / `IsGrok()` / `IsOpenAICompatible` / `isOpenAIAccount` / `normalizeOpenAICompatiblePlatform` 等，确认新平台在每个分发点都有归属（或走正确 default）。`IsGrok()`/`IsOpenAICompatible()`/`isOpenAIAccount()` 这类便捷谓词只列已知平台，新平台不会命中——若新平台与某平台共享链路，需在分发点显式列新平台，不能依赖 `IsXxx()`。路由层 `isOpenAIResponsesCompatibleGatewayPlatform` 这类 switch 尤其隐蔽：它决定请求进哪个 gateway handler，漏了新平台会让请求进错转发链路（取错凭证/打错端点），且报错信息（如 `api_key not found`）看似与路由无关，易误判为凭证问题。
>
> **最隐蔽的是"选号运行时过滤层"与"调度快照建桶层"的平台 key 割裂**：建桶（`bucketsForPlatform`）用真实 `account.Platform` 建桶，选号取桶/过滤用 `normalizeOpenAICompatiblePlatform(req.Platform)` 归一化后的值。两者必须**同一 platform key**——若归一函数把新平台归一成别的平台（如 default `PlatformOpenAI`），建桶在 `PlatformXxx`、取桶在 `PlatformOpenAI` → `pool=0`。且 `IsOpenAICompatible`/`isOpenAIAccount` 漏新平台会让选号过滤 `openai_account_scheduler.go:1354` 以 `platform_mismatch` 排除账号。**这种"DB 有账号、调度池取不到"的割裂被 `classifyNoAccountError` 包成 503 `Service temporarily unavailable`**（DB 诊断查持久状态说"有账号"不返回 404 → 走 503 fallback），看似"服务不可用/限流冷却"实为"选号过滤把账号排了"。排查 grok_search 类 503 时：先看后端日志 `openai.account_select_failed` 的 `error` 字段——`pool=0` + `excluded_account_count: 0` = 平台归属过滤（非冷却）；带 `supporting model:` 后缀 = 模型/定价筛选；带 `filtered: xxx` = 瞬态过滤（限流/runtime block）。**测试连接能通 ≠ 调度能选到**（测试绕过调度直接打上游，只验凭证有效性，对选号 bug 零诊断价值）。**platform 在选号链路被归一/判定的层级从源到尾有 4 层，全要补新平台**：① handler 源头 `openAICompatibleRequestPlatform`（`requestPlatform` 最初来源）→ ② 选号归一 `normalizeOpenAICompatiblePlatform` → ③ 过滤谓词 `IsOpenAICompatible` → ④ runtime block 守卫 `isOpenAIAccount`。漏①则后面三层修得再对也没用（传进来的 platform 已经是错的）；漏②③④则账号进池却被过滤。**排查顺序：先 SQL 确认 DB 账号满足入桶条件（group 关联 + active + schedulable=true + 无瞬态窗口），再逐层查 platform 在哪一层被归错/排除。**

---

## 案例：grok_search

- 常量 `PlatformGrokSearch`（domain + service re-export）
- forwarder `openai_gateway_grok_search.go`：SSO cookie 走 `console.x.ai/v1/responses`
- 独立错误处理 `handleGrokSearchAccountUpstreamError`（**不触发** grok 的 402 冷却——grok_search 的目标正是绕开 402）
- **不需 migration**（accounts/groups 无 CHECK；quota/monitor/composite 后续按需）
- admin 独立 `POST /admin/grok-search/sso`（`handler/admin/grok_search_handler.go`）：SSO 直接当 cookie（`sso`=`sso-rw`），**不走** grok OAuth 的 `ConvertSSOToBuild` 兑换；凭证 key `sso_token`+`base_url` 对齐 forwarder `GetCredential`
- 请求体契约借鉴本地 grok2api `console/normalize.go`（patchInput `text→input_text`、reasoning effort 归一、注入 `web_search`+`x_search`、删 `background`/`conversation` 等）
- TLS：console.x.ai 在 CF 后，forwarder 用 `DoWithTLS` + Chrome 指纹（见 [upstream-egress.md](./upstream-egress.md)）
- **端到端链路补全（多套分发点）**：除 forwarder 外，另接多处分发——调度快照 `schedulerSnapshotPlatforms()` 加 `PlatformGrokSearch`（否则账号进不了调度池）；测试账号 `TestAccountConnection` 加 grok_search 分支 + `testGrokSearchAccountConnection`（DoWithTLS+Chrome 指纹过 CF，401 不改账号状态）；同步模型 `FetchUpstreamSupportedModels` 对 grok_search 静态返回 `xai.DefaultModelIDs()`（console 网页态无 /v1/models 端点）；**账号可用模型 `GetAvailableModels` 把 grok_search 纳入 Grok 分支**（`PlatformGrok || PlatformGrokSearch`），否则测试弹窗下拉 fallthrough 到末尾 Claude 分支、全是 Claude 模型；**网关路由层 `isOpenAIResponsesCompatibleGatewayPlatform` 加 `PlatformGrokSearch`**（gateway.go），否则 `/v1/responses`、`/v1/chat/completions` 进错 gateway handler → `GetAccessToken` 对 apikey 取 `api_key` → 502 `api_key not found in credentials`（grok_search 凭证是 sso_token）；**选号运行时过滤层 `normalizeOpenAICompatiblePlatform` + `IsOpenAICompatible` + `isOpenAIAccount` 三处加 `PlatformGrokSearch`**（openai_gateway_scheduling.go / account.go / openai_account_runtime_block_fastpath.go），否则建桶(PlatformGrokSearch)与取桶(归一成 PlatformOpenAI)key 错位 + 选号过滤 :1354 `platform_mismatch` 排除 → `pool=0` → 503 `Service temporarily unavailable`（被 `classifyNoAccountError` 包成误导性 503，`isOpenAIAccount` 漏还会让 `BlockAccountScheduling` no-op → `tempUnscheduleGrokSearch` 内存即时下线失效）。ops 监控后端动态按 `acc.Platform` 聚合，前端 `OpsDashboardHeader` 加 grok_search 筛选即可。
- **/v1/chat/completions 桥接（账号级开关）**：grok_search 上游是 console.x.ai/v1/responses（responses 格式），不吃 chat completions 的 messages。默认 `/v1/chat/completions` 被拒（引导 `/v1/responses`）；账号 Extra `grok_search_chat_completions=true` 时走 `forwardGrokSearchChatCompletionsViaResponses` 桥（仿 grok 桥 `forwardGrokChatCompletionsViaResponses`，但凭证用 sso_token、端点复用 `buildGrokSearchRequest`+console.x.ai、TLS 用 `DoWithTLS`+Chrome 指纹、错误用 `handleGrokSearchAccountUpstreamError`，响应复用 `handleChatStreamingResponse` 输出 chat 格式）。桥方法见 `openai_gateway_grok_search.go`，分发分支在 `ForwardAsChatCompletions`。**前端必须显式暴露开关**：`CreateAccountModal` + `EditAccountModal` 的 grok_search 专属凭证块加 Toggle（`grokSearchChatCompletionsEnabled` ref），创建/编辑时写入/合并账号 Extra `grok_search_chat_completions`（关闭则 `delete` key）；后端有开关但前端漏 UI 会让用户"看不到开关"无法启用——账号级 Extra 开关属跨层契约（后端读 `account.Extra`、前端写 `extra`），建/编账号表单都要覆盖，否则开关只在后端存在而前端不可控。
- **effort 后缀剥离（纯后端，无 UI/调度/model_mapping 改动）**：部分客户端（如 smart Research）只能填模型名、无法单独传 `reasoning.effort`。grok_search 的 multi-agent 靠 `reasoning.effort` 触发不同 Agent 协同规模（low/medium=4 Agent，high/xhigh=16 Agent，effort 不是模型名而是强度档）。允许「真模型名 + effort 后缀」表达强度：`grok-4.20-multi-agent-0309-xhigh` → 转发阶段 `splitGrokSearchEffortSuffix` 剥离后缀得真模型 `grok-4.20-multi-agent-0309` + 注入 `reasoning.effort=xhigh`。支持后缀 `-low/-medium/-high/-xhigh/-max`（`-max` 经 `normalizeGrokSearchEffort` 映射 `xhigh`，与 normalize 对 max 的既有语义一致）。**effort 优先级**：客户端显式 effort > 后缀剥离值 > 默认 medium（`normalizeGrokSearchReasoning(payload, preferredEffort)`）。**剥离只放转发阶段（选号之后）**：因选号层 `account.IsModelSupported(req.RequestedModel)`（scheduler:1703）用客户端原始模型名匹配，账号无 `model_mapping` 时放行一切（带后缀名能选中）→ 走到 forwarder 才剥离；**不碰调度层/不配映射/不补前端**。两条转发路径都要覆盖：`forwardGrokSearch`（responses 直写）+ `forwardGrokSearchChatCompletionsViaResponses`（chat 桥接），均在算出 `upstreamModel` 后剥离、`billingModel` 不受污染、`OpenAIForwardResult.UpstreamModel` 是剥离后的真模型名。`splitGrokSearchEffortSuffix` 实现：大小写不敏感识别后缀（lower 判定 HasSuffix，base 保留原大小写）、从长到短匹配防御性顺序（各后缀带前导 `-` 天然不冲突，`-high` 不会误吃 `-xhigh`，因 `xxx-xhigh` 末 5 字节是 `xhigh` 而非 `-high`）、后缀须是 `normalizeGrokSearchEffort` 认识的档（`none` 不作后缀——它是显式关闭推理的特殊值）、base 非空校验（整串即后缀如 `-xhigh` 不剥离）。
- **状态码处理（`handleGrokSearchAccountUpstreamError` 决策树，独立于 grok 的 402 冷却）**：grok_search 走 console.x.ai（CF 后），错误处理按状态码 + body 细分，**CF 判定必须最前**（`httputil.IsCloudflareChallengeResponse` 对 403 和 429 都命中——CF 挑战可能以 429 形态出现，CF 是出口/指纹问题绝不能当账号额度/权限去冷却/失效）。决策树：①401 → `markGrokSearchReauthRequired`（SSO 无 refresh，持久 status=error + schedulable=false，管理员重导）；②403 → CF 挑战→**不惩罚账号**（return，交上层既有机制）/ SSO 权限失效（`isGrokSearchPermissionDenied`：`permission-denied`/`permission_denied`/`access to the chat endpoint is denied`）→ markReauthRequired / 其它→不处理；③429 → CF→不惩罚 / 免费额度耗尽（`isGrokSearchFreeQuotaExhausted`：body 含 `free usage quota`）→ `tempUnscheduleGrokSearch(24h)` / 普通瞬时频率限制→ `tempUnscheduleGrokSearch(5min)`；④5xx 非 pool mode→2min。**关键 gotcha**：免费额度识别用 **error 文本 `free usage quota` 而非 `code:resource-exhausted`**——console 的 RPS 速率限流也是 `resource-exhausted` code（error 是 "Too many requests for team... Requests per Second"），单看 code 会把 RPS 限流误判成额度耗尽、错走 24h 长冷却（实际只需 5min）。免费额度 24h 冷却（`grokSearchFreeQuotaCooldown`）对齐 grok2api `defaultFreeQuotaRecoveryPause`——短冷却无效（5min 恢复后又 429 无限循环）。**隔离**：所有逻辑封闭在 `handleGrokSearchAccountUpstreamError` + 2 个 grok_search 专属辅助函数（同文件），复用 utils 层 `httputil.IsCloudflareChallengeResponse`（工具共享非业务耦合，处理决策仍 grok_search 专属），**不照搬** grok2api 的 failure.go/selector 分层 + QuotaRecovery 探测队列，保持 sub2api `switch statusCode` 简洁结构。桥接路径 `forwardGrokSearchChatCompletionsViaResponses` 也调同一错误处理，自动受益。**签名复用**：该函数本就接收 `headers`+`responseBody`（曾被 `_ =` 忽略），本次启用、签名零变更。**对照 grok2api**：grok2api 对 console 的 `Free usage quota exceeded`（`resource-exhausted` 非 RPS 文本）实际也漏判（它的 RPS 模式 + cli 格式免费额度关键词都覆盖不到 console 这个格式），落到兜底普通 429 退避；sub2api 这处比 grok2api 多走一步专门识别。**不做 auto-clean**：sub2api 多用户 SaaS 物理删账号语义过重，失效账号留 status=error 等管理员重导（grok2api 有 auto_clean 定时删，sub2api 不照搬）。
- **创建入口对齐其他 apikey 平台**：前端单账号表单（SSO Token + Console Base URL）走通用 `POST /admin/accounts`，credentials=`{sso_token, base_url}`；通用 Create handler 对 grok_search 的 `sso_token` 调 `xai.NormalizeSSOToken` 归一化（剥离 `sso=`/cookie 整串），放幂等前让归一化值参与幂等 key。专用批量接口 `POST /admin/grok-search/sso` 保留但前端默认不暴露（批量场景仍可复用）。
- **故意不加**：UsersView 用量列 / SubscriptionsView 订阅筛选 / ChannelsView 渠道定价——grok 平台本身在这些处也不全覆盖，SSO 平台不适合订阅/计费语义，与 grok 对齐保持克制。

---

## Common Mistakes

1. **只加 domain 常量，忘 `service/domain_constants.go` re-export** → service 包 `undefined: PlatformXxx`。
2. **新平台复用现有平台的 `handleXxxAccountUpstreamError`** → 带入不想要的冷却/quota 语义（如 grok 的 402 冷却）。
3. **以为建账号必须先 migration** → accounts/groups 无 CHECK，基础可用不需 migration；只有 quota/monitor/composite 表有 CHECK。
4. **凭证 key 前后端不对齐** → 导入接口写的 key 与 forwarder `GetCredential` 读的 key 不一致，账号建成但转发读不到凭证。
5. **新平台接入 composite** → 改 `matchingPlatforms` 的 composite 列表会污染所有 composite 路由；除非明确需要，保持 concrete 隔离。
6. **前端只改建账号，漏建分组/筛选** → 用户报"创建分组没有 X 平台"。新平台前端要覆盖所有带平台选择的 view（建账号 + 建分组 + 筛选 + badge + 图标 + i18n），落地后 grep 平台名扫硬编码。
7. **i18n key 找不到 namespace** → `admin.groups` 不在 admin/groups.ts（不存在），在 `overview.ts` 的 `groups:` section（`admin/index.ts` 用 `...overview` 展开）。追 admin/index.ts 的 import 定位。
8. **后端 group platform 的 gin oneof 校验漏加新平台** → 前端建分组发了新 platform，后端 validator 拦截 400 `Field validation for 'Platform' failed on the 'oneof' tag`。前端加选项不够，`handler/admin/group_handler.go` 的 `CreateGroupRequest`/`UpdateGroupRequest` `oneof=...` 也要同步加新平台（`CompositeRouteRequest.TargetPlatform` 不加，保持隔离）。
9. **调度快照漏平台（最隐蔽）** → accounts/groups 无 CHECK，号能建、分组能挂，但 `schedulerSnapshotPlatforms()` 没加新平台，账号永远进不了调度池 → 请求"无可用账号"。这是比 CHECK 更前置的门槛，§4 CHECK 表会让人误以为"无 CHECK = 基础可用"，实际还要过调度快照。修复：`schedulerSnapshotPlatforms()` 加新平台 + bulk event switch case 加新平台。
10. **测试账号 fallthrough 到错误平台** → `TestAccountConnection` 分发器是独立 if 链（不复用 forwarder 分发），漏加新平台分支会 fallthrough 到 `testClaudeAccountConnection`，用错凭证打错端点。新平台测试方法若需特殊 TLS（如 CF 后的 Chrome 指纹），必须用 `DoWithTLS`+profile，不能用裸 `Do`；且测试场景**不改账号状态**（不调 markReauth/tempUnschedule，避免探测污染调度）。
11. **改 `schedulerSnapshotPlatforms()` 返回类型从数组到切片，漏改调用点 `[:]`** → `platforms[:]` 是数组切片语法，返回值改成 `[]string` 后编译失败。改平台列表返回类型时，grep 所有 `platforms[:]` 调用点同步改成 `platforms`。
12. **测试弹窗模型下拉全是 Claude（GetAvailableModels 漏平台）** → `GetAvailableModels`（`handler/admin/account_handler.go`，GET /admin/accounts/:id/models）是独立分发点，与"同步上游模型"的 `FetchUpstreamSupportedModels` 是**两个端点**。漏加新平台分支会 fallthrough 到末尾 Claude 分支，测试弹窗模型下拉全是 `claude.DefaultModels`。修复：把新平台在 Grok/Gemini/OpenAI/Antigravity/Claude 任一合适分支纳入（grok_search 归 Grok 分支：`PlatformGrok || PlatformGrokSearch`）。
13. **新平台凭证需预处理时，通用创建接口要加平台 hook** → 通用 `POST /admin/accounts`（`account_handler.go` Create）对 credentials 原样透传（`req.Credentials → service.CreateAccountInput.Credentials`），不归一化、不校验 key。若新平台凭证需预处理（如 grok_search 的 SSO token 要剥离 `sso=`/cookie 整串，否则 forwarder 拼 `Cookie: sso=<token>` 出现双 `sso=` 认证失败），必须在 Create handler 内为新平台加归一化 hook（复用 `xai.NormalizeSSOToken` 等），放幂等前让归一化值参与幂等 key。**批量导入接口的归一化不等于通用创建接口的归一化**——两条链路独立，别只补一处。
14. **网关路由层 platform 判断漏新平台（报错信息误导）** → `server/routes/gateway.go` 的 `isOpenAIResponsesCompatibleGatewayPlatform` 等 `getGroupPlatform(c)` switch 决定 `/v1/responses`、`/v1/chat/completions`、`/v1/messages` 进哪个 gateway handler。漏加新平台 → 请求进错转发链路：grok_search 漏加后 `/v1/chat/completions` 走 `GatewayService.ForwardAsChatCompletions` → `GetAccessToken` 对 apikey 取 `api_key` → 502 `api_key not found in credentials`。**报错信息（api_key not found）看似凭证问题，实为路由层把请求送进了取错凭证的链路**——排查"凭证类"报错时先确认路由层是否把新平台送进了正确 gateway handler。修复：路由层 switch 加新平台，放行/拒绝逻辑下沉到对应 service 的 platform 分支。
15. **responses 格式上游不支持原生 chat completions** → 若新平台上游只暴露 responses 端点（如 grok_search 的 console.x.ai/v1/responses），其 forwarder 只吃 responses body（`input`/`instructions`），不吃 chat completions 的 `messages`。直接让 `/v1/chat/completions` 复用该 forwarder 会失败。两种处理：(a) 路由层/service 层拒绝 chat completions、引导用 `/v1/responses`；(b) 写 chat↔responses 桥（`apicompat.ChatCompletionsToResponses` 转 body + 转发 + `ResponsesToChatCompletions`/`handleChatStreamingResponse` 转回 chat 响应），用账号级 Extra 开关控制是否启用（默认拒绝，开关开才桥接）。grok 平台已有 `forwardGrokChatCompletionsViaResponses` 桥可参照结构，但凭证/端点/TLS/错误处理要按新平台独立替换（不能复用 grok 的 getRequestCredential/buildGrokResponsesRequest/裸Do/handleGrokAccountUpstreamError）。
16. **选号过滤层平台归属漏新平台（503 误导性报错）** → 账号进了调度快照池（`schedulerSnapshotPlatforms()` + `bucketsForPlatform` 用真实 `account.Platform` 建桶都补了），但选号运行时过滤层三处遗漏：`normalizeOpenAICompatiblePlatform`（归一函数把新平台归一成 default `PlatformOpenAI`）、`IsOpenAICompatible`、`isOpenAIAccount`。后果：建桶在 `PlatformXxx`、选号取桶用归一后的 `PlatformOpenAI` → key 错位；且过滤条件 `openai_account_scheduler.go:1354` `account.Platform != normalizeOpenAICompatiblePlatform(req.Platform) || !account.IsOpenAICompatible()` 两条件都不满足 → 账号被 `platform_mismatch` 排除 → `pool=0` → 503 `Service temporarily unavailable`。**这 503 极具迷惑性**：`classifyNoAccountError` 调 `DiagnoseModelAvailabilityForPlatform` 查 DB 持久状态（`ignoreTransientState=true`）说"有账号 + 支持模型"→ 不返回 404 → 走 503 fallback，看似"服务暂时不可用/限流冷却"，实为"选号过滤把账号排了"。`isOpenAIAccount` 漏新平台还会让 `BlockAccountScheduling` 对新平台 no-op → 新平台错误处理（如 `tempUnscheduleGrokSearch`）的内存即时下线失效（只能靠 DB `SetTempUnschedulable` + 快照重建，有延迟）。**排查指引**：看后端日志 `openai.account_select_failed` 的 `error`——`pool=0` + `excluded_account_count: 0` = 平台归属过滤（非冷却）；测试连接能通 ≠ 调度能选到（测试绕过调度直接打上游）。修复：归一函数让新平台返回自身（与建桶 key 一致）+ 两个谓词加新平台。
17. **模型名后缀映射只能在转发阶段做，不能想当然改调度选号** → 要让「模型名 + effort 后缀」（如 `grok-4.20-multi-agent-0309-xhigh`）拆成「真模型 + effort」，剥离只能放 forwarder 归一化层（选号之后）。**原因**：选号层 `account.IsModelSupported(req.RequestedModel)`（scheduler:1703）用客户端原始模型名匹配，账号无 `model_mapping` 时 `IsModelSupported` 返回 `true` 放行一切（带后缀名照样选中）→ 才能走到 forwarder 剥离。若误把后缀识别/模型改写放进调度层，反而要在选号阶段凭空构造"该账号支持哪些后缀名"，徒增复杂度且与 `model_mapping` 语义重叠。**effort 不是模型名**：multi-agent 的 `xhigh`/`high` 是 `reasoning.effort` 档（Agent 协同规模），不要把 `grok-4.20-multi-agent-xhigh` 当真实模型加进 `xai.DefaultModels`（会让默认模型清单混入虚拟名）。正确做法是后缀剥离（`splitGrokSearchEffortSuffix`）+ effort 注入（`normalizeGrokSearchReasoning` 的 preferredEffort），客户端填真模型名拼后缀即可，零调度/映射/UI 改动。
18. **状态码错误处理：CF 判定必须最前 + 免费额度按 error 文本而非 code 识别** → grok_search 走 console.x.ai（CF 后）的错误处理 `handleGrokSearchAccountUpstreamError`，两个易错点：①**CF 判定顺序**：`httputil.IsCloudflareChallengeResponse` 对 403 **和** 429 都会命中（CF 挑战常以 429 形态出现），CF 是出口/指纹问题**绝不能**当账号额度/权限去冷却或失效账号——所以 403 和 429 的 case 里 CF 判断必须是第一个、命中即 `return`（不 fallthrough 到额度/权限/限流逻辑）。若把 CF 判断放后面，CF 挑战会被误判成"免费额度耗尽"或"权限失效"，对账号错误冷却/失效。②**免费额度识别用 error 文本 `free usage quota`，不能单看 `code:resource-exhausted`**：console 的 RPS 速率限流也是 `resource-exhausted` code（error 是 "Too many requests for team... Requests per Second"），单看 code 会把 RPS 限流误判成额度耗尽、错走 24h 长冷却（实际只需 5min 短退避）。**对照坑**：grok2api 对 console 的 `Free usage quota exceeded`（`resource-exhausted` 非 RPS 文本）实际也漏判——它的 RPS 模式（`Requests per Second (actual/limit)`）和 cli 格式免费额度关键词（`subscription:free-usage-exhausted`/`used all the included free usage for model`）都覆盖不到 console 这个格式，落到兜底普通 429 退避。接入新平台错误处理时不能盲信参考实现（grok2api）的关键词，要按**目标上游（console 网页态）的实际错误格式**专门识别。**隔离原则**：错误处理封闭在平台专属 `handleXxxAccountUpstreamError`（grok_search 不复用 grok 的，避免带入 402 冷却/quota snapshot 语义），复用 utils 层 CF 工具（工具共享非业务耦合），不照搬 grok2api 的 failure.go/selector 分层 + QuotaRecovery 探测队列。

---

## 关联
- [Upstream Egress & TLS Fingerprint](./upstream-egress.md) —— CF 后的上游用 Chrome uTLS 指纹
