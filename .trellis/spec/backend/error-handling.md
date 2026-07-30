# Error Handling

> How errors are handled in this project.

---

## Overview

项目使用统一的错误处理机制，通过 `internal/pkg/response` 提供标准化的错误响应。错误分为业务逻辑错误、数据库错误、外部服务错误等类别。

---

## Error Types

### 标准错误分类

1. **认证/授权错误**：401、403
2. **资源不存在**：404
3. **业务逻辑错误**：400、422
4. **服务器错误**：500
5. **限流/并发错误**：429

### 自定义错误
```go
// 业务错误
var (
    ErrInvalidCredentials = errors.New("invalid credentials")
    ErrUserNotFound       = errors.New("user not found")
    ErrInsufficientBalance = errors.New("insufficient balance")
)

// 外部服务错误
type ExternalAPIError struct {
    Provider string
    Err      error
}
```

---

## Error Handling Patterns

### Handler 层错误处理
```go
func (h *UserHandler) UpdateProfile(c *gin.Context) {
    var req UpdateProfileRequest
    if err := c.ShouldBindJSON(&req); err != nil {
        response.BadRequest(c, "Invalid request body")
        return
    }

    user, err := h.userService.UpdateProfile(c.Request.Context(), userID, req)
    if err != nil {
        response.ErrorFrom(c, err) // 自动分类错误
        return
    }

    response.Success(c, user)
}
```

### Service 层错误传播
```go
func (s *UserService) UpdateProfile(ctx context.Context, id int, req UpdateProfileRequest) (*User, error) {
    // 业务逻辑校验
    if req.BalanceNotifyThreshold != nil && *req.BalanceNotifyThreshold < 0 {
        return nil, ErrInvalidThreshold
    }

    // 数据库操作
    user, err := s.repo.Update(ctx, id, req)
    if err != nil {
        return nil, fmt.Errorf("failed to update user: %w", err)
    }

    return user, nil
}
```

### Repository 层错误处理
```go
func (r *UserRepository) Update(ctx context.Context, id int, req UpdateProfileRequest) (*User, error) {
    err := r.client.User.UpdateOneID(id).
        SetNickname(req.Nickname).
        Exec(ctx)
    
    if err != nil {
        if ent.IsNotFound(err) {
            return nil, ErrUserNotFound
        }
        return nil, fmt.Errorf("database error: %w", err)
    }
    
    return user, nil
}
```

---

## API Error Responses

### 标准错误响应格式
使用 `internal/pkg/response` 包提供的函数：

```go
// 4xx 客户端错误
response.BadRequest(c, "Invalid request")          // 400
response.Unauthorized(c, "Authentication required") // 401
response.Forbidden(c, "Access denied")            // 403
response.NotFound(c, "Resource not found")         // 404
response.Conflict(c, "Resource already exists")   // 409
response.TooManyRequests(c, "Rate limit exceeded") // 429

// 5xx 服务器错误
response.Error(c, "Internal server error")         // 500
response.ServiceUnavailable(c, "Service temporarily unavailable") // 503

// 自动分类错误
response.ErrorFrom(c, err)  // 根据错误类型自动选择响应
```

### 响应结构
```json
{
    "code": 40001,
    "message": "Invalid request parameter",
    "data": null
}
```

### 错误码规范
- `4xxxx`：客户端错误
- `5xxxx`：服务器错误
- `6xxxx`：业务逻辑错误

---

## Error Logging

### 日志级别
```go
import "github.com/Wei-Shaw/sub2api/internal/pkg/logger"

// Debug：详细调试信息
logger.Debug("Processing request", "user_id", userID)

// Info：一般信息
logger.Info("User created", "user_id", userID)

// Warn：警告但不影响运行
logger.Warn("Rate limit exceeded", "user_id", userID)

// Error：业务错误
logger.Error("Failed to update user", "error", err, "user_id", userID)
```

### 错误上下文
日志中包含足够的上下文信息：
```go
logger.Error("External API call failed",
    "provider", "anthropic",
    "endpoint", "/v1/messages",
    "error", err,
    "account_id", accountID,
)
```

---

## Special Error Cases

### 外部服务错误
```go
// 网关转发中的错误处理
if resp.StatusCode >= 500 {
    // 上游服务器错误，尝试其他账号
    return h.handleErrorWithFallback(c, err)
}

if resp.StatusCode == 429 {
    // 速率限制，记录并退避
    return h.handleRateLimit(c, resp)
}
```

### 数据库错误
```go
if IsUniqueConstraintError(err) {
    return response.Conflict(c, "Resource already exists")
}

if ent.IsNotFound(err) {
    return response.NotFound(c, "Resource not found")
}

return response.Error(c, "Database operation failed")
```

### 并发错误
```go
if errors.Is(err, ErrConcurrencyLimit) {
    return response.TooManyRequests(c, "Concurrency limit exceeded")
}
```

---

## Common Mistakes

1. **吞掉错误**：`_ = someFunc()` 应始终检查错误
2. **不记录错误上下文**：日志中缺少关键信息（user_id、account_id 等）
3. **错误码混乱**：未遵循统一的错误码规范
4. **过度使用 panic**：应返回 error 而非 panic
5. **不区分错误类型**：客户端错误和服务器错误未分开处理

---

## Examples

### Handler 错误处理示例
查看这些文件：
- `internal/handler/user_handler.go`
- `internal/handler/auth_handler.go`
- `internal/handler/gateway_helper.go`

### Response 包使用
查看 `internal/pkg/response/` 了解完整的响应函数。

---

## Important Notes

1. **统一使用 response 包**：不要直接使用 `c.JSON()` 返回错误
2. **错误要包含上下文**：日志中记录足够的信息用于排查
3. **区分客户端/服务端错误**：4xx vs 5xx
4. **外部服务错误要有降级**：网关转发中要有账号切换机制
5. **敏感信息不记录**：不要在日志/错误消息中暴露 token、密码等
