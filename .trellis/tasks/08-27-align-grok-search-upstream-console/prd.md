# grok_search 对齐上游 grok2api console 行为（P0 兼容修复 + P1 能力补齐）

## Goal

将 sub2api 的 grok_search（console.x.ai/v1/responses 通道）与上游 grok2api console 实现（`62d2775c`，v3.1.5）在**请求契约**、**DPoP 协议健壮性**、**出口一致性**三个维度对齐，消除兼容性/风控风险、补齐搜索参数与工具透传能力，并修正 sub2api 内部文档与代码不符的残留。

## 背景

sub2api 的 grok_search 参照 grok2api 开发，但参照的是 2026-08-05 之前的快照。经逐行对比并按 sub2api 当前代码（含 `a54e8918c` 冷却延长提交）两轮复核，确定以下差距需在本任务落地：

| # | 来源 | 差距 | 风险级别 |
|---|---|---|---|
| P0-1 | grok2api `a608c209` | 无 view_image 撞名兜底：注入的 web_search 恒 `enable_image_understanding:true`，Codex 类客户端自带同名 function 工具时整单被 mgw 以 duplicate tool definition 拒绝 | 高（请求级故障） |
| P0-2 | grok2api `normalize.go:355-399` | tool_choice 不做收紧校验，`required`/function 对象直接透传给上游 | 中 |
| P0-3 | 本仓库决策 | `grokSearchDefaultMaxOutputTokens=2_000_000` 与上游 multi-agent 实际值 `1_000_000` 不符（历史抄录错误） | 低 |
| P0-4 | 本仓库遗留 | 错误处理注释仍写"长冷却 24h"（实际 `a54e8918c` 已改为 30 天），误导维护者 | 低（仅注释） |
| P0-5 | 本仓库缺陷（对比中发现） | DPoP mint（`/dpop/token`）固定空代理直连，业务请求走账号代理 → 同一 SSO 双出口 IP，配代理账号有 CF 风控风险；且 DPoP 缓存键不含出口标识，换代理后复用旧出口 mint 的 token | 高（配代理账号的风控面） |
| P1-6 | grok2api `dd9183e2` | x_search 时间范围（from_date/to_date）与 web_search 图片搜索开关（enable_image_search）被归一层全部丢弃 | 能力缺口 |
| P1-7 | grok2api `normalize.go:323-328` | 原生 xAI 工具类型（mcp/shell/image_generation/collections_search/file_search/code_execution/code_interpreter）被静默丢弃 | 能力缺口 |
| P1-8 | grok2api `normalize.go:126-129,155-171` | reasoning item 的 content patch 缺失：无 type 的 part 不补 `reasoning_text`（多轮回放场景上游为此专门修复） | 中（协议防御） |
| P1-9 | grok2api `normalize.go:57-59` | effort `"auto"` 档被改写为 medium（上游识别 auto 为合法档原样透传） | 中（档位丢失） |
| P1-10 | grok2api `d33f6b17` | DPoP proof `iat` 用纯本地时钟，无对 mint 响应 Date 头的时钟偏差修正 | 低（防御性） |
| P1-11 | grok2api `adapter.go:323-336` 等 | 429 瞬时频率限流固定冷却 5min；上游读 `Retry-After` 头 + body `"Resets in: 3m 4s"` 精确调度 | 低（调度效率） |

## Requirements

### R1（P0-1）view_image 撞名兜底
- 归一 tools 时检测客户端是否携带名为 `view_image` 的 function 类型工具。
- 命中时：web_search 的 `enable_image_understanding` 默认置为 `false`；即使客户端显式传 `true` 也强制忽略——与上游语义一致（xAI 服务端在开启 image understanding 时会附带同名 view_image 工具，撞名即整单拒绝）。
- 未命中时：维持现行为（默认 true、尊重客户端显式值）。

### R2（P0-2）tool_choice 收紧策略
- 无 tools：删除 tool_choice。
- 字符串 `none`/`auto`：保留（归一为小写）。
- 字符串 `required`：仅在保留了**客户端工具**（function 或 R7 原生类型）时放行，否则降级 `auto`。
- 对象 `{type:"function", name:...}`（name 兼容嵌套 `function.name` 取法）：同上条件，且归一为 `{type,name}` 标准形态。
- 其余未知取值：降级 `auto`。

