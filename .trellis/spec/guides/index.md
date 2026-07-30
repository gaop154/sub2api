# Thinking Guides

> **Purpose**: Expand your thinking to catch things you might not have considered.

---

## Why Thinking Guides?

**Most bugs and tech debt come from "didn't think of that"**, not from lack of skill:

- Didn't think about what happens at layer boundaries → cross-layer bugs
- Didn't think about code patterns repeating → duplicated code everywhere
- Didn't think about edge cases → runtime errors
- Didn't think about future maintainers → unreadable code

These guides help you **ask the right questions before coding**.

---

## Available Guides

| Guide | Purpose | When to Use |
|-------|---------|-------------|
| [Code Reuse Thinking Guide](./code-reuse-thinking-guide.md) | Identify patterns and reduce duplication | When you notice repeated patterns |
| [Cross-Layer Thinking Guide](./cross-layer-thinking-guide.md) | Think through data flow across layers | Features spanning multiple layers |

---

## Quick Reference: Thinking Triggers

### When to Think About Cross-Layer Issues

- [ ] Feature touches 3+ layers (API, Service, Component, Database)
- [ ] Data format changes between layers
- [ ] Multiple consumers need the same data
- [ ] You're not sure where to put some logic
- [ ] You are adding an event kind, JSONL record, RPC payload, or config field
- [ ] UI / command code starts casting raw payload fields directly

→ Read [Cross-Layer Thinking Guide](./cross-layer-thinking-guide.md)

### When to Think About Code Reuse

- [ ] You're writing similar code to something that exists
- [ ] You see the same pattern repeated 3+ times
- [ ] You're adding a new field to multiple places
- [ ] **You're modifying any constant or config**
- [ ] **You're creating a new utility/helper function** ← Search first!
- [ ] Two files read the same untyped payload field with local casts
- [ ] Multiple branches update the same derived state from `kind` / `action`

→ Read [Code Reuse Thinking Guide](./code-reuse-thinking-guide.md)

### When Adding a New Platform

- [ ] 上游是否在 Cloudflare 后？→ 先写 `cmd/` probe 用**假凭证**实测 CF（过 CF 会返回应用层 401），别等真凭证
- [ ] 建账号/分组是否被 platform CHECK 阻塞？→ `accounts`/`groups` 无 CHECK（基础可用不需 migration）；`user_platform_quotas`/`channel_monitors`/`composite_model_routes` 有 CHECK（启用对应功能才需放开）
- [ ] 是否复用了其他平台的错误处理 / 凭证兑换链路？→ 新平台要物理隔离（独立 forwarder + 独立错误处理 + 独立 admin 导入）
- [ ] 凭证 key 前后端是否对齐 forwarder 的 `GetCredential`？（导入接口写的 key 必须 == forwarder 读的 key）
- [ ] domain 平台常量是否同步 `service/domain_constants.go` re-export？（否则 service 包 undefined）
- [ ] 前端：建账号 + 建分组 + 筛选 + 图标 + badge + i18n 是否都加新平台？（grep 平台名扫 `views/admin` + `components` 硬编码，否则用户会报"选项缺失"）
- [ ] 后端：`handler/admin/group_handler.go` 的 CreateGroupRequest/UpdateGroupRequest `Platform` oneof 是否加新平台？（不加则建分组 400 `failed on the 'oneof' tag`；composite TargetPlatform 不加）
- [ ] 调度快照：`scheduler_snapshot_service.go` 的 `schedulerSnapshotPlatforms()` + bulk event switch case 是否加新平台？（不加则账号进不了调度池 → "无可用账号"，比 CHECK 更隐蔽）
- [ ] 运行时分发点：转发(`openai_gateway_forward.go`) + 调度快照 + 测试账号(`TestAccountConnection`) + 同步模型(`FetchUpstreamSupportedModels`) + 账号可用模型(`GetAvailableModels`) + 网关路由层(`gateway.go` `isOpenAIResponsesCompatibleGatewayPlatform` 等 `getGroupPlatform` switch) 多套独立分发，新平台是否每处都有归属？（漏一处则该环节报错/返回错平台数据；`GetAvailableModels` 漏了 → 测试弹窗下拉全是 Claude；路由层漏了 → `/v1/chat/completions` 报 `api_key not found in credentials`，看似凭证问题实为路由进错链路）

→ Read [New Platform Onboarding](../backend/new-platform.md) + [Upstream Egress & TLS Fingerprint](../backend/upstream-egress.md)

### When Verifying AI Cross-Review Results

- [ ] Reviewer claims "user input can be malicious" → Check the actual data source (internal manifest? user config? external API?)
- [ ] Reviewer flags "missing validation" → Is the data from a trusted internal source?
- [ ] Reviewer says "behavior change" → Read the code comments — is it intentional design?
- [ ] Reviewer identifies a "bug" in test → Mentally delete the feature being tested — does the test still pass? If yes → tautological test

**Common AI reviewer false-positive patterns**:
1. **Trust boundary confusion**: Treating internal data (bundled JSON manifests) as untrusted external input
2. **Ignoring design comments**: Flagging intentional behavior documented in code comments as bugs
3. **Variable misreading**: Not tracing a variable to its actual definition (e.g., Map keyed by path vs name)

**Verification rule**: Every CRITICAL/WARNING finding must be verified against the actual code before prioritizing. Budget ~35% false-positive rate for AI reviews.

---

## Pre-Modification Rule (CRITICAL)

> **Before changing ANY value, ALWAYS search first!**

```bash
# Search for the value you're about to change
grep -r "value_to_change" .
```

This single habit prevents most "forgot to update X" bugs.

---

## How to Use This Directory

1. **Before coding**: Skim the relevant thinking guide
2. **During coding**: If something feels repetitive or complex, check the guides
3. **After bugs**: Add new insights to the relevant guide (learn from mistakes)

---

## Contributing

Found a new "didn't think of that" moment? Add it to the relevant guide.

---

**Core Principle**: 30 minutes of thinking saves 3 hours of debugging.
