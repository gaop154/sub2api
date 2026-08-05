# Implement: grok_search DPoP proof 支持

## Review Gate(实施前)
- prd/design/implement 已 review;`task.py start` 后再实施。
- 蓝本:grok2api `backend/internal/infra/provider/console/dpop.go`(commit `f1d5125`)。**纯逻辑移植,不 import grok2api**。

## 执行清单(有序)

### Step 0: 端到端契约验证 ✅ 已完成(2026-08-05)
- 脚本:`backend/grok_search_dpop_poc/main.go`(Go main,复用 `tlsfingerprint` Chrome profile,`DialTLSContext` 注入 transport)。
- **结果**(两种账号脱敏实测):
  - OIDC 登录账号:换 token ✅、绑定校验 ✅、业务 429(`Free usage quota exceeded`,额度问题,与 DPoP 无关)。
  - SSO-only 账号:换 token ✅、绑定校验 ✅、业务 **200**。
- **结论**:DPoP 链路正确;Chrome uTLS 下 `/dpop/token` 过 CF 成功;**SSO-only 也能换 token** → 录入约束放宽到仅需有效 `sso`(REQ-7/AC-8 落定)。design §1 已回写、§8/§9 风险 #1#2 已排除。
- **坑(已解)**:transport 必须用 `DialTLSContext`(非 `DialContext`),否则双重 TLS 报 `http: server gave HTTP response to HTTPS client`——仅 PoC 脚本注意,正式实现走 `s.httpUpstream.DoWithTLS` 不涉及。
- 真实 SSO 本地操作,不入对话/日志。Step 0 完成,可进 Step 1。

### Step 1: 新增 `openai_gateway_grok_search_dpop.go`(session + token 交换 + proof)
移植 grok2api `dpop.go`,接入 sub2api 模型,命名 `grokSearch*`:
- 类型:`grokSearchDPoPJWK`、`grokSearchDPoPSession{accessToken, privateKey *ecdsa.PrivateKey, publicJWK, expiresAt}`、`grokSearchDPoPSessionManager`(mu/sessions/lru/loads/now)、`grokSearchDPoPTokenError`(status/header/body,带 `response()` 伪造 `*http.Response`)。
- 常量:`grokSearchDPoPSessionLimit=4096`、`grokSearchDPoPRefreshSkew=20*time.Second`、`grokSearchDPoPMaxTokenLifetime=time.Hour`、`grokSearchDPoPTokenPath="/dpop/token"`。
- manager 方法:`get(ctx, s *OpenAIGatewayService, account *Account, ssoToken) (session, cacheKey, err)`、`cached`、`store`、`invalidate(cacheKey, accessToken)`、`removeLocked`、cache key 构造(`base_url|account.ID|sha256hex(sso)`)。
  - 并发去重:优先 `golang.org/x/sync/singleflight`(确认 go.sum 已有则用);否则 mutex + double-check。
- `fetchGrokSearchDPoPSession`:生成 EC P-256 → `publicJWK` → POST `{base_url}/v1/dpop/token`(浏览器头 + Content-Type + body `{"jwk":publicJWK}`,用 `s.httpUpstream.DoWithTLS`+Chrome profile)→ 解析 + 绑定校验(thumbprint vs `cnf.jkt`)→ 返回 session;失败构造 `grokSearchDPoPTokenError`。
- 辅助:`grokSearchDPoPJWK(pub)`(x/y `FillBytes(32)`+`base64.RawURLEncoding`)、`grokSearchDPoPJWKThumbprint`(RFC 7638 字典序 crv/kty/x/y + sha256 + base64url)、`parseGrokSearchDPoPAccessToken`(读 exp/cnf.jkt)、`grokSearchDPoPHTU(u)`(scheme+ToLower(host)+escapedPath)、`grokSearchDPoPProof(method, u, session)`(MapClaims jti/htm/htu/iat/ath + ES256 + Header typ/jwk + SignedString)。
- 复用:`golang-jwt/v5`、`crypto/ecdsa`/`elliptic`/`rand`/`sha256`、`encoding/base64`、`google/uuid`、`net/url`、`container/list`。

### Step 2: `OpenAIGatewayService` 持有 manager + 发送封装
- `OpenAIGatewayService` 新增字段 `grokSearchDPoP *grokSearchDPoPSessionManager`,在 service 构造处初始化(`new grokSearchDPoPSessionManager()`);若 service 有专门 init 点,就近加。
- 新增 `doGrokSearchDPoPRequest(ctx, account, ssoToken, proxyURL, method, upstreamURL, body, accept) (*http.Response, error)`:
  - `for attempt:=0; attempt<2; attempt++`:`get` session(token 错误→返回伪造 resp;其它 err→返回)→ `buildGrokSearchRequest(...)` 构 req → `req.Header.Set("Authorization","DPoP "+session.accessToken)` + `req.Header.Set("DPoP", grokSearchDPoPProof(...))` → `DoWithTLS` 发 → 非 401 或 attempt>0 则返回;否则读完 body + `invalidate(cacheKey, session.accessToken)` 进下一轮。
