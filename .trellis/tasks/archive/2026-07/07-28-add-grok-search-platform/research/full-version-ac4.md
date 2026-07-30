# Research: grok_search 完整版（AC-4）三个子系统现状

- **Query**: 调研 DB migration/CHECK 约束、admin SSO 导入接口、前端建账号 UI 的现状，给出 grok_search 完整版需改/新增点
- **Scope**: internal
- **Date**: 2026-07-29
- **关联**: PRD AC-4（前端建账号 + admin SSO 导入接口）；PoC 已 commit（`openai_gateway_grok_search.go`）

---

## 1. DB migration / platform CHECK 约束

### 1.1 现状代码位置

#### accounts.platform（PRD 核实点）
- **无 CHECK 约束**，已核实。
- `backend/ent/schema/account.go:63-66`：`field.String("platform").MaxLen(50).NotEmpty()` —— 无 `Validate`、无 Enum。
- 现有 `accounts` 表建表 SQL 也无 `CHECK (platform IN ...)`（grep 全部 migrations 仅命中 user_platform_quotas / channel_monitors / channel_monitor_request_templates / composite_model_routes，无 accounts）。
- **结论**：accounts 直接可写 `grok_search`，无 migration 需求。

#### groups.platform
- **无 CHECK 约束**。
- `backend/ent/schema/group.go:77-79`：`field.String("platform").MaxLen(50).Default(domain.PlatformAnthropic)` —— 无 Validate。
- **结论**：groups 直接可写 `grok_search`，无 migration 需求。

#### user_platform_quotas.platform（quota 相关，PoC Out of Scope，完整版可能要）
- **有 CHECK 约束**（双重）：
  - DB 层：`backend/migrations/142_user_platform_quotas.sql:8` `CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity'))` → `157_user_platform_quotas_add_grok.sql:14-16` 扩为 `CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok'))`。
  - ent 层：`backend/ent/schema/user_platform_quota.go:40-49` `Validate` 函数 switch case 同步枚举（注释明确指出"需与 service.AllowedQuotaPlatforms 保持同步"）。
  - 权威源：`backend/internal/service/domain_constants.go:53-59` `AllowedQuotaPlatforms = {anthropic, openai, gemini, antigravity, grok}`，被 `IsAllowedQuotaPlatform`（:62）、`setting_features.go:932,945`、`setting_update.go:488`、handler/admin `user_handler.go:723,745,865-868` 与 `setting_handler_audit.go:758` 使用。

#### channel_monitors.provider / channel_monitor_request_templates.provider
- **有 CHECK 约束**：
  - `backend/migrations/125_add_channel_monitors.sql:31` 原始 `CHECK (provider IN ('openai', 'anthropic', 'gemini'))`。
  - `backend/migrations/128_add_channel_monitor_request_templates.sql:24` 同上。
  - `backend/migrations/176_channel_monitor_grok_provider.sql` 用 `pg_get_constraintdef` 检测 + DROP IF EXISTS + ADD 幂等模式扩为 `CHECK (provider IN ('openai', 'anthropic', 'gemini', 'grok'))`（两张表都改）。
  - ent 层：`channel_monitor.go:38` 和 `channel_monitor_request_template.go:42` 用 `Values(...)` enum。
  - 测试：`migrations/channel_monitor_grok_provider_migration_test.go:17` 校验 SQL 文本。

#### composite_model_routes.target_platform
- **有 CHECK 约束**：`backend/migrations/172_composite_model_routes.sql:17` `CHECK (target_platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok'))`。
- PoC/PRD 明确 composite 接入是完整版后续项（不立即需要）。

### 1.2 grok_search 完整版需新增的 migration

> 取决于完整版是否启用对应功能；按 PRD Out of Scope 列表，channel monitor / quota 在完整版才接。

