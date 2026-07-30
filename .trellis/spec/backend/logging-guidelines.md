# Logging Guidelines

> How logging is done in this project.

---

## Overview

项目使用 `internal/pkg/logger` 提供的结构化日志功能。日志支持多个级别，采用结构化格式，包含足够的上下文信息用于问题排查。

**日志库**：基于结构化日志的自定义 logger (`internal/pkg/logger`)

---

## Log Levels

### Debug
**用途**：详细的调试信息，通常仅在开发环境使用

```go
logger.Debug("Processing request details",
    "user_id", userID,
    "request_body", string(body),
)
```

### Info
**用途**：一般 informational 事件，表示正常的业务流程

```go
logger.Info("User created successfully",
    "user_id", user.ID,
    "email", user.Email,
)
```

### Warn
**用途**：警告但不影响系统运行的情况

```go
logger.Warn("Rate limit approaching",
    "user_id", userID,
    "requests", requestCount,
    "limit", limit,
)
```

### Error
**用途**：错误事件，需要关注但不一定需要立即处理

```go
logger.Error("Failed to call external API",
    "provider", "anthropic",
    "error", err,
    "account_id", accountID,
)
```

---

## Structured Logging

### 日志格式
日志采用结构化格式，包含键值对：

```go
logger.Error("Gateway request failed",
    "provider", "claude",
    "endpoint", "/v1/messages",
    "status_code", resp.StatusCode,
    "error", err,
    "user_id", userID,
    "api_key_id", keyID,
    "account_id", accountID,
)
```

### 推荐的日志字段
**请求相关**：
- `user_id`：用户 ID
- `api_key_id`：API Key ID
- `account_id`：上游账号 ID
- `group_id`：分组 ID
- `request_id`：请求追踪 ID

**操作相关**：
- `operation`：操作名称（如 "create_user"、"update_profile"）
- `duration_ms`：操作耗时（毫秒）
- `status`：操作状态（"success"、"failed"）

**错误相关**：
- `error`：错误对象
- `error_type`：错误类型
- `retry_count`：重试次数

---

## What to Log

### 业务事件
```go
// 用户认证
logger.Info("User logged in", "user_id", userID, "method", "password")
logger.Info("User logged out", "user_id", userID)

// 资源变更
logger.Info("API key created", "key_id", keyID, "user_id", userID)
logger.Info("Account updated", "account_id", accountID, "changes", fieldNames)

// 网关请求
logger.Info("Gateway request succeeded",
    "provider", "claude",
    "model", "claude-3-5-sonnet-20241022",
    "tokens", tokenCount,
    "duration_ms", duration,
)
```

### 系统事件
```go
// 服务启动
logger.Info("Server starting",
    "version", version,
    "port", port,
    "mode", runMode,
)

// 后台任务
logger.Info("Usage aggregation completed",
    "records_processed", count,
    "duration_ms", duration,
)
```

### 性能指标
```go
logger.Info("Database query completed",
    "query", "get_user_with_usage",
    "duration_ms", duration,
    "rows", rowCount,
)
```

---

## What NOT to Log

### 敏感信息（严格禁止）
```go
// ❌ 不要记录这些信息
logger.Debug("User password", "password", user.Password)  // 密码
logger.Info("API request", "authorization", header)       // Token
logger.Debug("Credit card", "card", cardNumber)           // 支付信息
logger.Info("Session", "session", sessionData)            // 会话数据
```

### 个人信息（PII）
```go
// ⚠️ 谨慎记录个人信息
// 如果必须记录，使用脱敏处理
logger.Info("User updated",
    "user_id", userID,
    "email", maskEmail(user.Email),  // user@example.com -> u***@example.com
)
```

### 大数据
```go
// ❌ 避免记录大型数据结构
logger.Debug("Request body", "body", string(megabytesOfData))

// ✅ 记录摘要信息
logger.Debug("Request received",
    "content_length", contentLength,
    "content_type", contentType,
)
```

---

## Logging in Different Layers

