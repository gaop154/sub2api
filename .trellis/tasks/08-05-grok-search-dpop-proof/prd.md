# PRD: grok_search DPoP proof 支持(修复 unauthorized:dpop-required 403)

## 目标

为 grok_search 平台的 console.x.ai 请求实现 **DPoP token 交换 + proof 注入**(RFC 9449),
消除上游新增的 `unauthorized:dpop-required` 403,让 grok_search 账号恢复可用;并补齐 DPoP
相关错误形态在错误处理中的归类。**保持 grok_search 物理隔离,不碰其它平台。**

## 背景与已确认事实

- 07-28 引入 grok_search(SSO cookie 走 console.x.ai/v1/responses),07-29 补了 403/429 状态码识别。
- 当前报错(2026-08-05):**所有** grok_search 账号请求 console.x.ai 返回
  `403 {"code":"unauthorized:dpop-required","error":"DPoP proof required but was not verified."}`。
- **根因**:console.x.ai 上游新增强制 DPoP(RFC 9449)校验;sub2api 当前 `buildGrokSearchRequest`
  (`openai_gateway_grok_search.go:209-239`)只发 SSO cookie + `Bearer anonymous`,**无 DPoP** → 被 403 拒绝。
- 现错误处理(`handleGrokSearchAccountUpstreamError` `:490-529`):该 403 不含 CF 标记、不含 `permission-denied`
  → 落"其它 403:不处理"(`:510`)→ 账号仍在池 → 每次请求都 403 → "所有账号全报"。这是 07-29 决策树**未预见的第四种 403**。

### 上游真实契约(关键,已由 grok2api v3.1.0 实测确认)

参考实现 grok2api 在分支 `grok_console_260805` 提交 `f1d5125`("feat: align DPoP protocol...")**已正确实现 DPoP**,
专文件 `console/dpop.go`(390 行),为本次移植的蓝本。x.ai 的 DPoP 是**「绑定式 token 交换」模型,非自包含**:

1. **换 token**:`POST {base_url}/v1/dpop/token`,SSO cookie + body `{"jwk":<公钥>}` → 返回 `{access_token, token_type:"DPoP", expires_in}`。
2. **绑定校验**:本地公钥 JWK thumbprint(RFC 7638) 必须等于 `access_token` JWT 的 `cnf.jkt` claim。
3. **业务请求**:`Authorization: DPoP <token>`(**DPoP scheme,非 Bearer**) + `DPoP: <proof>`,proof 含 `htm/htu/iat/jti/ath`。
4. `ath = base64url(sha256(access_token))`;密钥随 DPoP session 生命周期(fetch 时生成 EC P-256,session 期复用),**无需持久化**。
5. 业务请求 401 → 失效 session + 重换 token + 重试一次。

### sub2api 侧基础

- `go.mod` 已有 `github.com/golang-jwt/jwt/v5 v5.3.1`、`github.com/google/uuid v1.6.0`;已有 `crypto/ecdsa`/`crypto/sha256` 用法 → **无需新引依赖**。
- grok_search 已有过 CF 的 `DoWithTLS` + Chrome uTLS profile(`grokSearchChromeProfile`)→ token 交换与业务请求复用同一出口。
- CF 判定已有 `httputil.IsCloudflareChallengeResponse`;SSO 失效判定已有 `isGrokSearchPermissionDenied`/401。

### 账号登录态要求(PoC 已验证,2026-08-05)

grok-register 产出两种形态账号(实测 `cpa_auths/uploaded_search/`):
- **OIDC 登录完整**(`xai-j0s8fyj8st`):`sso` + 完整 OAuth 凭证(`access_token`/`refresh_token`/`id_token` 等)。
- **SSO-only**(`xai-hmxner9fyc`):仅 `sso`,未做 OIDC 登录。

**PoC 结论(决定性)**:两种账号的 `sso` 均**成功换到 DPoP token**(`cnf.jkt==thumbprint` 绑定校验通过);SSO-only 账号业务请求返回 **200**,OIDC 登录账号返回 429(免费额度耗尽,与 DPoP 无关)。即 `/v1/dpop/token` **不要求 OIDC 登录态**,SSO 本身即 sufficient。

**录入约束(据此放宽)**:grok_search 账号录入**仅需有效 `sso`**,不要求 OIDC 登录/`access_token` 字段。grok-register 不必每账号走 OIDC 登录,产出成本更低(用户偏好的更优解)。

## 需求

### REQ-1: DPoP session 获取(token 交换 + 绑定校验)
- `POST {base_url}/v1/dpop/token`:SSO cookie + body `{"jwk":{kty:EC,crv:P-256,x,y}}` + 浏览器头。
- 解析 `{access_token, token_type:"DPoP", expires_in}`;校验 `token_type==DPoP`、`expires_in` 合理。
- **绑定校验**:`dpopJWKThumbprint(本地公钥)`(RFC 7638: 字典序 crv/kty/x/y + sha256 + base64url) == 解析 access_token payload 的 `cnf.jkt`;不一致即拒绝。
- 用 `DoWithTLS` + Chrome profile 发送(过 CF)。

### REQ-2: session 缓存管理(grok_search 专属)
- LRU 缓存 `session{accessToken, privateKey(*ecdsa.PrivateKey), publicJWK, expiresAt}`,key = `base_url | account.ID | sha256(sso)`。
- 提前刷新(到期前 N 秒视为过期)、上限淘汰、并发去重(优先 `singleflight`,无则 mutex double-check)。
- `invalidate(account, accessToken)`:业务 401 时失效对应 session(校验 accessToken 匹配,避免误删并发刷新的新 session)。

