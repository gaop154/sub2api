# PoC 验证执行包 — grok_search 平台

> 代码已就绪：`PlatformGrokSearch` 常量、`forwardGrokSearch` forwarder（`openai_gateway_grok_search.go`）、
> 分发分支（`openai_gateway_forward.go`），`go build ./...` 通过。
>
> 目标：验证 **AC-3（console.x.ai + SSO 链路是否跑通）** 与 **AC-1（sub2api 端到端 multi-agent 搜索不再 402）**。
>
> 验证顺序：**Part A（直验 console 链路）→ Part B（建 sub2api 账号）→ Part C（sub2api 端到端）**。
> Part A 不依赖 sub2api，是定位 CF / SSO / 请求体卡点的最快路径，**必须先做**。

---

## 前置准备

- **SSO token**：grok-register 产出的 `sso` 值（`sso` 与 `sso-rw` 同值，失效需重导）。
- 所有命令里 `<SSO_TOKEN>` 替换为真实 SSO 值。**SSO 是敏感凭证，建议直接在命令行本地替换，不要贴到对话/日志。**

---

## Part A：直接验证 console.x.ai + SSO 链路（AC-3，最先做）

绕过 sub2api，直接打 `console.x.ai/v1/responses`，验证 SSO 有效性 / CF / 请求体契约。
请求构造与 `buildGrokSearchRequest` + `normalizeGrokSearchRequestBody` 完全一致（design §3/§4）。

### A.1 先验链路通不通（用任意已知有效模型，剥离 multi-agent 模型名未知）

```bash
curl -i https://console.x.ai/v1/responses \
  -H "Cookie: sso=<SSO_TOKEN>; sso-rw=<SSO_TOKEN>" \
  -H "Authorization: Bearer anonymous" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -H "Origin: https://console.x.ai" \
  -H "Referer: https://console.x.ai/" \
  -H "x-cluster: https://us-east-1.api.x.ai" \
  -H "User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36" \
  -H 'Sec-Ch-Ua: "Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"' \
  -H "Sec-Ch-Ua-Mobile: ?0" \
  -H 'Sec-Ch-Ua-Platform: "Windows"' \
  -d '{
    "model": "grok-4",
    "input": [{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
    "store": false,
    "include": ["reasoning.encrypted_content"],
    "stream": false
  }'
```

### A.2 链路通后再验 multi-agent 搜索（AC-1 核心）

```bash
curl -i https://console.x.ai/v1/responses \
  -H "Cookie: sso=<SSO_TOKEN>; sso-rw=<SSO_TOKEN>" \
  -H "Authorization: Bearer anonymous" \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -H "Origin: https://console.x.ai" \
  -H "Referer: https://console.x.ai/" \
  -H "x-cluster: https://us-east-1.api.x.ai" \
  -H "User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36" \
  -H 'Sec-Ch-Ua: "Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"' \
  -H "Sec-Ch-Ua-Mobile: ?0" \
  -H 'Sec-Ch-Ua-Platform: "Windows"' \
  -d '{
    "model": "grok-4.20-multi-agent-0309",
    "input": [{"role":"user","content":[{"type":"input_text","text":"搜一下今天的新闻"}]}],
    "store": false,
    "include": ["reasoning.encrypted_content"],
    "tools": [{"type":"web_search"}],
    "reasoning": {"effort": "high"},
    "stream": true
  }'
```

> 若 A.2 返回 model not found，说明 multi-agent 模型名不对。从 console 网页（F12 → Network →
> 真实 /v1/responses 请求体）抓取真实 model ID，回填到 sub2api 账号的 model_mapping（见 Part B 注释）。

### A.x 判读矩阵（Review Gate）

| 返回 | 含义 | 下一步 |
|---|---|---|
| **200** + SSE/JSON | 链路通 ✅ | 进 Part B/C 验 sub2api 集成 |
| **401** | SSO 无效或未注入 | 核对 SSO 值；确认 cookie 格式 `sso=...; sso-rw=...` |
| **403** + HTML（cf/challenge） | **CF 拦截** | 触发 design §8 **L1**：`DoWithTLS` + Chrome TLS profile + `Sec-Ch-Ua*` |
| **400–422** JSON error | 请求体契约问题 | 看 error message：model 名 / input 结构 / 字段，补 `normalizeGrokSearchRequestBody` 转换 |
| **404** | URL/路径错 | 核对 `console.x.ai/v1/responses`（可能区域/路径变体） |
| **429** | 限流 | 换号或等待，链路本身通 |

