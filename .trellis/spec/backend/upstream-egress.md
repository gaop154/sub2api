# Upstream Egress & TLS Fingerprint（出口层与 TLS 指纹）

> 上游出口层（httpUpstream）的使用约定，以及绕过 Cloudflare 等 TLS 指纹检测的方案。

---

## 概述

sub2api 通过 `internal/repository/http_upstream.go` 的 `httpUpstreamService` 统一发出上游请求：
- `Do(req, proxyURL, accountID, concurrency)` —— 裸 Go TLS
- `DoWithTLS(req, proxyURL, accountID, concurrency, profile *tlsfingerprint.Profile)` —— 自定义 uTLS 指纹

共享出口层不会污染平台：
- `applyGrokCLIProxyHeaders`（http_upstream.go:440）按 hostname 守卫，仅对 `cli-chat-proxy.grok.com` 生效，其他 host 直接 return。
- `validateRequestHost`（:570）是 **SSRF 防护**（`ValidateResolvedIP`），不是 host 白名单——公网域名可正常通过。

---

## 场景：上游在 Cloudflare 后（如 console.x.ai）

### 现象
裸 Go TLS（curl / OpenSSL 指纹）→ CF 返回 `403 "you have been blocked"`（HTML，`server: cloudflare`）。这是**认证前拦截**，与凭证无关——凭证再有效也过不去。

### 判别 CF 拦截类型
看响应 body 关键词：
- `you have been blocked` / `cf-error` / `Attention Required` → firewall/bot block（换 Chrome TLS 指纹通常可过）
- `Just a moment` / challenge script → managed challenge（需 FlareSolverr / cf_clearance，TLS 指纹不够）

### 方案：Chrome uTLS 指纹（L1）
用 `DoWithTLS` + 自建 Chrome `*tlsfingerprint.Profile`。实证（grok_search / console.x.ai）：裸 TLS→403，Chrome 指纹→CF 放行（应用层 401/200）。代理（住宅/海外）作为 IP 因素的双保险，但直连 + Chrome 指纹已实证可过。

---

## tlsfingerprint 包契约

`internal/pkg/tlsfingerprint/dialer.go`：
- **内置默认是 Node.js 24.x / Claude Code 指纹**（JA3 `44f88fca027f27bab4bb08d4af15f23e`），**不是 Chrome**。给 Anthropic/Claude Code 类上游用默认即可；模拟 Chrome 必须自建 Profile。
- `Profile` 字段空时 fallback 到 Node.js 默认。
- `buildClientHelloSpecFromProfile`（:336）从 Profile 构造 `utls.ClientHelloSpec`，支持自定义 cipher/curves/sigalgs/扩展顺序/GREASE/ECH。
- 出口：`NewDialer` / `NewHTTPProxyDialer` / `NewSOCKS5ProxyDialer` 的 `DialTLSContext` 可直接作 `http.Transport.DialTLSContext`。

### Gotcha：未知扩展触发 "error decoding message"
`buildClientHelloSpecFromProfile` 的 switch 只识别常见扩展（0/5/10/11/13/16/18/23/35/43/45/50/51/0xfe0d/0xff01），**未识别的扩展走 default 分支发空 `GenericExtension`**。某些服务器校验扩展 payload，空 payload 触发：
```
TLS handshake failed: remote error: tls: error decoding message
```
**模拟 Chrome 时**：`compress_certificate`(27) 真实 Chrome 带 brotli payload，走 GenericExtension 发空 → 握手失败。**必须从 Extensions 列表删除这类扩展**（JA3 略差但 CF 放行，grok_search 实证）。代码注释（dialer.go:431-434）也对 ECH(0xfe0d) 提到同样问题。

### ALPN / GREASE 约束
- **ALPN 用 `["http/1.1"]`**：实证 console.x.ai 应用层接受 HTTP/1.1；同时规避 Go net/http 的 HTTP/2 帧顺序（SETTINGS/WINDOW_UPDATE）被 CF 检测。若上游强制 HTTP/2 需另外评估（utls 不模拟 HTTP/2 帧顺序）。
- **GREASE 扩展 ≤ 2 个**：utls 限制（`apply TLS preset: at most 2 grease extensions are supported`）。Chrome 真实指纹的 GREASE 扩展放首尾各一个（如 `0x0a0a` / `0x2a2a`）。

---

## 验证模式：cmd probe

接入上游前，写 `backend/cmd/<name>/main.go` probe 实证链路，**不依赖 sub2api 运行**：
- TLS：用 `tlsfingerprint.NewDialer(profile, nil).DialTLSContext` 作 `http.Transport.DialTLSContext`，`ForceAttemptHTTP2: false`。
- **用假凭证**隔离 CF 因素（CF 过了会返回应用层 401，说明指纹够；不必先有真凭证）。
- **从文件读凭证**（如 grok-register accounts 文件 `email----password----sso`）避免明文进命令行/对话。
- 判读矩阵：`200`=全通 / `401`=凭证错（CF 已过）/ `403+html`=CF 挡 / `400-422`=请求体契约错。

参照：`backend/cmd/cfprobe/main.go`（grok_search console.x.ai 验证）。

---

## Common Mistakes

1. **用裸 `Do` 打 CF 后的上游** → 403。CF 后的上游必须 `DoWithTLS` + 浏览器指纹。
2. **Profile 字段留空当 Chrome 用** → 实际发 Node.js 指纹，CF 可能不放行。
3. **Extensions 列表含 compress_certificate(27) 等未知扩展** → 空 GenericExtension 触发 `error decoding message`。模拟 Chrome 时删掉。
4. **GREASE 扩展 >2** → utls 报错。
5. **ALPN 含 h2 但用 Go net/http** → HTTP/2 帧顺序是 Go 默认的，可能被 CF 的 HTTP/2 指纹识破。先用 `http/1.1` 实证。

---

## 案例：grok_search 过 console.x.ai CF

`service/openai_gateway_grok_search.go` 的 `grokSearchChromeProfile()` 构造 Chrome Profile（去 compress_certificate、ALPN `http/1.1`、GREASE `0x0a0a`/`0x2a2a`），`forwardGrokSearch` 用 `httpUpstream.DoWithTLS`。cfprobe 实证：
- 裸 curl → `403`（CF block）
- Chrome 指纹 + 假 SSO → `401` bad-credentials（CF 放行，应用层）
- Chrome 指纹 + 真 SSO → `200` + multi-agent SSE