- 注意:`buildGrokSearchRequest` 当前硬编码 `Authorization: Bearer anonymous`(:222)——改为不设 Authorization(由 `doGrokSearchDPoPRequest` 注入 DPoP),或保留为默认值但调用方覆盖。**推荐**:移除该行,Authorization 完全由 DPoP 路径管理。

### Step 3: 接入两条调用路径
- `forwardGrokSearch`(:135 区域):把 `buildGrokSearchRequest` + `DoWithTLS` 两步替换为 `doGrokSearchDPoPRequest`;后续 `resp.StatusCode >= 400` 错误分流、流式/非流式处理逻辑**不变**。
- `forwardGrokSearchChatCompletionsViaResponses`(:684 区域):同样替换发送为 `doGrokSearchDPoPRequest`;后续处理不变。
- proxyURL 已在两处计算,直接传入。

### Step 4: 错误处理扩展(`handleGrokSearchAccountUpstreamError` :499)
- 403 分支 CF 之后、permission 之前插入:
  ```go
  if isGrokSearchDPoPRequired(responseBody) {
      logger.LegacyPrintf("service.openai_gateway_grok_search",
          "grok_search DPoP required account_id=%d (proof missing/invalid or upstream contract changed)", account.ID)
      return
  }
  ```
- 新增 `isGrokSearchDPoPRequired(body []byte) bool`:lower 含 `dpop-required` / `dpop proof required`。
- 401/403-permission/429/5xx/default:**基本不动**(换 token 失败的伪造 resp 自然汇入对应分支);新增 `/dpop/token` 未授权识别 `isGrokSearchUnauthorized`(lower 含 `unauthorized`/`not authorized`,且非 CF/非 dpop-required)→ `markGrokSearchReauthRequired`(SSO 质不足,重导合规 SSO)。

### Step 5: 测试(新增 `openai_gateway_grok_search_dpop_test.go`)
- **proof 构造**:解析 `DPoP` JWT,断言 JOSE `typ=dpop+jwt`/`alg=ES256`/`jwk{kty,crv,x,y}`;claims `htm`(大写)/`htu`(去 query、host 小写)/`iat`/`jti`/`ath==base64url(sha256(accessToken))`;两次 proof 的 jti 不同。
- **JWK thumbprint**:对已知公钥断言 == RFC 7638 期望值。
- **绑定校验**:`cnf.jkt != thumbprint` → `fetchGrokSearchDPoPSession` 报错。
- **session 缓存**:同 account+sso 二次 `get` 命中缓存(可用计数 mock httpUpstream 验证未重复打 token 端点);过期/`invalidate` 后重新获取。
- **401 重试**:`doGrokSearchDPoPRequest` 首次 401 → invalidate + 重换 + 二次成功;二次仍 401 → 返回 401 resp。
- **错误处理**:403 body=`{"code":"unauthorized:dpop-required",...}` → 不 blocked/不 reauth。
- **隔离回归**:grep 确认 grok/openai 的 `handle*AccountUpstreamError`/`buildGrok*Request` 未改。
- 注:token 端点/DoWithTLS 依赖需 mock(参考现有 `openai_gateway_grok_search_selection_test.go` 的 service+mock 模式)。

### Step 6: 验证
```bash
cd backend
go build ./...
go vet ./internal/service/...
go test ./internal/service -run 'GrokSearch|DPoP|Dpop' -count=1 -v
```
端到端(AC-2):sub2api 打 grok_search 账号 multi-agent 搜索 → 200。

## Rollback Point
- 改动:`openai_gateway_grok_search.go`(发送替换 + 错误分支 + Authorization 调整)+ 新增 `openai_gateway_grok_search_dpop.go` + 测试(+ 可选 PoC 脚本);无 DB 变更。
- 回滚:`git checkout -- backend/internal/service/openai_gateway_grok_search.go` + 删 dpop 文件/测试/PoC 脚本。

## 约束
- 禁止 commit(trellis-implement 阶段)。
- 简体中文注释,参照 `openai_gateway_grok_search.go` 风格。
- 复用 `golang-jwt/v5`+`google/uuid`,不引新依赖。
- 403 判定顺序:CF → DPoP 缺失 → SSO 权限失效(顺序错则误判)。
- 不 import grok2api(纯逻辑移植)。
- 真实 SSO 本地操作,不入对话/日志。