### REQ-3: 业务请求 DPoP 注入(两条调用路径)
- `forwardGrokSearch`(`:94`)与 chat bridge `forwardGrokSearchChatCompletionsViaResponses`(`:619`)的发送改为 `doGrokSearchDPoPRequest`(封装:取 session → 构 request → 注入 → 发 → 401 重试一次)。
- 注入头:`Authorization: DPoP <access_token>`(**覆盖**原 `Bearer anonymous`)、`DPoP: <proof>`;保留 SSO cookie。
- proof JWT:JOSE `{typ:"dpop+jwt",alg:"ES256",jwk:<公钥>}`,claims `{htm(大写), htu(规范化: scheme://host/path, 去 query/fragment, host 小写), iat, jti(uuid), ath}`。
- 用 `golang-jwt/v5`:`jwt.NewWithClaims(ES256, claims)` + 设 `Header["typ"]/["jwk"]` + `SignedString(privateKey)`。

### REQ-4: 错误处理归类(扩展 `handleGrokSearchAccountUpstreamError` `:490`)
- **业务 403 `dpop-required`**:新增 `isGrokSearchDPoPRequired` → **不冷却/不失效/不 reauth** + 告警日志(协议层异常,SSO 仍有效)。
- **业务/换 token 401**(重试后仍 401):`markGrokSearchReauthRequired`(现有)。
- **换 token 403**:CF → 不惩罚(现有 CF 分支);SSO 权限失效 → markReauth(现有 permission 分支)。
- 403 判定顺序:CF → DPoP 缺失 → SSO 权限失效 → 其它。
- 换 token 失败的错误统一汇入 `handleGrokSearchAccountUpstreamError`(用 token endpoint 的 status 构造可分流结果),避免另写一套。

### REQ-5: 隔离与零回归
- 所有改动封闭在 grok_search 文件(+ 新增 `openai_gateway_grok_search_dpop.go`,同 package service)。
- 不碰 grok/openai/gemini 等任何其它平台;不 import grok2api(纯逻辑移植)。

### REQ-6: 端到端验证(契约已实测,PoC 降级为验证)
- 用真实 SSO 跑通"换 token → 业务 200";确认 sub2api egress(Chrome uTLS)下 token 交换可用。

### REQ-7: 账号登录态与录入约束(PoC 已验证,2026-08-05)
- PoC 实测:OIDC 登录与 SSO-only 两种账号的 SSO 均**成功换到 DPoP token**,SSO-only 业务请求 200。
- **录入约束(放宽)**:grok_search 账号录入**仅需有效 `sso`**,不要求 OIDC 登录或 `access_token` 字段。
- **DPoP token 不在 grok_register 预生成**:access_token 是≤1h 短期凭证、且绑定客户端 EC 公钥(预生成意味着私钥也要随账号存储),应由 sub2api **运行时**用 SSO 换取并按账号缓存(过期/401 自动刷新)。grok_register 只产 `sso`。
- `/dpop/token` 返回"未授权"(SSO 有效但登录态/权限不足,PoC 未触发,作防御性兜底) → `markGrokSearchReauthRequired`(同 SSO 失效,重导合规 SSO),不当临时错误重试。

## 验收标准

- [x] AC-1:用真实 SSO 调 `console.x.ai/v1/dpop/token` 成功换 access_token,绑定校验(thumbprint==cnf.jkt)通过。**PoC 已验(2026-08-05,两种账号均通过)**。
- [ ] AC-2:grok_search 账号端到端 multi-agent 搜索返回 200,无 `unauthorized:dpop-required`。**PoC 已验链路(SSO-only 200);集成 sub2api 后复测**。
- [ ] AC-3:业务 401 → 自动失效 session + 重换 token + 重试一次后成功(透明);重试后仍 401 → markReauth。
- [ ] AC-4:`dpop-required` 403 命中新分支 → 不冷却/不失效/不 reauth(账号仍 active),有告警日志。
- [ ] AC-5:session 缓存命中时不重复换 token;过期/401 后正确刷新。
- [ ] AC-6:隔离零回归(grok/openai 的请求构造与错误处理零改动,相关单测不受影响)。
- [ ] AC-7:`go build ./...` + `go vet` + grok_search/DPoP 单测通过。

- [x] AC-8:PoC 记录 SSO-only(未 OIDC 登录)能否换 DPoP token;据结论定 grok_search 录入约束。**已验:SSO-only 可换 token 且业务 200 → 录入约束放宽到仅需有效 `sso`(不要求 OIDC 登录)**。

## 约束

- 遵循 grok_search 物理隔离(见 `.trellis/spec/backend/new-platform.md`)。
- 复用 `golang-jwt/v5` + `google/uuid`,不引新依赖。
- 简体中文注释,参照 `openai_gateway_grok_search.go` 现有风格。
- 真实 SSO 本地操作,**不入对话/日志**。

- grok_search 账号录入:**仅需有效 `sso`**(PoC 已证 SSO-only 可换 DPoP token 且业务 200,不要求 OIDC 登录/`access_token` 字段)。

## Out of Scope

- DPoP access_token 的服务端验签(客户端职责只到读取 exp/cnf.jkt,验签是上游职责)。
- cf_clearance 持久化(07-28 起用 Chrome uTLS 过 CF,无 clearance 概念;token endpoint 403 按现有 CF/SSO 分类处理)。
- 前端 session/密钥管理 UI。
- 其它平台 DPoP 接入。
