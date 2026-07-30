# Implement: grok_search 账号健壮性增强（403/429 状态码识别）

## 执行清单（有序）

### Step 1：常量提取（`openai_gateway_grok_search.go`）
- 新增 `grokSearchFreeQuotaCooldown = 24 * time.Hour`（免费额度耗尽冷却）。
- 若 429 普通限流的 `5 * time.Minute` 当前是内联字面量，提取为 `grokSearchRateLimitCooldown = 5 * time.Minute`。

### Step 2：辅助识别函数（同文件，与 `handleGrokSearchAccountUpstreamError` 附近）
- `isGrokSearchFreeQuotaExhausted(body []byte) bool`：`strings.Contains(strings.ToLower(string(body)), "free usage quota")`。
- `isGrokSearchPermissionDenied(body []byte) bool`：lower 后含 `permission-denied` / `permission_denied` / `access to the chat endpoint is denied` 任一。
- 简体中文注释说明识别依据（实测 body / grok2api 语义参照），强调 `resource-exhausted` code 不单看的原因。

### Step 3：改造 `handleGrokSearchAccountUpstreamError`（:486）
- 删除 ` _ = responseBody; _ = headers`（启用参数）。
- **403 新增 case**（顺序：CF → 权限 → 否则落 default 不处理）：
  ```go
  case http.StatusForbidden:
      if httputil.IsCloudflareChallengeResponse(statusCode, headers, responseBody) {
          return // CF 出口问题，不惩罚账号
      }
      if isGrokSearchPermissionDenied(responseBody) {
          s.markGrokSearchReauthRequired(ctx, account) // SSO 权限丢失，同 401
          return
      }
      // 其它 403：不特殊处理（落 default 语义）
  ```
- **429 改造 case**（顺序：CF → 免费额度 → 普通限流）：
  ```go
  case http.StatusTooManyRequests:
      if httputil.IsCloudflareChallengeResponse(statusCode, headers, responseBody) {
          return // CF 挑战可能伪装成 429，不惩罚账号
      }
      if isGrokSearchFreeQuotaExhausted(responseBody) {
          s.tempUnscheduleGrokSearch(ctx, account, grokSearchFreeQuotaCooldown, "grok_search free usage quota exhausted")
          return
      }
      s.tempUnscheduleGrokSearch(ctx, account, grokSearchRateLimitCooldown, "grok_search rate limited")
  ```
- 401 / 5xx / default：不动。
- import 补 `httputil` 包（`internal/util/httputil`）。

### Step 4：测试（新增 `openai_gateway_grok_search_statuscode_test.go`）
覆盖：
- **429 三分支**：
  - body=`{"code":"resource-exhausted","error":"Free usage quota exceeded..."}` → 验证 tempUnschedule 时长 24h（可断言 BlockAccountScheduling 的 until 在 ~24h 后）。
  - body=`{"error":"rate limited"}`（普通限流）→ 5min。
  - CF challenge body / `cf-mitigated: challenge` 头 → 不 blocked。
- **403 三分支**：
  - CF（`IsCloudflareChallengeResponse` 判定 body / 头）→ 不 blocked、不 reauth。
  - `{"code":"permission-denied","error":"Access to the chat endpoint is denied"}` → markReauthRequired（status=error）。
  - 普通无关 403 body → 不特殊处理（不 blocked、不 reauth）。
- **隔离回归**：grok/openai 的错误处理函数未被调用/未改（grep 确认无交集即可，或断言 grok_search 改动不影响其它平台 handler）。
- 注意：`handleGrokSearchAccountUpstreamError` 依赖 `s.accountRepo`/`BlockAccountScheduling`；测试可复用 `openai_gateway_grok_search_selection_test.go` 的 `OpenAIGatewayService{}` + mock repo 模式，或仅断言 `isOpenAIAccountRuntimeBlocked`/`account.TempUnschedulableUntil` 的副作用边界。

### Step 5：验证
```bash
cd backend
go build ./...
go vet ./internal/service/...
go test ./internal/service -run 'GrokSearch' -count=1 -v
```
全过才算完成。

## Review Gate（实施前）
- prd/design/implement 已 review，task.py start 后再实施。
- 实施派 `trellis-implement` 子 agent，完成后 `trellis-check` 复核 → `trellis-update-spec`（补状态码处理约定到 new-platform.md）→ commit。

## Rollback Point
- 改动单文件（`openai_gateway_grok_search.go`）+ 新增测试，不改签名、不碰调度/forwarder/其它平台。
- 回滚：`git checkout -- backend/internal/service/openai_gateway_grok_search.go` + 删测试文件。

## 约束
- 禁止 commit（trellis-implement）。
- 简体中文注释。
- 429 判定顺序：CF → 免费额度 → 普通限流（顺序错则误判，最易出 bug）。
- 复用 `httputil.IsCloudflareChallengeResponse`，不自己重写 CF 检测。
