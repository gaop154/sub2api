# Quality Guidelines

> Code quality standards for backend development.

---

## Overview

项目通过 golangci-lint、测试要求和代码审查流程确保代码质量。所有代码必须通过 `make test`（包括 lint 和单元测试）才能合并。

**质量检查工具**：
- golangci-lint（代码质量检查）
- Go 单元测试（unit test）
- Go 集成测试（integration test）
- Wire 代码生成检查

---

## Forbidden Patterns

### 禁止的模式

1. **循环内数据库调用**
```go
// ❌ 禁止：在循环内进行 DB 调用
for _, userID := range userIDs {
    user, err := repo.GetUser(ctx, userID)  // N+1 查询
}

// ✅ 正确：批量查询后在循环内使用
users, err := repo.GetUsersByIDs(ctx, userIDs)
for _, user := range users {
    // 处理用户
}
```

2. **循环内 Feign/HTTP 调用**
```go
// ❌ 禁止：在循环内进行远程调用
for _, account := range accounts {
    status, err := checkAccountStatus(ctx, account)  // N 个远程调用
}

// ✅ 正确：批量检查或并发调用
statuses, err := checkAccountStatusBatch(ctx, accounts)
```

3. **Service 层跨依赖**
```go
// ❌ 禁止：Service 直接依赖其他 Service 的 Mapper
type UserService struct {
    userRepo    repository.UserRepository
    groupRepo   repository.GroupRepository  // 应该通过 GroupService 访问
}

// ✅ 正确：通过注入对应 Service 访问
type UserService struct {
    userRepo     repository.UserRepository
    groupService *GroupService
}
```

4. **在 SQL 注解中写复杂 SQL**
```go
// ❌ 禁止：SQL 应该写在 XML 文件中
// （项目使用 Ent + 迁移文件，复杂查询应写在 repository 中）
```

5. **硬编码配置值**
```go
// ❌ 禁止：硬编码配置
timeout := 30 * time.Second

// ✅ 正确：使用配置
timeout := time.Duration(cfg.Gateway.TimeoutSeconds) * time.Second
```

---

## Required Patterns

### 必须使用的模式

1. **使用 Wire 进行依赖注入**
```go
// cmd/server/wire.go
var ProviderSet = wire.NewSet(
    // 提供所有依赖
    repository.ProviderSet,
    service.ProviderSet,
    handler.ProviderSet,
)
```

2. **使用 response 包返回响应**
```go
// ✅ 使用统一的 response 包
response.Success(c, data)
response.ErrorFrom(c, err)
response.BadRequest(c, "Invalid request")

// ❌ 不直接使用 c.JSON()
c.JSON(200, data)  // 不要这样做
```

3. **使用 context 传递超时和取消**
```go
func (s *UserService) GetUser(ctx context.Context, id int) (*User, error) {
    // 所有数据库和外部调用都应该接受 ctx
    return s.repo.Get(ctx, id)
}
```

4. **错误处理包装**
```go
// ✅ 正确：错误包装保留上下文
if err := s.repo.Create(ctx, user); err != nil {
    return nil, fmt.Errorf("failed to create user: %w", err)
}
```

5. **使用结构体组织相关 Handler**
```go
// ✅ 使用结构体组织相关处理器
type UserHandler struct {
    userService *service.UserService
    authService *service.AuthService
}

func NewUserHandler(...) *UserHandler {
    return &UserHandler{...}
}
```

---

## Testing Requirements

### 测试分层

1. **单元测试**：测试单个函数/方法
   - 命名：`{源文件名}_test.go`
   - 使用 mock 接口测试 Service 层
   - 不依赖外部服务（数据库、Redis、HTTP）

2. **集成测试**：测试模块间交互
   - 标签：`// +build integration`
   - 使用测试数据库和 Redis
   - 测试完整流程

3. **E2E 测试**：端到端测试
   - 标签：`// +build e2e`
   - 测试完整用户场景
   - 使用真实外部依赖或 Docker

### 测试命令

```bash
# 运行所有测试和 linting
make test

# 仅运行单元测试
make test-unit

# 仅运行集成测试
make test-integration

# 运行单个测试
go test ./internal/handler -run TestUpdateProfile

# 运行特定包的测试
go test ./internal/service/...
```

### 测试覆盖率

目标：
- 新代码覆盖率：≥ 80%
- 核心业务逻辑覆盖率：≥ 90%