### R3（P0-3）max_output_tokens 回正
- 常量改为 `1_000_000`，注释说明取自上游 catalog multi-agent 实际值，勿放大。

### R4（P0-4）过时注释修正
- `handleGrokSearchAccountUpstreamError` doc 注释中"长冷却 24h"改为与 `grokSearchFreeQuotaCooldown`（30 天）一致的表述。

### R5（P0-5）DPoP 出口一致性
- mint 请求必须与业务请求走同一出口：`fetchGrokSearchDPoPSession` 接收并使用调用链的 `proxyURL`（不再固定空串）。
- DPoP session 缓存键加入出口标识（proxyURL），不同出口不共用 session；换代理即换 session。
- 三个调用点（gateway ×2 + 测试连接 ×1）经 `doGrokSearchDPoPRequest` 自动获得该行为，外部签名不变。

### R6（P1-6）搜索参数透传
- `x_search`：透传 `from_date` / `to_date`。校验：非 string 或空串丢弃；严格 `YYYY-MM-DD`（parse 后回写比对全等）才透传；两者齐备且 from > to 时成对丢弃（避免上游 400）。
- `web_search`：透传布尔字段 `enable_image_search`（缺省不写该键）。

### R7（P1-7）原生工具类型透传
- 归一白名单增加：`mcp`、`shell`、`image_generation`、`collections_search`、`file_search`、`code_execution`、`code_interpreter`——原样保留整个 tool 对象。
- 这些类型计为"客户端工具"，参与 R2 的 tool_choice 放行判定。

### R8（P1-8）reasoning item content patch
- `patchGrokSearchInput` 增加 reasoning item 分支：item `type=="reasoning"` 时遍历其 content，无 `type` 但有 `text` 的 part 补 `type:"reasoning_text"`。

### R9（P1-9）effort auto 档透传
- `normalizeGrokSearchEffort` 增加 `auto → auto`：客户端显式 `reasoning.effort:"auto"` 原样透传，不落 medium 兜底。
- 模型名 effort 后缀剥离（`splitGrokSearchEffortSuffix`）不支持 `-auto` 后缀，维持不变。

### R10（P1-10）DPoP iat 时钟偏差修正
- mint 时记录请求前后本地时间，从响应 `Date` 头学习 clockSkew（`(serverDate − RTT中点本地时间)` 秒级取整）；Date 缺失或不可解析按 0。
- session 增加 `clockSkew` 字段（始终有定义值）。
- proof 的 `iat = (localNow + clockSkew)` 的 Unix 秒；缓存过期判定继续走本地墙钟。

### R11（P1-11）429 瞬时限流精确冷却
- 新增两个纯函数（移植上游）：`Retry-After` 头解析（整数秒或 HTTP 日期）、body `"Resets in: 3m 4s"` 解析（`(\d+)\s*([dhms])` 累加）。
- 429 非 CF、非免费额度分支：取头优先、body 次之的时长，clamp 到 [1min, 24h]；无信号维持 5min 兜底。
- 免费额度 30d、CF 不惩罚等既有语义不变。

### R13（本仓库实测修正，2026-09-12 追加）免费额度耗尽改持久标记
- 背景：30d 冷却的依据是「实测额度按月重置」（`a54e8918c`）；后续实测 >2 个月额度仍未恢复，重置周期不可预期，到期回池只是周期性制造 429 探测。
- 429 + `isGrokSearchFreeQuotaExhausted`：改调 `markGrokSearchQuotaExhausted` —— `SetError` 持久标记（status=error + schedulable=false），不再自动回池；管理员充值/换号/确认额度恢复后手动恢复账号。
- 转发链路（`handleGrokSearchAccountUpstreamError`）与测试连接路径（`account_test_service.go` 429 分支）同步。
- `grokSearchFreeQuotaCooldown` 常量删除（无消费方），相关注释同步。
- reason 文案与 401 重认证区分：`grok_search free usage quota exhausted; purchase credits or replace account`。

## 明确不做（Non-goals）

