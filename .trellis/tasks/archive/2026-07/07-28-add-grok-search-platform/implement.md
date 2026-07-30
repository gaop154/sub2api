# Implement: grok_search 平台(PoC 优先)

> 目标:先用最小改动验证 console.x.ai+SSO 链路是否跑通(CF/请求体是关键未知),通了再补完整版。

## 阶段 0:PoC(最小验证,~200-300 行)

### Step 1 — 平台常量
- `backend/internal/domain/constants.go:20-27` 加 `PlatformGrokSearch = "grok_search"`。
- 验证:`go build ./...` 通过。

### Step 2 — forwarder
- 新建 `backend/internal/service/openai_gateway_grok_search.go`,照 `antigravity_gateway_upstream.go:ForwardUpstream`(`:21`)结构。
- 实现 `forwardGrokSearch(ctx, c, account, body, originalModel, reqStream, startTime)`:
  - `ssoToken := account.GetCredential("sso_token")`;`baseURL := account.GetCredential("base_url")`,空则 `https://console.x.ai`。
  - 拼 `upstreamURL := strings.TrimSuffix(baseURL, "/") + "/v1/responses"`。
  - body 施加 console 契约(design §3)。
  - 设 headers(design §4)。
  - `proxyURL` 取 `account.Proxy.URL()`(同 `grok.go:96-99`)。
  - `resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)`。
  - stream → `handleStreamingResponseWithReasoning`;non-stream → `handleNonStreamingResponse`。
  - 错误分流(参照 `handleGrokAccountUpstreamError`,改平台字符串)。

### Step 3 — 分发
- `openai_gateway_forward.go:101` 后加:
  ```go
  if account.Platform == PlatformGrokSearch {
      return s.forwardGrokSearch(ctx, c, account, body, originalModel, reqStream, startTime)
  }
  ```
- 验证:`go build ./...`。

### Step 4 — SQL 建账号 + group + channel
- `accounts`:`platform='grok_search'`, `type='apikey'`, `credentials='{"sso_token":"<SSO>","base_url":"https://console.x.ai"}'::jsonb`。
- `account_groups`:挂到 `platform='grok_search'` 的 group。
- `groups` + `channels`:均 `platform='grok_search'`,channel `model_mapping` 含 `grok-4.20-multi-agent-0309`。

### Step 5 — PoC 验证(见下「验证」)+ Review Gate

### Review Gate(PoC)
- 200 → 进阶段 1。
- 403+html(CF)→ 先做 L1(`DoWithTLS`+Chrome profile+Sec-Ch-Ua),再评估。
- 400-422(请求体)→ 补 console 契约转换。
- 401 → 检查 SSO 注入/有效性。

## 阶段 1:完整版(PoC 通过后)
- migration(`user_platform_quotas`/`channel_monitors` CHECK,仿 `157`/`176`)。
- admin 导入接口(SSO → `grok_search` 账号,不走 `ConvertSSOToBuild`)。
- 前端(platform 选项 `frontend/src/api/admin/settings.ts:33`、SSO 输入、图标 `PlatformIcon.vue`)。
- composite 接入(需要时:`matchingPlatforms`/`isConcreteRequestPlatform` + `composite_model_routes` CHECK)。
- SSO 失效检测 + CF 自动应对。
- channel monitor / quota。

## 验证

### PoC 验证
1. SQL 插账号 + account_groups。
2. `curl {sub2api}/v1/chat/completions`(用 grok_search channel 的 API key)
   `-d '{"model":"grok-4.20-multi-agent-0309","messages":[{"role":"user","content":"搜一下今天的新闻"}]}'`
3. 判返回:200(链路通)/ 403+html(CF)/ 400-422(请求体)/ 401(SSO)。

### 完整版验证
- 前端建账号流程、`trellis-check`、单元测试、multi-agent 搜索端到端。

## 回滚点
- PoC:删 forwarder 文件 + `forward.go` 分发 + SQL 行。
- 完整:`git revert`。

## 关键参照文件
- 模板:`sub2api/backend/internal/service/antigravity_gateway_upstream.go`
- 错误处理:`openai_gateway_grok.go:1350`、`grok_upstream_errors.go`
- 出口:`http_upstream.go:185 Do` / `:231 DoWithTLS`
- 响应:`openai_gateway_response_handling.go:45,1108`
- 账号:`ent/schema/account.go`、`account.go:295 GetCredential`、`account_repo.go:1846`(挂 group)
- 路由:`channel_service.go:358 matchingPlatforms`
- grok2api 参照(不改):`console/{adapter,headers,normalize,catalog}.go`、`egress/{tlsclient,flaresolverr,manager}.go`
