# Design: grok_search DPoP proof 支持

> 架构对齐 grok2api `console/dpop.go`(commit `f1d5125`,v3.1.0 实测),适配 sub2api 的
> egress(`httpUpstream.DoWithTLS` + Chrome uTLS profile)与 account 模型。**纯逻辑移植,不 import grok2api。**

## 1. x.ai DPoP 真实契约(token 交换 + 绑定,非自包含)

```
[1. 换 token — 按 session 缓存,非每请求]
  生成 EC P-256 密钥对(crypto/ecdsa.GenerateKey + crypto/rand)
  POST {base_url}/v1/dpop/token
    Cookie: sso=<t>; sso-rw=<t>            (SSO 仍是换 token 的身份)
    Content-Type: application/json
    body: {"jwk": {"kty":"EC","crv":"P-256","x":"<b64url>","y":"<b64url>"}}
    (+ 浏览器头: Origin/Referer console.x.ai、UA、Sec-Ch-Ua*、Cache-Control:no-cache、Pragma:no-cache)
  → 200 {"access_token":"<JWT>","token_type":"DPoP","expires_in":<秒>}
  绑定校验: dpopJWKThumbprint(本地公钥) == 解码 access_token payload.cnf.jkt  否则拒绝

[2. 业务请求 /v1/responses — 每请求新 proof,session 复用]
  Authorization: DPoP <access_token>        ← 覆盖原 "Bearer anonymous"
  DPoP: <proof JWT>
    JOSE header: {"typ":"dpop+jwt","alg":"ES256","jwk":<公钥>}
    claims: {"htm":"POST","htu":"https://console.x.ai/v1/responses","iat":<unix>,"jti":"<uuid>","ath":"<b64url(sha256(access_token))>"}
  Cookie: sso=<t>; sso-rw=<t>               ← 保留
  (+ 浏览器头; path 以 /responses 结尾时带 x-cluster: https://us-east-1.api.x.ai)

[3. 401 重试]
  业务请求 401 → invalidate(cacheKey, accessToken) → 重换 token → 重试一次(仅一次)
```

**要点**:`ath` 必带;`Authorization` 用 `DPoP` scheme;密钥随 session 走(fetch 时生成),无需 per-account 持久化。

**[/dpop/token 账号登录态 — PoC 已确(2026-08-05)]** PoC 实测两种账号(OIDC 登录完整 `xai-j0s8fyj8st`、SSO-only `xai-hmxner9fyc`)的 `sso` 均**成功换到 DPoP token**(绑定校验通过),SSO-only 业务请求 **200**,OIDC 账号 429(额度耗尽,与 DPoP 无关)。结论:`/v1/dpop/token` **不要求 OIDC 登录态**,SSO 本身即 sufficient。**录入约束放宽到仅需有效 `sso`(更优解达成)**。grok-register 不必每账号走 OIDC 登录,`access_token` 字段非必需(且其为 OIDC Bearer、无 `cnf.jkt`,与 DPoP token 是两回事)。

## 2. 模块划分(新增 `openai_gateway_grok_search_dpop.go`,同 package service)

移植 grok2api `dpop.go`,命名统一 `grokSearch*` 前缀,接入 sub2api 的 `httpUpstream` + `*Account`:

| 移植项 | grok2api | sub2api 对应 |
|---|---|---|
| 密钥/发请求 | `lease.DoDeferredForbidden` / egress lease | `s.httpUpstream.DoWithTLS(req, proxyURL, accountID, concurrency, grokSearchChromeProfile())` |
| session manager | `dpopSessionManager`(LRU+singleflight+skew) | `grokSearchDPoPSessionManager`(同结构) |
| cache key | `baseURL\|cred.ID\|nodeID\|HashToken(sso)` | `base_url\|account.ID\|sha256(sso)`(无 node 概念,去 nodeID) |
| CF clearance 失效 | `lease.InvalidateClearance()` | 省略(sub2api 用 Chrome uTLS 过 CF,无 clearance 持久态);token endpoint 403 走 CF/SSO 分类 |
| token endpoint 错误 | `dpopTokenEndpointError.response()` 伪造 resp 给上层 | 同(伪造 resp,统一汇入 `handleGrokSearchAccountUpstreamError`) |
| 持有者 | `Adapter` 持 `dpop *dpopSessionManager` | `OpenAIGatewayService` 持 `grokSearchDPoP *grokSearchDPoPSessionManager`(或包级单例) |

