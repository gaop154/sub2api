# Design: grok_search 平台

## 1. 架构边界(隔离性)
- `grok_search` 与现有 `grok` 物理隔离:独立 forwarder 文件、独立分发分支、独立 group/channel。
- 现有 grok 链路(`forwardGrokResponses`/`grokTokenProvider`/`buildGrokResponsesURL`/quota/cache)完全不触达。
- 唯一共享:`httpUpstream` 出口层 —— 但 `applyGrokCLIProxyHeaders`(`http_upstream.go:440`)只对 `cli-chat-proxy.grok.com` 生效,对 `console.x.ai` 直接 return,无副作用。
- 回归风险:极低(分发按 `account.Platform` 字面量精确匹配)。

## 2. 数据流
```
客户端 → channel(grok_search, model_mapping 含 multi-agent)
  → matchingPlatforms("grok_search") → ["grok_search"]
  → 调度选 grok_search 账号(必须挂 account_groups)
  → Forward → openai_gateway_forward.go:101 分发
  → forwardGrokSearch
      读 sso_token/base_url → 拼 console.x.ai/v1/responses
      body 施加 console 契约 → 设 Cookie/anonymous/Origin/x-cluster/UA/Sec-Ch-Ua
  → httpUpstream.Do → SSE/JSON
  → handleStreamingResponseWithReasoning / handleNonStreamingResponse
  → 客户端
```

## 3. 请求体契约(console,参照 grok2api `console/normalize.go`)
- `model`:multi-agent 模型名(请求体无 agent 数量字段,靠 model 名进入 multi-agent 模式)。
- `store:false`(console 无状态)。
- 删:`metadata`/`previous_response_id`/`service_tier`/`prompt_cache_key`。
- `include:["reasoning.encrypted_content"]`。
- `tools`:`{type:"web_search"}`/`{type:"x_search"}`(搜索能力)。
- `reasoning.effort`:`low`/`medium`/`high`/`xhigh`(`xhigh` 独占 multi-agent,等价最大 agent 数)。
- `max_output_tokens`:可至 2,000,000。
- body 与现有 `forwardGrokResponses` 发给 cli-chat-proxy 的 Responses body 同源,转换量小。

## 4. headers 契约(参照 grok2api `console/headers.go`)
| header | 值 |
|---|---|
| Cookie | `sso=<t>; sso-rw=<t>`(两值相同) |
| Authorization | `Bearer anonymous`(占位) |
| Origin / Referer | `https://console.x.ai` |
| x-cluster | `https://us-east-1.api.x.ai` |
| User-Agent | Chrome |
| Sec-Ch-Ua* | 与 UA 大版本匹配(**一致性是过 CF 关键**,不带会指纹矛盾) |
| Accept | `application/json, text/event-stream` |

## 5. SSE 解析(复用现有)
- 正文:`response.output_text.delta` 累加。
- usage:`response.completed` 的 `response.usage`。
- 结束:`[DONE]`。
- 与 Responses SSE 同构 → 复用 `handleStreamingResponseWithReasoning`(`openai_gateway_response_handling.go:45`)。

## 6. 凭证模型
- `credentials` JSONB:`{"sso_token":"<SSO>","base_url":"https://console.x.ai"}`。
- `account.GetCredential("sso_token")`(`account.go:295`)。
- `type` 复用 `apikey`(PoC;语义化 `sso` 需改代码,后续再做)。

## 7. 模型路由(绕 composite)
- 独立 group + channel,均 `platform='grok_search'`。
- `matchingPlatforms("grok_search")`(`channel_service.go:358`)自动返回 `["grok_search"]`。
- **不改 `DetectModelPlatform`**(`composite_platform.go:90`,改它会污染所有 `grok-*`)。
- channel `model_mapping`:`grok-4.20-multi-agent-0309`(同名或映射到 console 真实 ID)。

## 8. CF 策略(分级)
- L0:`httpUpstream.Do`(裸 Go TLS)+ 一致 headers,实证 console.x.ai 挡否。
- L1(预期需要):`DoWithTLS` + Chrome profile(`pkg/tlsfingerprint/dialer.go` 支持覆盖,自抓 cipher/curves/ALPN `h2`)+ `Sec-Ch-Ua*`。
- L2(反复挑战时):FlareSolverr(参照 grok2api `egress/flaresolverr.go`)或手动导 cf_clearance。
- 403 处理:借鉴 `RetryForbiddenAsEgress` —— 按 body 分类,非账号阻断的 egress 403 不惩罚账号、重建会话重试。

## 9. SSO 生命周期
- `sso`=`sso-rw` 值相同;无 refresh_token、无周期保活。
- 失效(401):标记账号需重认证,管理员重导(grok2api 也是 `MarkReauthRequired`,无自动重登)。

## 10. 兼容性 / 回滚
- 新平台独立,回滚 = 删 forwarder 文件 + 分发分支 + SQL 行,零影响现有 grok。
- PoC 用 SQL 插账号 + 独立 group/channel,不写 migration。

## 11. 风险排序
1. CF(最高):Chrome 指纹是否必需、能否稳定过。
2. 请求体(中):console 与 cli-chat-proxy 的 Responses schema 细差。
3. SSO 持久(中):失效需手动重导。
