# Database Guidelines

> Database patterns and conventions for this project.

---

## Overview

项目使用 PostgreSQL 作为主数据库，Redis 用于缓存和分布式锁。ORM 使用 Ent，但数据库 schema 的权威来源是 `backend/migrations/` 下的 SQL 迁移文件。

**技术栈**：
- PostgreSQL 15+（主数据库）
- Redis 7+（缓存/队列/分布式锁）
- Ent（ORM）

---

## Query Patterns

### ORM Usage
- **使用 Ent 类型安全查询**：优先使用 Ent 生成的查询 API
- **复杂查询下探到原生 SQL**：必要时使用 `*sql.DB`
- **避免 N+1 查询**：使用 Ent 的 `Eager Loading` 预加载关联

```go
// Ent 查询示例
users, err := r.client.User.
    Query().
    Where(user.EmailEQ(email)).
    All(ctx)

// 原生 SQL 查询示例
rows, err := r.db.QueryContext(ctx, `
    SELECT u.id, u.email, COUNT(u.id) as request_count
    FROM users u
    LEFT JOIN usage_logs ul ON u.id = ul.user_id
    GROUP BY u.id
`)
```

### 事务处理
```go
err := r.client.Transaction(ctx, func(tx *ent.Tx) error {
    // 执行多个操作
    if err := tx.User.Create().Save(ctx); err != nil {
        return err
    }
    return tx.APIKey.Create().Save(ctx)
})
```

### 批量操作
```go
// 批量创建
r.client.User.Create().
    SetUsers(users...).
    Save(ctx)

// 使用 In 查询
r.client.User.
    Query().
    Where(user.IDIn(ids...)).
    All(ctx)
```

---

## Migrations

### 迁移管理
- **权威来源**：`backend/migrations/` 下的 SQL 文件
- **Schema 定义**：`backend/ent/schema/`（用于生成 Ent 代码）
- **自动执行**：启动时 `repository.InitEnt()` 自动执行待执行的迁移

### 变更流程
1. 编写 SQL 迁移文件到 `migrations/`
2. 更新 `ent/schema/` 保持一致
3. 运行 `go generate ./ent` 更新 Ent 代码
4. 启动时自动执行迁移

### 迁移文件命名
```
migrations/
├── 20240101120000_init_schema.sql
├── 20240102150000_add_user_settings.sql
└── 20240103100000_add_index_on_usage_log.sql
```

命名格式：`YYYYMMDDHHMMSS_description.sql`

---

## Naming Conventions

### 表名
- 使用蛇形命名法（snake_case）
- 使用复数形式：`users`、`api_keys`、`usage_logs`
- 关联表使用下划线连接：`user_groups`、`account_groups`

### 列名
- 使用蛇形命名法
- 主键：`id`
- 外键：`{resource}_id`（如 `user_id`、`group_id`）
- 时间戳：`created_at`、`updated_at`
- 布尔字段：使用 `is_` 或 `has_` 前缀

### 索引名
- 普通索引：`idx_{table}_{columns}`
- 唯一索引：`uidx_{table}_{columns}`
- 外键索引：`fidx_{table}_{column}`

示例：
- `idx_users_email`
- `uidx_api_keys_token`
- `idx_usage_logs_user_id_created_at`

---

## Redis Usage

### 使用场景
1. **会话存储**：JWT 黑名单、用户会话
2. **速率限制**：API 调用频率限制
3. **分布式锁**：防止并发操作冲突
4. **缓存热点数据**：仪表盘统计、公开配置
5. **消息队列**：异步任务队列

### 缓存键命名
使用冒号分隔的命名空间：`{namespace}:{id}:{key}`

```go
"auth:blacklist:{token}"
"user:cache:{user_id}"
"dashboard:stats:aggregated"
"rate_limit:user:{user_id}"
```

### TTL 设置
- 会话数据：24 小时
- 统计缓存：5-15 分钟
- 速率限制：按秒/分钟设置
- 分布式锁：秒级到分钟级

---

## Performance Optimization

### 查询优化
1. **选择必要字段**：只查询需要的列
2. **分页**：大结果集使用 `.Limit().Offset()`
3. **避免 SELECT ***：明确指定需要的字段
4. **使用 EXISTS 代替 IN**：子查询场景

### 索引策略
1. **为 WHERE 条件字段添加索引**
2. **为 JOIN 字段添加索引**
3. **为 ORDER BY 字段添加索引**
4. **复合索引遵循最左前缀原则**
5. **避免过度索引影响写入性能**

### 连接池配置
```yaml
database:
  max_open_conns: 25
  max_idle_conns: 5
  conn_max_lifetime: 300s
```

---

## Error Handling

### 数据库错误处理
```go
import (
    "entgo.io/ent/dialect/sql"
)

// 检查唯一约束冲突
if IsUniqueConstraintError(err) {
    return response.Error(c, "该资源已存在")
}

// 检查记录不存在
if ent.IsNotFound(err) {
    return response.NotFound(c, "资源不存在")
}
```

---

## Testing

### 数据库测试
1. **单元测试**：使用 mock repository 接口
2. **集成测试**：使用测试数据库
3. **测试数据清理**：每个测试后清理或使用事务回滚

---

## Common Mistakes

1. **直接修改 Ent schema 而不写迁移**：Schema 变更必须通过迁移文件
2. **忘记索引**：常用查询字段缺少索引导致性能问题
3. **N+1 查询**：循环内执行查询，应使用预加载
4. **Redis 当持久化存储**：关键数据必须存在 PostgreSQL
5. **长事务**：避免事务时间过长，影响并发性能
6. **未处理迁移幂等性**：迁移应可重复执行

---

## Examples

### Repository 示例
查看这些文件了解实际模式：
- `internal/repository/user_repository.go`
- `internal/repository/api_key_repository.go`
- `internal/repository/usage_log_repository.go`

### 迁移文件示例
查看 `migrations/` 目录下的实际 SQL 文件了解模式。

---

## Important Notes

1. **迁移是权威来源**：Schema 变更必须通过迁移文件
2. **启动时自动迁移**：`repository.InitEnt()` 在启动时执行待执行的迁移
3. **避免生产环境破坏性迁移**：先在测试环境验证
4. **Redis 不是持久化存储**：关键数据必须存储在 PostgreSQL
5. **连接管理**：使用连接池，避免长事务