## 3. session manager(`grokSearchDPoPSessionManager`)
- 字段:`mu sync.Mutex`、`sessions map[string]*entry`、`lru list.List`、`loads singleflight.Group`(若 go.sum 有 `golang.org/x/sync`;否则改 mutex + double-check)、`now func() time.Time`。
- 常量:`grokSearchDPoPSessionLimit=4096`、`grokSearchDPoPRefreshSkew=20s`、`grokSearchDPoPMaxTokenLifetime=time.Hour`。
- `get(ctx, s, account, ssoToken) (session, cacheKey, err)`:cached 命中(未过期)→ 返回;否则 singleflight 内 `s.fetchGrokSearchDPoPSession` → store → 返回。
- `cached`:过期(`expiresAt <= now+skew`)则淘汰;命中则 LRU 前移。
- `store`:LRU 满淘汰最旧;更新已存在 entry。
- `invalidate(cacheKey, accessToken)`:仅当 entry.accessToken==accessToken 才删(防误删并发刷新的新 session);**不** `singleflight.Forget`(理由同 grok2api 注释:避免过期 token 并发 401 触发多次换 token)。
- cache key:`strings.TrimRight(base_url,"/") + "|" + account.ID + "|" + sha256hex(sso)`。

## 4. token 交换(`fetchGrokSearchDPoPSession`)与绑定校验
- 生成 EC P-256 私钥;`publicJWK = grokSearchDPoPJWK(&pub)`(x/y 用 `FillBytes(make([]byte,32))` 定长 + `base64.RawURLEncoding`)。
- POST `{base_url}/v1/dpop/token`,header 用 `applyGrokSearchBrowserHeaders`(由 `buildGrokSearchRequest` 现有浏览器头抽出的复用集,**不带 Authorization/x-cluster**)、`Content-Type: application/json`、body `{"jwk":publicJWK}`。
- 用 `s.httpUpstream.DoWithTLS`(Chrome profile)发送。
- 响应:status∈[200,300) 才继续;否则构造 `grokSearchDPoPTokenError`(带 status/header/body)返回,由 `doGrokSearchDPoPRequest` 伪造 resp 汇入错误分流。
- 解析 `{access_token, token_type, expires_in}`;校验 `token_type=="DPoP"`、`0<expires_in<=MaxTokenLifetime`。
- `thumbprint = grokSearchDPoPJWKThumbprint(publicJWK)`(RFC 7638: 结构体字段字典序 crv/kty/x/y marshal + sha256 + base64url)。
- `tokenExpiry, tokenJKT = parseGrokSearchDPoPAccessToken(access_token)`(split 3 段、`base64.RawURLEncoding` 解 payload、读 `exp`/`cnf.jkt`)。
- **绑定校验**:`tokenJKT != thumbprint` → 报错拒绝。
- `expiresAt = min(now+expires_in, tokenExpiry)`;`expiresAt <= now+skew` → 报错(已过期)。
- 返回 `session{accessToken, privateKey, publicJWK, expiresAt}`。

## 5. 业务请求注入(`doGrokSearchDPoPRequest`)与 401 重试
- 替换现有 `buildGrokSearchRequest` + `DoWithTLS` 两步发送(forwardGrokSearch `:135`、chat bridge `:684`)。
- 流程(`for attempt := 0; attempt < 2; attempt++`):
  1. `session, cacheKey, err = s.grokSearchDPoP.get(...)`;若 err 是 `*grokSearchDPoPTokenError` → 返回其伪造 resp(进错误分流);其它 err → 返回 transport error。
  2. 用现有 `buildGrokSearchRequest` 构 request(设 Cookie/浏览器头/x-cluster),再:
     - `req.Header.Set("Authorization", "DPoP "+session.accessToken)`(覆盖 anonymous)
     - `req.Header.Set("DPoP", grokSearchDPoPProof(req.Method, req.URL, session))`
  3. `s.httpUpstream.DoWithTLS` 发送。
  4. `if resp.StatusCode != 401 || attempt > 0 → return resp`;否则读完 body、`invalidate(cacheKey, session.accessToken)`、进入第 2 次循环。
- `buildGrokSearchRequest` 调整:新增 `dpopAuthorization string` + `dpopProof string` 参数,内部覆盖 Authorization、设 DPoP(非空才设);移除硬编码 `Bearer anonymous`(改由调用方决定)。