### Handler 层
```go
func (h *UserHandler) UpdateProfile(c *gin.Context) {
    startTime := time.Now()
    
    // 记录请求开始
    logger.Debug("Processing update profile request",
        "user_id", userID,
        "remote_addr", c.ClientIP(),
    )
    
    // ... 处理逻辑 ...
    
    // 记录处理完成
    logger.Info("Profile updated successfully",
        "user_id", userID,
        "duration_ms", time.Since(startTime).Milliseconds(),
    )
}
```

### Service 层
```go
func (s *UserService) CreateUser(ctx context.Context, req CreateUserRequest) (*User, error) {
    logger.Info("Creating user",
        "email", req.Email,
        "username", req.Username,
    )
    
    user, err := s.repo.Create(ctx, req)
    if err != nil {
        logger.Error("Failed to create user",
            "error", err,
            "email", req.Email,
        )
        return nil, err
    }
    
    logger.Info("User created successfully",
        "user_id", user.ID,
        "email", user.Email,
    )
    return user, nil
}
```

### Gateway 层
```go
// 网关请求日志
logger.Info("Gateway request",
    "user_id", userID,
    "api_key_id", keyID,
    "provider", "claude",
    "model", req.Model,
    "request_id", requestID,
)

// 网关响应日志
logger.Info("Gateway response",
    "request_id", requestID,
    "status_code", statusCode,
    "tokens_input", inputTokens,
    "tokens_output", outputTokens,
    "duration_ms", duration.Milliseconds(),
)
```

---

## Request Logging

### Access Logging
HTTP 请求由中间件自动记录，包含：
- 请求方法和路径
- 状态码
- 响应时间
- 客户端 IP
- 用户 ID（如果已认证）

### 操作审计
重要操作需要审计日志：
```go
logger.Info("Admin action: user banned",
    "admin_id", adminID,
    "target_user_id", targetUserID,
    "reason", reason,
    "timestamp", time.Now(),
)
```

---

## Error Logging Patterns

### External API Errors
```go
logger.Error("External API request failed",
    "provider", "anthropic",
    "endpoint", "/v1/messages",
    "status_code", resp.StatusCode,
    "error", err,
    "account_id", accountID,
    "retry_count", retryCount,
)
```

### Database Errors
```go
logger.Error("Database operation failed",
    "operation", "update_user",
    "table", "users",
    "user_id", userID,
    "error", err,
)
```

### Concurrency Errors
```go
logger.Warn("Concurrency limit reached",
    "user_id", userID,
    "current_concurrent", current,
    "max_concurrent", max,
)
```

---

## Performance Logging

### Slow Query Detection
```go
if duration > threshold {
    logger.Warn("Slow database query",
        "query", queryName,
        "duration_ms", duration.Milliseconds(),
        "threshold_ms", threshold,
    )
}
```

### API Performance
```go
logger.Info("API performance metrics",
    "endpoint", "/api/v1/users/me",
    "avg_duration_ms", avgDuration,
    "p95_duration_ms", p95Duration,
    "p99_duration_ms", p99Duration,
)
```

---

## Common Mistakes

1. **过度日志**：每个循环都记录日志，产生大量日志
2. **日志级别混乱**：错误使用 Info 记录应该用 Debug 的信息
3. **缺少上下文**：日志中缺少关键信息（user_id、account_id 等）
4. **记录敏感信息**：记录密码、token 等敏感数据
5. **不使用结构化**：使用字符串拼接而非键值对
6. **日志在不同级别不一致**：相同的错误在不同地方用不同级别记录

---

## Examples

### 查看实际日志使用
查看这些文件了解实际模式：
- `internal/handler/logging.go`
- `internal/handler/ops_error_logger.go`
- `internal/service/dashboard_service.go`

---

## Important Notes

1. **生产环境使用 Info 及以上级别**：Debug 日志应在生产环境关闭
2. **错误日志要包含上下文**：至少包含 user_id/account_id/request_id
3. **敏感信息绝对不记录**：密码、token、完整信用卡号等
4. **结构化日志优于字符串拼接**：使用键值对格式
5. **合理使用日志级别**：避免所有日志都使用 Error 级别
6. **考虑性能影响**：高频操作避免详细日志
