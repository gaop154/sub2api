# PRD: CodeBuddy 转发请求体脱敏

## 目标

为 sub2api 的 OpenAI 兼容转发链路增加「发往上游前对请求体做零宽脱敏」的能力，
缓解将腾讯 CodeBuddy（copilot.tencent.com）作为 OpenAI 兼容渠道接入时，
其内容审核对客户端 system / harness / tools 模板中合规声明术语（DoS / exploit / credential 等）
的误拦截。

脱敏为可选的 **per-channel** 能力，默认关闭，仅对显式开启的渠道生效，不影响其它渠道。

## 背景与已确认事实

- **接入方式**：CodeBuddy（`copilot.tencent.com/v2/chat/completions`，OpenAI 兼容）已作为
  普通 OpenAI 兼容渠道接入 sub2api（渠道 base_url 指向 copilot.tencent.com），走 sub2api
  现成的 OpenAI 转发路径（`OpenAIGatewayService.Forward`）。**无需新建专用平台适配器。**
- **问题现象**：该渠道转发请求时，后端内容审核返回
  「抱歉，系统检测到您当前输入的信息存在敏感内容，我无法响应您的请求，请检查后重新输入。」
  原因是客户端（如 Claude Code / Codex CLI）注入的合规 system 模板与 tools 描述里包含
  「拒绝作恶」语境的安全术语，被后端关键词审核误判。
- **已验证方案**：`C:\idealProject\github\codebuddy2api\desensitize.py` 已实现并验证过同类问题的
  脱敏——对一组「合规声明高频词」在词内插入零宽空格（U+200B），打断后端关键词匹配，
  人/模型阅读无差别；可选压缩超长 harness system 模板为短摘要，进一步降低误拦率。
- **本任务 = 把 desensitize.py 的逻辑移植为 Go，接入 sub2api 的 OpenAI 转发链路。**
- sub2api 后端为 Go（Gin + ent + wire），body 在转发链路中以 `[]byte` 流转。

## 需求

### REQ-1: 脱敏核心逻辑（移植 desensitize.py → Go）
- 1:1 移植 `SENSITIVE_TERMS` 词表（攻击 / 安全术语 + Claude / Anthropic 品牌词）。
- 实现 `desensitizeText`：按词长降序的正则，对匹配词在第 1 个字符后插入 U+200B。
- 对 system / developer 角色消息文本、tools 的 description 字段做脱敏。
- 实现 harness 识别（Codex / Claude Code 注入的 user 上下文）与可选压缩。

### REQ-2: 接入 OpenAI 转发链路
- 在 `OpenAIGatewayService.Forward`（`openai_gateway_forward.go`）的 body 规整段之后、
  业务分流之前，对 `body []byte` 执行脱敏，替换为脱敏后的新 body。
- 脱敏后的 body 须被所有下游分支（buildUpstreamRequest、passthrough、failover 等）一致使用。
- 兼容 chat 格式（messages / system / tools）与 responses 格式（input / instructions / tools）。

### REQ-3: per-channel 开关
- 新增渠道级配置（存 `Account.Extra`，参考 `IsOpenAIPassthroughEnabled` 等 extra-based 先例）：
  - `upstream_desensitize_enabled`（bool）：是否启用脱敏，默认 false。
  - `upstream_desensitize_compact`（bool）：是否压缩 system / harness；默认 false
    （即启用脱敏时默认仅做零宽插入、保留 system 原文，最保真；需要更强脱敏再勾选开启压缩）。
- 通过 `Account` 上的公有方法暴露；不限制平台 / 类型（任何 OpenAI 兼容渠道均可开启）。

### REQ-4: 可观测与可回退
- 脱敏为幂等、可关停：关闭开关后请求体原样转发，行为与现状一致。
- 沿用现有 body normalize 步骤的「替换 body」风格，不引入新的请求失败路径
  （脱敏内部异常时降级为原 body 转发，绝不阻断请求）。

## 验收标准

- [ ] AC-1: 在某个指向 copilot.tencent.com 的 OpenAI 兼容渠道（Extra 开启脱敏）上，
      原本触发内容审核拦截的请求，开启脱敏后能正常返回（不再出现敏感内容拦截文案）。
- [ ] AC-2: 未开启脱敏的渠道，请求体与响应行为与改动前完全一致（零影响）。
- [ ] AC-3: 脱敏后请求体里，system / tools 中命中的敏感词被正确插入 U+200B；
      普通用户对话内容不被改动（仅处理 system / developer / harness / tools）。
- [ ] AC-4: compact 两端均可配——`upstream_desensitize_compact` 开启时压缩 harness system
      为短摘要；默认（不勾）时 system 原文保留、仅做零宽插入。
- [ ] AC-5: 脱敏逻辑为独立 Go 文件 + 单元测试，覆盖词表命中、大小写、content blocks、
      tools 递归、chat / responses 两格式、compact 开 / 关等场景。

## 范围外

- ~~CodeBuddy 专用平台适配器、Runs / ACP 协议、SSE 事件转发~~（原 prd 基于已废弃的
  `codebuddy接口文档.txt`，与本任务实际用法无关，整体砍掉）。
- CodeBuddy token 过期自动刷新、CodeBuddy 模型列表获取（若需要另起任务）。
- 脱敏词表的外部化配置（本任务硬编码词表，与 desensitize.py 一致；后续可扩展）。
- Anthropic 转发路径（`GatewayService.Forward`）的脱敏——本任务仅覆盖 OpenAI 路径。
  若后续 Anthropic 渠道也接 copilot.tencent.com 再扩展。

## 技术约束

- Go 1.25.x；JSON 处理沿用项目既有库（tidwall / gjson、sjson）或 `map[string]any` 反序列化。
- 脱敏逻辑须为纯函数、无副作用、可单测，不依赖请求上下文。
- 遵循项目既有 per-channel 开关模式（`Account.Extra` + 公有 getter）。