## 6. proof 构造(`grokSearchDPoPProof`)
- claims(MapClaims):`jti=uuid.NewString()`、`htm=strings.ToUpper(method)`、`htu=grokSearchDPoPHTU(url)`、`iat=now.Unix()`、`ath=base64.RawURLEncoding(sha256(access_token))`。
- `proof := jwt.NewWithClaims(jwt.SigningMethodES256, claims)`;`proof.Header["typ"]="dpop+jwt"`;`proof.Header["jwk"]=session.publicJWK`;`SignedString(session.privateKey)`。
- `grokSearchDPoPHTU(u *url.URL)`:`scheme + "://" + strings.ToLower(host) + escapedPath`(去 query/fragment、host 显式小写——比 grok2api 更严格,符合 RFC 9449 §4.3)。

## 7. 错误处理决策树(扩展 `handleGrokSearchAccountUpstreamError` `:490`)

```
401(重试后仍 401,doGrokSearchDPoPRequest 已重试一次) → markGrokSearchReauthRequired       [现状]
403 → CF?                          → 不惩罚(return)               [现状]
     → isGrokSearchDPoPRequired?   → 告警日志 + 不惩罚(return)     [新增]
     → isGrokSearchPermissionDenied? → markReauth(return)          [现状]
     → 其它                          → 落 default                   [现状]
换 token 的 403/401(经伪造 resp 汇入) → CF→不惩罚;permission/401→markReauth;未授权(SSO 有效但账号未 OIDC 登录/无 API 权限,非 CF/非 dpop-required)→ markReauth(重导合规 SSO,不当临时错误重试)
429/5xx/default → 现状不动
```
- `isGrokSearchDPoPRequired(body)`:lower 含 `dpop-required` / `dpop proof required`。
- `isGrokSearchUnauthorized(body)`:lower 含 `unauthorized`/`not authorized`(用于换 token 端点"SSO 有效但权限/登录态不足";判定顺序在 dpop-required 之后)。
- **归类理由**:DPoP 缺失/无效是协议层问题(实现 bug 或上游再变),SSO 仍有效 → 绝不当账号问题冷却/失效(否则正常账号被误下线 24h 不可用)。实现正确后正常路径不应出现;出现即异常信号。

## 8. 适配差异与权衡(相对 grok2api)
- **egress**:grok2api 用 lease(egress 池 + cf clearance);sub2api 用 `DoWithTLS`+Chrome profile(无 clearance)。→ token endpoint 403 时省略 `InvalidateClearance`,改靠现有 CF/SSO 分类;若实测发现 CF 反复挑战,再评估 cf_clearance(非本期)。**PoC 已验(2026-08-05):Chrome uTLS 下 `/dpop/token` 实测过 CF 成功,本期无需 cf_clearance。**
- **持有者**:grok2api Adapter 持 manager;sub2api `OpenAIGatewayService` 已是单例(注册于 service),新增字段 `grokSearchDPoP` 即可,生命周期跟随 service。
- **proof htu host**:显式 `ToLower`(比 grok2api 严格),更贴 RFC。
- **不验 access_token 签名**:同 grok2api(客户端无上游公钥;只读 exp/cnf.jkt)。

## 9. 隔离 / 回滚 / 风险
- **隔离**:改动 `openai_gateway_grok_search.go` + 新增 `openai_gateway_grok_search_dpop.go`(+ 测试);命名 `grokSearch*`;不进通用 httpUpstream;不碰 grok 的 `buildGrokResponsesRequest`。
- **回滚**:revert 两文件 + 删测试 + 删 Step 0 PoC 脚本;无 DB 变更。
- **风险排序**:
  1. ~~**账号登录态(原最高,影响录入要求)**~~ → **PoC 已排除(2026-08-05)**:SSO-only 与 OIDC 登录账号均成功换 token,SSO-only 业务 200。录入约束放宽到仅需有效 `sso`。
  2. ~~**egress 差异(中)**~~ → **PoC 已排除**:Chrome uTLS profile 下 `/dpop/token` 实测过 CF 成功(两种账号均到达端点并返回 JSON,非 CF 挑战页)。
  3. **token 端点契约漂移(低-中)**:grok2api v3.1.0 刚实测,窗口期内稳定;`dpop-required` 告警分支兜底上游再变。
  4. **时钟漂移(低)**:`expires_in`/`exp` 由上游给,客户端按其缓存;`iat` 用本地时钟,RFC 窗口通常宽。
  5. **并发换 token(低)**:singleflight/双重检查兜底。