- ❌ 不取消默认注入 web_search/x_search——与上游分叉是刻意的产品决策（grok_search 定位即搜索通道）；仅在代码注释标注分叉及理由。
- ❌ 不做 `response_format` → `text.format`（json_schema 展平）转换——搜索场景不使用结构化输出。
- ❌ 不引入 `/v1/usage` 配额探测/三档同步体系（含 429 后对账）。
- ❌ 不做流式空闲超时、非流式空响应冷却（P2 稳态项，另行立项）。
- ❌ 不改 402 透传语义（grok_search 目标即绕开 402，console 返回 402 按未知透传是刻意设计）。
- ❌ 不做 chat 桥响应侧质量项（reasoning summaries 保留 / sources 去重）——上游在 conversation 层，sub2api 用 apicompat，架构差异大。

## Acceptance Criteria

- [ ] AC1 客户端 tools 含 `view_image` function 时：产出 web_search 的 `enable_image_understanding=false`，且客户端显式 `true` 被忽略。
- [ ] AC2 客户端无 `view_image` 时：默认 true，显式传值被尊重（回归保护）。
- [ ] AC3 tool_choice 输入矩阵（none/auto/required±客户端工具/function对象±客户端工具/嵌套name/未知串/非function对象）行为符合 R2。
- [ ] AC4 max_output_tokens 缺省补 `1000000`。
- [ ] AC5 x_search 合法日期透传、非法格式/空串/倒序区间被（成对）丢弃；不设字段行为不变。
- [ ] AC6 web_search 显式 `enable_image_search` 透传；未设时不产生该键。
- [ ] AC7 七种原生工具类型原样透传且参与 tool_choice 放行判定。
- [ ] AC8 reasoning item 内无 type 有 text 的 part 被补 `reasoning_text`；其它 item 处理不变。
- [ ] AC9 `reasoning.effort:"auto"` 原样发出（不被改写 medium）；不传 effort 仍兜底 medium。
- [ ] AC10 mint 与业务请求使用同一 proxyURL（测试断言 mint 请求带代理）；不同 proxyURL 产出不同缓存键。
- [ ] AC11 mint 响应带 Date 头时 proof 的 iat 反映 skew（含正/负偏差）；Date 缺失按 0 且流程正常。
- [ ] AC12 429 带 `Retry-After` 头或 body `"Resets in"` 时按解析值（clamp 后）冷却；无信号维持 5min；免费额度耗尽走 R13 持久标记（2026-09-12 修订，原 30d 冷却语义作废）。
- [ ] AC13 既有测试全绿（service 包相关测试），`go build ./...` 通过；受默认产物变化影响的既有断言同步修正。
- [ ] AC14 注释/doc 与实际行为一致（1M、30 天、注入分叉、新透传能力说明）。

- [ ] AC15 429 免费额度耗尽后账号被持久标记 error（不自动回池，无到期恢复语义）；测试连接路径同语义；401/CF/瞬时 429 行为不变。

## 已拍板决策

1. max_output_tokens 回正 1M（用户确认）。
2. 免费额度耗尽 = 持久标记 SetError 不回池（2026-09-12 修订：实测 >2 月额度未恢复，原「按月重置→30d 冷却」假设失效）。
3. 保持默认注入搜索工具（平台定位特性，刻意与上游 339617a2 分叉）。

## 参考

- 上游参考仓库：`C:\idealProject\github\grok2api` @ `62d2775c`
  - `backend/internal/infra/provider/console/normalize.go`（tools/tool_choice/x_search 参数/view_image/reasoning patch/auto）
  - `backend/internal/infra/provider/console/dpop.go`（clockSkew：`dpopProofIAT` / `dpopClockSkewFromDateHeader`；缓存键含出口 nodeID）
  - `backend/internal/infra/provider/console/adapter.go`（`normalizeRateLimitResponse`：Retry-After/RPS 解析）
  - `backend/internal/infra/provider/console/dpop_clock_skew_test.go`（测试范式）
- sub2api 现状文件：`backend/internal/service/openai_gateway_grok_search.go`、`openai_gateway_grok_search_dpop.go`
- sub2api spec：`.trellis/spec/backend/grok-paths-isolation.md`（§5.1 内联错误处理约定）、`upstream-egress.md`（CF/uTLS 约束）
