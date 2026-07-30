# 实施计划: CodeBuddy 转发请求体脱敏

## 步骤

1. [ ] 新建 `backend/internal/service/upstream_desensitize.go`
   - 移植 `sensitiveTerms` 词表（1:1 来自 `codebuddy2api/desensitize.py:38-125`）。
   - `buildSensitiveTermsRe()`：按词长降序拼接、`(?i)`、`\b` 边界，包级 `var` 单例。
   - `desensitizeText(s string) string`。
   - `DesensitizeOpts{Enabled,Compact bool}` + `DesensitizeOpenAIBody(body,opts)`。
   - chat / responses 字段处理（messages / system / instructions / input / tools.description）。
   - harness markers + compact 摘要常量（移植 `_HARNESS_USER_MARKERS` / `_CODEX_SYSTEM_MARKERS` /
     `_CODEX_CORE_SUMMARY` / `_RUNTIME_BLOCK_REPLACEMENTS` 等）。
   - `recover` 降级返回原 body。

2. [ ] `backend/internal/service/account.go` 新增开关方法
   - `IsUpstreamDesensitizeEnabled()`（读 `upstream_desensitize_enabled`）。
   - `UpstreamDesensitizeCompact()`（读 `upstream_desensitize_compact`，正向，默认 false=不压缩）。
   - 复用私有 `getExtraBool`（account.go:2121）。

3. [ ] `backend/internal/service/openai_gateway_forward.go` 插入脱敏
   - `Forward` 中 `flattenOpenAIResponsesNamespaces` 后、`originalBody := body` 前（~L74-76）插入调用。
   - 核对 passthrough / failover / WS 各分支使用的是脱敏后的 `body` 变量（见「待确认细节」）。

4. [ ] 单元测试 `backend/internal/service/upstream_desensitize_test.go`
   - 覆盖 design §9 全部场景。

5. [ ] 本地静态验证
   - `cd backend && go build ./...`
   - `go test ./internal/service/ -run Desensitize -v`
   - `go vet ./internal/service/`

6. [ ] 端到端验证（需用户配合，真实 codebuddy 渠道）
   - 配置一个指向 copilot.tencent.com 的 OpenAI 兼容渠道，Extra 开启 `upstream_desensitize_enabled`。
   - 用原触发拦截的客户端请求复测，确认不再被拦。
   - 关闭开关复测，确认行为还原。

## Review Gates
- 步骤 1-2 完成后：review 词表完整性、开关命名、插入点行号准确性。
- 步骤 5 通过后：进入端到端验证（步骤 6）。

## Rollback
- 改动面：1 个新建文件 + account.go 2 个方法 + Forward 1 段插入。
- 软回滚：渠道 Extra 关闭 `upstream_desensitize_enabled` 即刻生效关闭，无需回滚代码。
- 硬回滚：还原 Forward 插入段（单文件小改）。

## 待 implement 阶段确认的细节
- `buildUpstreamRequest` 最终发给 codebuddy 的 body 是 chat 还是 responses 格式
  （决定字段处理优先级与测试样本）。
- passthrough 分支（L125+）用的是 `body` 还是 `originalBody`，决定是否需要同步处理
  `originalBody`（初步判断插入点在 `originalBody` 赋值之前，二者一致，但需实读确认）。
- 是否需要为 Extra 开关补保存路径 `Normalize`（MVP 可不加）。