| 表 / 功能 | 是否需 migration | SQL 模式（仿 157/176 幂等 DROP+ADD） |
|---|---|---|
| `accounts.platform` | 否 | — |
| `groups.platform` | 否 | — |
| `user_platform_quotas.platform` | **完整版接 quota 时需要** | `ALTER TABLE user_platform_quotas DROP CONSTRAINT IF EXISTS user_platform_quotas_platform_check; ALTER TABLE user_platform_quotas ADD CONSTRAINT user_platform_quotas_platform_check CHECK (platform IN ('anthropic', 'openai', 'gemini', 'antigravity', 'grok', 'grok_search'));` |
| `channel_monitors.provider` | **完整版接监控时需要** | 同 176 模式，加 `grok_search`（provider 语义上是"调度平台"） |
| `channel_monitor_request_templates.provider` | 同上 | 同上 |
| `composite_model_routes.target_platform` | 完整版接 composite 路由时需要 | 仿上 |

**配套代码同步**（仅当 migration 启用时）：
- 若放开 `user_platform_quotas`：同步修改 `ent/schema/user_platform_quota.go:44` 的 switch case + `service/domain_constants.go:53` 的 `AllowedQuotaPlatforms`（注释已明确这是构建期+运行时双源）。
- 若放开 `channel_monitors`：同步修改 `ent/schema/channel_monitor.go:38` 和 `channel_monitor_request_template.go:42` 的 `Values(...)`。

**migration 编号建议**：现有最新看到 `181_add_group_account_filter.sql`，新 migration 用 `182_*` 或更高（实际取 `ls migrations | sort -V | tail`）。

---

## 2. admin SSO 导入接口

### 2.1 现状代码位置

#### 通用建账号入口（POST /api/v1/admin/accounts）
- 路由：`backend/internal/server/routes/admin.go:356` `accounts.POST("", h.Admin.Account.Create)`。
- handler：`backend/internal/handler/admin/account_handler.go:822` `AccountHandler.Create`。
- 请求体：`CreateAccountRequest`（同文件 `:113-131`），关键字段：
  ```go
  Platform    string         `json:"platform" binding:"required"`           // 任意字符串，无枚举校验
  Type        string         `json:"type" binding:"required,oneof=oauth setup-token apikey upstream bedrock service_account"`
  Credentials map[string]any `json:"credentials" binding:"required"`
  ```
- service 层：`backend/internal/service/admin_account.go:516` `adminServiceImpl.CreateAccount` —— `normalizeOpenAILongContextBillingExtra` + `normalizeGrokMediaEligibilityExtra` + 自动绑默认分组（`{platform}-default`）+ `accountRepo.Create` + `BindGroups`。
- 关键点：**通用入口的 platform 字段无后端枚举校验**，技术上已可直接发 `platform="grok_search"` + `credentials={"sso_token":...,"base_url":...}` 建 grok_search 账号；但没有任何 grok_search 专属校验/归一化（sso_token 必填、格式、base_url 默认值）。

#### 现有 Grok SSO 导入入口（POST /api/v1/admin/grok/sso-to-oauth）
- 路由：`backend/internal/server/routes/admin.go:468` `grok.POST("/sso-to-oauth", h.Admin.GrokOAuth.CreateAccountsFromSSO)`。
- handler：`backend/internal/handler/admin/grok_oauth_handler.go:306` `CreateAccountsFromSSO`。
- 完整调用链：
  1. `normalizeSSOImportTokens`（`grok_oauth_handler.go:461`）—— 对每行调 `xai.NormalizeSSOToken`（去前缀/取 `sso=`/`sso-rw=` 名值对/剥控制字符）。
  2. 并发（`grokSSOImportConcurrency=3`）调 `safeCreateAccountFromSSOToken` → `createAccountFromSSOToken`（`:370`）。
  3. **`grokOAuthService.ConvertFromSSO(ctx, token, req.ProxyID)`**（`service/grok_oauth_service.go:179`）→ `oauthClient.ConvertSSOToBuild`（`repository/grok_oauth_client.go:93`）—— **SSO→OAuth token 兑换（远程调用）**。
  4. `grokOAuthService.BuildAccountCredentials(tokenInfo)`（`grok_oauth_service.go:218`）—— 把 OAuth token 转 credentials map（`access_token`/`refresh_token`/`expires_at`/...）。
  5. `adminService.CreateAccount` with `Platform: service.PlatformGrok, Type: service.AccountTypeOAuth`。
