# 执行计划：grok_search 对齐上游 console 行为

> 前置：`prd.md`（11 项需求 + 14 条验收）、`design.md`（技术设计）已定稿。
> 改动集中于 `backend/internal/service/` 两个实现文件 + 测试；无 DB/配置变更。

## Step 1：tools 归一族（P0-1 view_image + P1-6 参数透传 + P1-7 原生类型）

- [ ] 1.1 新增 helper `hasGrokSearchFunctionTool(tools []any, target string) bool`（design §2.1）
- [ ] 1.2 `normalizeGrokSearchTools` 改造：签名返回 `bool`（retainedClientTools，含原生类型命中）；
  - web_search：`hasClientViewImage` 判定 + `enable_image_understanding` 兜底 + `enable_image_search` 透传
  - x_search：`from_date`/`to_date` 校验透传 + 倒序成对丢弃
  - 新增 case：七种原生工具类型原样 append + retainedClientTools=true
- [ ] 1.3 调用点 `normalizeGrokSearchRequestBody` 接收返回值（tool_choice 参数暂存，Step 2 接管）
- [ ] 1.4 新建 `openai_gateway_grok_search_tools_test.go`：tools-viewimage / tools-image-search / xsearch-dates / native-tools 四组（AC1/AC2/AC5/AC6/AC7）

**验证**：`go test ./internal/service/ -run 'GrokSearch' -count=1`

## Step 2：tool_choice 收紧（P0-2）

- [ ] 2.1 新增 `normalizeGrokSearchToolChoice(payload map[string]any, retainedClientTools bool)`（design §2.2 规则矩阵）
- [ ] 2.2 `normalizeGrokSearchRequestBody` 原"缺省补 auto"逻辑替换为调用新函数
- [ ] 2.3 补 tool-choice 测试组（AC3：required±工具 ×2、function 对象±工具 ×2、嵌套 name、none/auto、未知串、非 function map）

**验证**：同 Step 1 命令；对照上游 `normalize.go:355-399` 自查规则矩阵（审查门）

## Step 3：input patch + effort auto + 常量注释（P0-3 / P0-4 / P1-8 / P1-9）

- [ ] 3.1 `patchGrokSearchInput` 加 reasoning 分支 + 新函数 `patchGrokSearchReasoningContent`（design §2.3）+ reasoning-patch 测试（AC8）
- [ ] 3.2 `normalizeGrokSearchEffort` 加 `auto→auto` + effort-auto 测试（AC9；确认 splitGrokSearchEffortSuffix 后缀表不变）
- [ ] 3.3 `grokSearchDefaultMaxOutputTokens` → `1_000_000`，注释含"勿放大"警示 + max-tokens 测试（AC4）
- [ ] 3.4 注释修正：错误处理 doc"24h"→"30 天"；`normalizeGrokSearchRequestBody` doc 补新能力 + 注入分叉说明（AC14）

**验证**：grep 无残留 `2_000_000` 与误导"24h"；跑相关测试

## Step 4：DPoP 族（P0-5 出口一致性 + P1-10 时钟偏差）

- [ ] 4.1 `fetchGrokSearchDPoPSession` / `manager.get` 增加 `proxyURL` 参数，mint 走同代理（design §3.1）
- [ ] 4.2 `grokSearchDPoPSessionCacheKey` 加入 proxyURL 段
- [ ] 4.3 `grokSearchDPoPSession` 增加 `clockSkew` 字段
- [ ] 4.4 新增 helper `grokSearchDPoPClockSkewFromDateHeader` + `grokSearchDPoPProofIAT`（design §3.2，照抄上游算法）
- [ ] 4.5 mint 记录 localBefore/localAfter、算 skew 存 session；`applyGrokSearchDPoPAuthorization` iat 改用 helper（expiresAt 仍本地墙钟）
- [ ] 4.6 dpop 测试扩充：egress-consistency（mint 经代理断言、缓存键分出口）+ clock-skew 四组（AC10/AC11）

**验证**：`go test ./internal/service/ -run 'GrokSearchDPoP' -count=1`

## Step 5：429 精确冷却（P1-11）

- [ ] 5.1 新增纯函数 `grokSearchRetryAfterFromHeader` / `grokSearchRetryAfterFromBody`（design §4.1，移植上游）
- [ ] 5.2 `handleGrokSearchAccountUpstreamError` 429 瞬时限流分支：头优先→body 次之→clamp [1min,24h]→5min 兜底（design §4.2）
- [ ] 5.3 测试连接路径 `applyGrokSearchTestAccountErrorState` 429 分支同语义更新（复用纯函数，遵守 spec §5.1 内联约定）
- [ ] 5.4 retry-after 测试组：头（秒/HTTP 日期）、body "Resets in: 3m 4s"、皆无→5min、超大 clamp、免费额度仍 30d（AC12）

**验证**：`go test ./internal/service/ -run 'GrokSearch' -count=1`（含 account_test_service 相关）

## Step 6：全量收口

- [ ] 6.1 `go build ./...` 通过
- [ ] 6.2 `go test ./internal/service/ -count=1` 全绿（重点：既有 body/冷却断言受默认产物变化影响的同步修正，如实报告）
- [ ] 6.3 对照 PRD AC1-AC14 逐条勾验
- [ ] 6.4 spec 更新：`.trellis/spec/backend/grok-paths-isolation.md` §4.2/§5 表格核对补齐（429 瞬时限流精确冷却、mint 同出口、注入分叉说明）——走 trellis-update-spec 流程
- [ ] 6.5 提交（git-commit skill，业务化 message；提交前征询用户）

## 回滚点

- Step 1+2（tools/tool_choice 同函数族）一个 commit；Step 3 一个；Step 4（协议层）独立 commit 单独追溯；Step 5 一个。

## 审查门

- Step 2 后：规则矩阵 vs 上游 `normalize.go:355-399` 逐条自查。
- Step 4 后：确认 3 个调用点（gateway×2 + 测试连接）行为符合 R5（mint 同代理）。
- Step 6.3 AC 全过方可提交。
