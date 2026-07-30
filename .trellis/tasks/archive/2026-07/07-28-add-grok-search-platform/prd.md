# PRD: 新增 grok_search 平台(SSO + console.x.ai,绕开 multi-agent 402)

## 目标

在 sub2api 新增一个与现有 `grok` **完全隔离**的独立平台 `grok_search`,用 **SSO cookie 走 Grok Console 通道**(`console.x.ai/v1/responses`),让 `grok-4.20-multi-agent` 搜索不再触发现有 OIDC→Responses API 的 `personal-team-blocked:spending-limit`(402)。**单服务(sub2api)内完成,不依赖 grok2api。**

## 背景与已确认事实

- **现有 grok 平台**:走 OIDC access_token + Responses API(`cli-chat-proxy.grok.com`),调 multi-agent 报 402 `personal-team-blocked:spending-limit`。这是 personal-team 在 Responses API 的 API-credits 限额;**SuperGrok 网页订阅 ≠ Responses API 额度**,换模型/换档位无效,sub2api 把 402 当账号级冷却(`openai_gateway_grok.go:1362`)。
- **grok2api 为何不报 402**:用 SSO cookie 走 `console.x.ai/v1/responses`(Console 网页态),按**网页订阅配额**计费,不碰 API-credits。
- **零代码方案已排除**:「自定义上游 + 请求头覆写」注入不了 SSO —— `cookie`/`authorization` 都在禁止覆写列表(`account_header_override.go:50`、`credentialsBuilder.ts:66`);即便放开,token 失效/CF/请求体协议仍要单独兼容,维护成本高。
- **关键可行性结论(已调研)**:
  - console.x.ai `/v1/responses` 是 Responses API 格式,与现有 `forwardGrokResponses` 发给 cli-chat-proxy 的 body 同源 → 转换量小。
  - 现成 forwarder 模板 `antigravity_gateway_upstream.go:ForwardUpstream`(不走 token provider、自读 credentials、自拼 URL、自设认证头)。
  - `accounts.platform` 无 CHECK(`ent/schema/account.go:64`),可直接写 `grok_search`。
  - 调度必须挂 `account_groups`(`account_repo.go:1846` 的调度查询 JOIN account_groups)。
  - **别改 `DetectModelPlatform`**(污染所有 `grok-*` 路由),用独立 group+channel 绕开 composite。
  - multi-agent 靠 model 名表达;`reasoning.effort` 的 `xhigh` 是 multi-agent 独占(等价最大 agent 数)。
  - SSO 无 refresh/保活,失效只能重导;`sso` 与 `sso-rw` 值相同。
  - CF:Chrome TLS 指纹大概率必需(grok2api 经验,console 在 CF 后),PoC 先裸探实证。
- SSO token 来源:grok-register 注册产出(`grok-register/account_outputs.py:414` 的 SSO 值)。

## 需求

### REQ-1: 新平台常量与分发(零回归)
- `constants.go:20-27` 加 `PlatformGrokSearch = "grok_search"`。
- `openai_gateway_forward.go:101` 加 `else if account.Platform == PlatformGrokSearch { return s.forwardGrokSearch(...) }`。
- 不触碰现有 grok 路径。

### REQ-2: forwarder(新文件 `openai_gateway_grok_search.go`,照 `antigravity_gateway_upstream.go:21` 改)
- 自读 credentials:`sso_token` + `base_url`(默认 `https://console.x.ai`)。
- 自拼 URL:`base_url + "/v1/responses"`,不调 `buildGrokResponsesURL`/`xai.Build*URL`(绕白名单)。
- 请求体:沿用客户端 Responses body + console 契约(`store:false`、删 `metadata/service_tier/previous_response_id`、`include:["reasoning.encrypted_content"]`、tools 归一为 `web_search`/`x_search`、`reasoning.effort` 按 multi-agent 档映射)。
- 必带 headers:`Cookie: sso=<t>; sso-rw=<t>`、`Authorization: Bearer anonymous`、`Origin/Referer: https://console.x.ai`、`x-cluster: https://us-east-1.api.x.ai`、Chrome `User-Agent` + 匹配 `Sec-Ch-Ua*`。
- `s.httpUpstream.Do` 发(`applyGrokCLIProxyHeaders` 只对 cli-chat-proxy 生效,不污染)。
- 复用 `handleStreamingResponseWithReasoning`/`handleNonStreamingResponse`。
- 错误处理参照 `handleGrokAccountUpstreamError`(改平台字符串);403 借鉴 `RetryForbiddenAsEgress`(按 body 分类,非账号阻断的 egress 403 不惩罚账号,当 CF 挑战)。
- 不走 `GetAccessToken`/`grokTokenProvider`。

### REQ-3: 凭证与账号
- `credentials`(JSONB):`{"sso_token":"<SSO>","base_url":"https://console.x.ai"}`;访问器 `account.GetCredential`(`account.go:295`)。
- PoC 账号 `type` 复用 `apikey`。
- 录入端复用 `xai.NormalizeSSOToken`(`sso_device.go:344`)归一化。

### REQ-4: 模型路由(绕开 composite,不改 DetectModelPlatform)
- 建独立 group(`platform='grok_search'`)+ channel(`platform='grok_search'`,`model_mapping` 含 `grok-4.20-multi-agent-0309`)。
- `matchingPlatforms("grok_search")`→`["grok_search"]`,只在本平台选账号。

### REQ-5: CF 应对分级(PoC 先探)
- L0:`Do` + 一致 headers 裸探。
- L1:`DoWithTLS` + Chrome profile(`pkg/tlsfingerprint`)+ `Sec-Ch-Ua*`。
- L2:FlareSolverr 或手动 cf_clearance。

## 验收标准

- [ ] AC-1:`grok_search` 账号用 SSO 调 `grok-4.20-multi-agent-0309` 返回 200,搜索可用,**不再 402**。
- [ ] AC-2:现有 `grok` 平台行为零影响(回归验证)。
- [ ] AC-3(PoC gate):验证 console.x.ai+SSO 链路是否跑通,明确卡点(CF / 请求体 / SSO 注入)。
- [ ] AC-4(完整版):前端建账号 + admin SSO 导入接口。
- [ ] AC-5(完整版):SSO 失效(401)检测 → 标记需重认证。

## Out of Scope(PoC 阶段)

- 前端 UI、DB migration、admin 导入接口、SSO 自动保活/重登、channel monitor、quota。
- composite 接入(需要时再加:`matchingPlatforms`/`isConcreteRequestPlatform` + `composite_model_routes` CHECK)。
- **grok-register 的 `grok-sub2api-upload` 任务**:与本任务无关,且上传 OIDC 凭证解决不了 multi-agent 402(已查明)。