---

## Part B：sub2api 建账号 / 分组 / API Key（AC-1 集成）

在 sub2api 的 Postgres 上执行（DO block 一次完成，仅改顶部 3 个变量）。

```sql
DO $$
DECLARE
  v_sso      TEXT := '<SSO_TOKEN>';                         -- ← 替换真实 SSO
  v_user_id  BIGINT := 1;                                   -- ← 改成你的用户 ID（管理员通常 1）
  v_apikey   TEXT := 'sk-grok-search-poc-0001';             -- ← 自定义，Part C curl 用
  v_account_id BIGINT;
  v_group_id   BIGINT;
BEGIN
  INSERT INTO accounts (name, platform, type, credentials, concurrency, priority,
                        status, schedulable, rate_multiplier, extra, created_at, updated_at)
  VALUES ('grok-search-poc', 'grok_search', 'apikey',
          jsonb_build_object('sso_token', v_sso, 'base_url', 'https://console.x.ai'),
          3, 50, 'active', true, 1.0, '{}'::jsonb, NOW(), NOW())
  RETURNING id INTO v_account_id;

  INSERT INTO groups (name, platform, status, rate_multiplier, subscription_type,
                      created_at, updated_at)
  VALUES ('grok-search-poc', 'grok_search', 'active', 1.0, 'standard', NOW(), NOW())
  RETURNING id INTO v_group_id;

  INSERT INTO account_groups (account_id, group_id, priority, created_at)
  VALUES (v_account_id, v_group_id, 50, NOW());

  INSERT INTO api_keys (user_id, key, name, group_id, status, quota, created_at, updated_at)
  VALUES (v_user_id, v_apikey, 'grok-search-poc', v_group_id, 'active', 0, NOW(), NOW());

  RAISE NOTICE '✅ account_id=%, group_id=%, api_key=%', v_account_id, v_group_id, v_apikey;
END $$;
```

> **模型映射**：PoC 不配 account 级 `model_mapping`，客户端 model 名直接透传到 console
> （`forwardGrokSearch` 里 `GetMappedModel` 未命中则返回原名）。若 Part A.2 发现真实 model ID
> 与 `grok-4.20-multi-agent-0309` 不同，在此账号的 `extra` 配 model_mapping（参考 grok 账号）。

---

## Part C：sub2api 端到端验证（AC-1）

启动 sub2api 服务后，用 Part B 的 API Key 打 sub2api 的 Responses 端点。
（`forwardGrokSearch` 从 Responses 路径进，body 走 console 契约转换。）

```bash
curl -i http://localhost:<PORT>/v1/responses \
  -H "Authorization: Bearer sk-grok-search-poc-0001" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "grok-4.20-multi-agent-0309",
    "input": [{"role":"user","content":[{"type":"input_text","text":"搜一下今天的新闻"}]}],
    "stream": true
  }'
```

预期：sub2api 选 grok_search 账号 → `forwardGrokSearch` → console.x.ai → **200 + 搜索结果，不再 402**。

### 回滚（PoC 失败时）
```sql
DELETE FROM api_keys WHERE key = 'sk-grok-search-poc-0001';
DELETE FROM account_groups WHERE account_id = (SELECT id FROM accounts WHERE name='grok-search-poc');
DELETE FROM accounts WHERE name = 'grok-search-poc';
DELETE FROM groups WHERE name = 'grok-search-poc';
```
代码回滚：删 `openai_gateway_grok_search.go` + `openai_gateway_forward.go` 分发分支 + 常量三行，零影响现有 grok。

---

## Review Gate（PoC 完成后）

- **A.2 / C 返回 200** → AC-1/AC-3 达成，进 Phase 1 完整版（migration / admin 导入接口 / 前端 / composite 接入）。
- **403+html（CF）** → 先实现 design §8 L1（`DoWithTLS` + Chrome profile），再评估。
- **400-422（请求体）** → 补 `normalizeGrokSearchRequestBody` 转换（对照 console 真实请求体）。
- **401** → SSO 失效，重导。