- **不适用 grok_search**：PRD/design 明确 grok_search 不走 `ConvertSSOToBuild`（SSO 直接作为 cookie 用，不兑换 OAuth token）。

#### NormalizeSSOToken（PRD 指定复用）
- `backend/internal/pkg/xai/sso_device.go:344-363`：
  ```go
  func NormalizeSSOToken(value string) string {
      // TrimSpace → 剥 "cookie:" 前缀 → 按 ";" 分隔找 "sso=" 或 "sso-rw=" 名值对
      // → 命中则返回 sanitizeSSOToken(token)
      // 未命中名值对则按整段处理（取第一个 ";" 前）
  }
  ```
- 用法示例：`grok_oauth_handler.go:472` `xai.NormalizeSSOToken(token)`。
- **grok_search 录入 SSO 应复用此函数归一化**（PRD REQ-3 已指出）。

#### 前端 API 客户端
- `frontend/src/api/admin/grok.ts:164` `createFromSSO(payload)` → `POST /admin/grok/sso-to-oauth`（带超时计算 `getGrokSSOImportTimeout`）。
- `frontend/src/api/admin/index.ts:56` barrel export `grok: grokAPI`。

### 2.2 grok_search 完整版需新增（两种方案）

> 决策权在 main agent；research 仅列出选项与各自校验点。

**方案 A — 新建独立 SSO 导入接口（与 grok OAuth 物理隔离，推荐）**
- 新路由：`POST /api/v1/admin/grok-search/sso`（或挂在现有 grok 组下，如 `/grok-search/sso-import`）。
- 新 handler 方法：建议放 `handler/admin/grok_oauth_handler.go`（已含 SSO 导入基础设施）或新建 `grok_search_handler.go`。
- 实现要点：
  - 复用 `normalizeSSOImportTokens`（已调 `xai.NormalizeSSOToken`）。
  - **不调** `ConvertFromSSO` / `ConvertSSOToBuild` / `BuildAccountCredentials`。
  - 直接构造 `CreateAccountInput{Platform: PlatformGrokSearch, Type: AccountTypeAPIKey, Credentials: {"sso_token": token, "base_url": baseURL || "https://console.x.ai"}}` 调 `adminService.CreateAccount`。
- 校验：`sso_token` 必填非空；`base_url` 可选（默认 `https://console.x.ai`）；`type` 固定 apikey（PoC 决定）；可选批量导入（仿 `GrokSSOToOAuthRequest`）。
- 优势：与现有 grok OAuth 链路完全隔离，回归风险低；校验集中。

**方案 B — 扩展通用建账号入口**
- 前端直接发 `POST /api/v1/admin/accounts` with `platform="grok_search"` + `type="apikey"` + `credentials={"sso_token":...}`。
- 后端需在 `AccountHandler.Create`（`account_handler.go:822`）或 `adminServiceImpl.CreateAccount`（`admin_account.go:516`）加 `platform == grok_search` 分支：
  - 校验 `credentials.sso_token` 非空。
  - 调 `xai.NormalizeSSOToken` 归一化。
  - 注入默认 `base_url` 若缺。
- 劣势：通用路径加平台分支，违背"独立隔离"原则；后续 grok_search 凭证校验都会堆到这里。

**说明**：grok_search forwarder（`openai_gateway_grok_search.go:102-110`）已固定读取 `account.GetCredential("sso_token")` + `GetCredential("base_url")`（默认 `grokSearchDefaultBaseURL`），两种方案的写入凭证键必须对齐这俩 key。

---

## 3. 前端建账号 UI

### 3.1 现状代码位置