查看覆盖率：
```bash
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### 测试示例

```go
func TestUserService_CreateUser(t *testing.T) {
    // Arrange
    mockRepo := &MockUserRepository{}
    service := NewUserService(mockRepo)
    req := CreateUserRequest{
        Email: "test@example.com",
        Password: "password123",
    }

    // Act
    user, err := service.CreateUser(context.Background(), req)

    // Assert
    assert.NoError(t, err)
    assert.NotNil(t, user)
    assert.Equal(t, req.Email, user.Email)
    mockRepo.AssertCalled(t, "Create", mock.Anything, mock.Anything)
}
```

---

## Linting Rules

### golangci-lint 配置
配置文件：`.golangci.yml`

主要规则：
- **goimports**：自动格式化 import
- **golint**：代码风格检查
- **go vet**：静态分析
- **errcheck**：检查错误处理
- **staticcheck**：额外静态检查
- **gocyclo**：循环复杂度检查（复杂度 ≤ 15）
- **dupl**：重复代码检测

### 运行 linting

```bash
# 自动运行（在 make test 中包含）
golangci-lint run ./...

# 单独运行
make lint
```

---

## Code Review Checklist

### 功能性
- [ ] 代码实现了需求功能
- [ ] 边界条件处理正确
- [ ] 错误处理完整
- [ ] 并发安全性考虑
- [ ] 资源清理（defer、Close）

### 代码质量
- [ ] 通过所有 linting 检查
- [ ] 通过所有测试
- [ ] 测试覆盖率达标
- [ ] 无重复代码
- [ ] 函数复杂度合理（≤ 15）

### 架构和设计
- [ ] 遵循分层架构
- [ ] 依赖注入正确使用 Wire
- [ ] Service 层只依赖自身 Mapper
- [ ] 跨服务数据通过注入 Service
- [ ] 接口定义合理

### 安全性
- [ ] 无 SQL 注入风险
- [ ] 敏感信息不记录日志
- [ ] 认证/授权检查
- [ ] 输入验证完整
- [ ] 输出编码正确

### 性能
- [ ] 无 N+1 查询
- [ ] 无循环内远程调用
- [ ] 使用批量操作
- [ ] 合理使用缓存
- [ ] 数据库查询优化

### 可维护性
- [ ] 命名清晰准确
- [ ] 注释必要且准确
- [ ] 错误消息有意义
- [ ] 日志级别正确
- [ ] 日志包含上下文

---

## Common Mistakes

### 常见错误

1. **忘记错误处理**
```go
// ❌ 错误：忽略错误
user, _ := repo.GetUser(ctx, id)

// ✅ 正确：处理错误
user, err := repo.GetUser(ctx, id)
if err != nil {
    return nil, fmt.Errorf("failed to get user: %w", err)
}
```

2. **循环内数据库调用**
```go
// ❌ 错误：N+1 问题
for _, id := range ids {
    user, _ := repo.GetUser(ctx, id)
}

// ✅ 正确：批量查询
users, _ := repo.GetUsersByIDs(ctx, ids)
```

3. **跨层直接依赖**
```go
// ❌ 错误：Service 直接依赖其他层的数据访问
type UserService struct {
    userRepo   repository.UserRepository
    groupRepo  repository.GroupRepository  // 应该通过 GroupService
}

// ✅ 正确：通过 Service 层访问
type UserService struct {
    userRepo    repository.UserRepository
    groupSvc    *GroupService
}
```

4. **硬编码配置**
```go
// ❌ 错误：硬编码
if count > 100 {  // 硬编码限制
    return ErrTooMany
}

// ✅ 正确：使用配置
if count > cfg.MaxItems {
    return ErrTooMany
}
```

5. **缺少测试**
```go
// ❌ 错误：新功能没有测试
// 新增了 CreateUser 方法但没有任何测试

// ✅ 正确：为每个新功能添加测试
func TestUserService_CreateUser(t *testing.T) {
    // 测试代码
}
```

---

## Examples

### 查看高质量代码示例
- `internal/service/dashboard_service.go` - 服务层组织
- `internal/handler/user_handler.go` - 处理器层组织
- `internal/repository/user_repository.go` - 仓储层组织

---

## Important Notes

1. **所有代码必须通过 make test**：包括 linting 和测试
2. **修改 Provider 后必须重新生成 Wire 代码**：`make generate`
3. **数据库变更必须通过迁移文件**：不要直接修改 schema
4. **Service 层保持单一职责**：每个 Service 专注一个领域
5. **避免循环内 DB/HTTP 调用**：使用批量或并发操作
6. **错误处理要完整**：不要忽略任何错误
7. **代码审查必须通过**：按 checklist 逐项检查
