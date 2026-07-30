# Directory Structure

> How backend code is organized in this project.

---

## Overview

后端采用典型的分层架构，使用依赖注入（Wire）进行组件装配。请求流向为：`routes → handler → service → repository`

核心分层：
- **cmd/**：应用入口点
- **internal/handler/**：HTTP 处理器层，处理请求解析和响应
- **internal/service/**：业务逻辑层
- **internal/repository/**：数据访问层
- **internal/server/**：HTTP 服务器、路由和中间件

---

## Directory Layout

```
backend/
├── cmd/
│   └── server/              # 应用主入口
│       ├── main.go          # 程序入口
│       ├── wire.go          # Wire 依赖注入定义
│       └── wire_gen.go      # Wire 生成的代码（不要手动编辑）
├── internal/
│   ├── config/              # 配置加载和验证
│   ├── domain/              # 领域模型和实体
│   ├── handler/             # HTTP 处理器
│   │   └── dto/            # 数据传输对象
│   ├── middleware/          # Gin 中间件
│   ├── model/               # Ent 生成的实体模型
│   ├── payment/             # 支付集成
│   ├── repository/          # 数据访问层
│   ├── securityaudit/       # 安全审计
│   ├── server/              # HTTP 服务器和路由
│   ├── service/             # 业务逻辑层
│   ├── setup/               # 安装向导
│   ├── util/                # 工具函数
│   ├── web/                 # 内嵌的前端静态资源
│   └── pkg/                 # 内部包
│       ├── logger/          # 日志组件
│       ├── response/        # 统一响应格式
│       └── usagestats/      # 使用统计
├── ent/                     # Ent ORM schema 定义
├── migrations/              # 数据库迁移文件（SQL，权威来源）
└── resources/               # 资源文件
```

---

## Module Organization

### 新增功能模块时的组织原则

1. **Handler 层** (`internal/handler/`)
   - 每个 API 端点对应一个方法
   - 使用结构体组织相关处理器
   - 命名规范：`{Resource}Handler`（如 `UserHandler`、`GatewayHandler`）
   - 构造函数命名：`New{Resource}Handler(...)`

2. **Service 层** (`internal/service/`)
   - 核心业务逻辑实现
   - 命名规范：`{Resource}Service`（如 `UserService`、`DashboardService`）
   - 构造函数接受 repository 接口，便于测试

3. **Repository 层** (`internal/repository/`)
   - 数据访问接口和实现
   - 接口定义在 service 包中，实现在 repository 包中
   - 使用 Ent 进行 ORM 操作，复杂查询可使用原生 SQL

4. **路由注册** (`internal/server/routes/`)
   - 按功能模块分组：用户 API、管理 API、网关 API
   - 用户 API：`/api/v1/*`
   - 管理 API：`/api/v1/admin/*`
   - 网关 API：`/v1/*`、`/v1beta/*`、`/responses` 等

---

## Naming Conventions

### 文件命名
- Go 文件：使用蛇形命名法（snake_case）`user_handler.go`
- 测试文件：`{源文件名}_test.go`，如 `user_handler_test.go`

### 包命名
- 包名使用简短、小写的单词
- 避免下划线或混合大小写
- 包名应描述其内容，如 `handler`、`service`、`repository`

### 类型命名
- 接口：通常是功能描述，如 `UserService`、`DashboardStatsCache`
- 结构体：使用 PascalCase
- 常量：使用 PascalCase 或 camelCase
- 私有变量：使用 camelCase

### 函数/方法命名
- 公开函数/方法：使用 PascalCase
- 私有函数/方法：使用 camelCase
- 构造函数：`New{TypeName}()`
- Getter 方法：省略 `Get` 前缀（如 `user.ID()` 而非 `user.GetID()`）

---

## Examples

### Handler 示例
- `internal/handler/user_handler.go` - 用户相关 API 处理
- `internal/handler/gateway_handler.go` - 网关转发处理

### Service 示例
- `internal/service/dashboard_service.go` - 仪表盘统计服务
- `internal/service/auth_service.go` - 认证服务

### Repository 示例
- 查找 `internal/repository/` 下的具体实现
- 接口定义通常在对应的 service 文件中

### 路由注册示例
- `internal/server/routes/user.go` - 用户路由
- `internal/server/routes/gateway.go` - 网关路由

---

## 特殊约定

### 依赖注入（Wire）
- 所有依赖通过 `cmd/server/wire.go` 中的 ProviderSet 定义
- 修改 Provider 后必须运行 `make generate` 重新生成 `wire_gen.go`
- 不要手动编辑 `wire_gen.go`

### 数据库迁移
- Schema 变更的权威来源是 `backend/migrations/` 下的 SQL 文件
- `ent/schema` 中的定义用于生成 Ent 代码，但迁移由 SQL 驱动
- 启动时 `repository.InitEnt()` 会自动执行迁移

### 配置管理
- 配置文件：`backend/config.yaml`
- 配置定义和加载：`internal/config/`
- 环境变量优先级高于配置文件

### 测试组织
- 单元测试：与源文件同目录
- 集成测试：`backend/internal/**/*_test.go`
- E2E 测试：专门的测试目录
- 使用 `make test` 运行所有测试和 linting