#### 平台选项清单（注意：PRD 提到的 settings.ts:33 不是建账号 UI）
- `frontend/src/api/admin/settings.ts:33` 的 `PLATFORMS` 常量（`["anthropic", "openai", "gemini", "antigravity", "grok"]`）**只用于平台配额归一化**（`normalizePlatformQuotasMap` / `sanitizePlatformQuotasMap`，同文件 :36-58），与建账号表单无关。
- 同文件 :20 `export type PlatformType = "anthropic" | "openai" | "gemini" | "antigravity" | "grok"` —— 也只是 quota 配置专用。
- **真正的建账号平台选项**：硬编码在 `frontend/src/components/account/CreateAccountModal.vue:73-163` segmented control：
  ```
  anthropic (line 76) / openai (89) / gemini (114) / antigravity (139) / grok (152)
  ```
  每个 `<button @click="form.platform = 'xxx'">`，没有从常量遍历。

#### 平台类型定义
- `frontend/src/types/index.ts:495` `GroupPlatform = 'anthropic' | 'openai' | 'gemini' | 'antigravity' | 'grok' | 'composite'`
- `frontend/src/types/index.ts:813` `AccountPlatform = 'anthropic' | 'openai' | 'gemini' | 'antigravity' | 'grok'`（无 composite，无 grok_search）
- 这两个类型被 `PlatformIcon.vue` 的 prop（`GroupPlatform`，:45）、`PlatformTypeBadge` 等大量组件引用。

#### 凭证字段按 platform/type 切换
- `CreateAccountModal.vue:1101-1102` `v-if="form.type === 'apikey' && form.platform !== 'antigravity'"` 共用 API Key + base_url 输入区，grok 走这里时 placeholder 切换（:1114 `form.platform === 'grok'`）。
- `CreateAccountModal.vue:3195-3226` 引用 `OAuthAuthorizationFlow` 子组件，按 platform 传 props：
  - `:show-sso-option="form.platform === 'grok'"`（:3213）—— 当前只 grok 显示 SSO 单选项。
  - `:show-refresh-token-option="... || form.platform === 'grok'"`（:3206）。
  - `@import-sso="handleGrokImportSSO"`（:3225）。
- `handleGrokImportSSO`（CreateAccountModal.vue:5419-5489）—— 调 `adminAPI.grok.createFromSSO`（即 `POST /admin/grok/sso-to-oauth`），批量提交 sso_tokens，处理 created/failed 列表。

#### SSO 输入字段实现位置
- `frontend/src/components/account/OAuthAuthorizationFlow.vue:216-272` `v-if="inputMethod === 'sso_cookie'"` 块：
  - `ssoCookieInput` ref（:924）。
  - textarea 多行（每行一个 token），支持批量。
  - `emit('import-sso', ssoCookieInput.value.trim())`（:1068-1069）。
- 触发按钮 `@click="handleImportSSO"`（:261 `:disabled="loading || !ssoCookieInput.trim()"`）。

#### 图标注册机制（硬编码 v-if 链，无插件机制）
- `frontend/src/components/common/PlatformIcon.vue:1-65`：每个平台一个 `<svg v-if="platform === 'xxx'">` 块（anthropic :3 / openai :9 / gemini :15 / antigravity :19 / grok :23 / composite :29），fallback generic icon（:36）。
- 加新平台图标 = 在 :29 后插一个 `<svg v-else-if="platform === 'grok_search'">` 分支（可复用 grok 的 X logo SVG path，或换图标）。
- 关联组件：
  - `PlatformTypeBadge.vue:88-94` `platformLabel` computed 硬编码 if 链（无 grok_search → 默认 `'Gemini'` 标签，必须加）。
  - `PlatformTypeBadge.vue:165-179` `platformClass` 硬编码（无 grok_search → 默认 blue/gemini 色，需加）。
  - `CreateAccountModal.vue:150-162` segmented 按钮（同上）。
- 全项目使用：`PlatformIcon` 被 GroupBadge / PlatformTypeBadge / AccountsView / ChannelsView / GroupsView / ProxiesView / UserAllowedGroupsModal / GroupRateMultipliersModal / GroupRPMOverridesModal / AvailableChannelsTable 引用（19 处），改一处全联动。

### 3.2 grok_search 前端需加的 3 点

#### (a) 平台选项（建账号 segmented control）
- 文件：`frontend/src/components/account/CreateAccountModal.vue:150-162` 后追加第 6 个 `<button @click="form.platform = 'grok_search'">Grok Search</button>`。
- 类型扩展：
  - `frontend/src/types/index.ts:813` `AccountPlatform` 加 `| 'grok_search'`。
  - `frontend/src/types/index.ts:495` `GroupPlatform` 加 `| 'grok_search'`（因 GroupsView 也用 PlatformIcon，且建 group 时也要选 platform）。
- 若 main agent 决策"建账号表单也用 grok_search" → 同步上述类型 + segmented button。

#### (b) SSO token 输入字段
- **决策依赖后端方案**：
  - 若后端走方案 A（独立 `/grok-search/sso` 接口）→ 前端仿 `api/admin/grok.ts:164` 新建 `api/admin/grokSearch.ts`，handler 仿 `handleGrokImportSSO`（CreateAccountModal.vue:5419）。
  - 若后端走方案 B（通用 `/accounts` 接口）→ 前端在 apikey 区块（CreateAccountModal.vue:1102）下加 grok_search 专用 SSO token + base_url 输入框，提交时写 credentials.sso_token。
- UI 选择建议：grok_search 是 `apikey` 类型（不是 oauth-based），不适合放进 `OAuthAuthorizationFlow`（那里都是 OAuth 流程）。建议在 apikey 区块下方加独立子块 `v-if="form.platform === 'grok_search'"`：含 SSO token textarea（多行批量）+ base_url input（默认 `https://console.x.ai`）。
- i18n：`frontend/src/i18n/locales/{en,zh}/admin/accounts.ts` 加 grok_search 相关键。

#### (c) 图标
- 文件：`frontend/src/components/common/PlatformIcon.vue`，在 grok 分支（:23）后或 composite（:29）前插：
  ```vue
  <svg v-else-if="platform === 'grok_search'" :class="sizeClass" viewBox="0 0 24 24" fill="currentColor">
    <!-- 可复用 grok 的 X path，或换 search-oriented 图标 -->
  </svg>
  ```
- 同步更新 `PlatformTypeBadge.vue`：
  - `:88-94` `platformLabel` 加 `if (props.platform === 'grok_search') return 'Grok Search'`。
  - `:165` `platformClass` 加对应色系分支（如 zinc/gray 与 grok 区分）。
- 若想在 GroupsView 平台筛选下拉 / AccountsView 过滤器加 grok_search，需找各自 hardcoded 平台数组（grep `'anthropic'` in views/admin/*.vue）。

---

## Caveats / Not Found

1. **migration 编号未锁定**：现有最高看到 `181_`，但 grep 仅返回部分列表（100/236）；写新 migration 前取 `ls backend/migrations | sort -V | tail`。
2. **完整版是否启用 quota/channel monitor/composite 由 PRD 决定**：本研究仅列出"如启用则需 X"。PRD 当前 Out of Scope（PoC 阶段）明确写了"前端 UI、DB migration、admin 导入接口、SSO 自动保活/重登、channel monitor、quota"——但 AC-4 完整版范围由 main agent/PRD 收敛。
3. **grok_search SSO 失效检测（AC-5）**不在本调研范围；service 层 `openai_gateway_grok_search.go:437` 已有 `tempUnscheduleGrokSearch(... "grok_search sso token unauthorized (re-import required)")`，401 时临时停调度 10 分钟。完整版的"标记需重认证"流程需单独调研 admin UI 侧的 reauth 显示。
4. **settings.ts:33 不是建账号 UI 的 platform 选项源**：PRD 描述把 settings.ts:33 当成"前端 platform 选项清单"，实际它只服务 quota 设置；真正建账号的硬编码在 CreateAccountModal.vue。这点已在 3.1 澄清。
5. 未验证：方案 A vs B 的最终选择由 main agent 决策；本调研不预设倾向（仅列出各自校验点）。
