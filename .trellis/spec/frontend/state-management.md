# State Management

> How state is managed in this project.

---

## Overview

项目使用 Pinia 进行全局状态管理，遵循以下原则：
- **本地状态优先**：组件内部状态优先使用 `ref`/`reactive`
- **全局状态谨慎**：只有跨组件共享的状态才放入 Pinia
- **服务器状态分离**：API 数据使用专门的组合式函数或 Store

**状态管理技术栈**：
- Pinia（全局状态）
- Vue Composition API（本地状态）
- 组合式函数（可复用状态逻辑）

---

## State Categories

### 1. 本地状态（Local State）
**用途**：单个组件内部的状态

```vue
<script setup lang="ts">
// ✅ 本地状态使用 ref/reactive
const count = ref(0)
const form = reactive({
  username: '',
  email: ''
})

const loading = ref(false)
const error = ref<Error | null>(null)
</script>
```

**适用于**：
- 表单输入状态
- UI 交互状态（展开/折叠、选中/未选中）
- 组件内部的临时状态
- 不需要跨组件共享的数据

### 2. 全局状态（Global State）
**用途**：跨多个组件共享的状态

```typescript
// stores/counter.ts
export const useCounterStore = defineStore('counter', () => {
  const count = ref(0)

  const increment = () => {
    count.value++
  }

  return { count, increment }
})
```

**适用于**：
- 用户认证状态
- 应用配置和设置
- 主题、语言等 UI 状态
- 需要持久化的状态

### 3. 服务器状态（Server State）
**用途**：从 API 获取的数据

```typescript
// composables/useUserData.ts
export function useUserData(userId: Ref<number>) {
  const data = ref<User | null>(null)
  const loading = ref(false)
  const error = ref<Error | null>(null)

  const fetch = async () => {
    loading.value = true
    try {
      data.value = await userAPI.getUser(userId.value)
    } catch (e) {
      error.value = e as Error
    } finally {
      loading.value = false
    }
  }

  return { data, loading, error, refresh: fetch }
}
```

**适用于**：
- API 返回的数据
- 需要缓存和重新验证的数据
- CRUD 操作的数据

### 4. URL 状态
**用途**：存储在 URL 中的状态（查询参数、路由参数）

```typescript
const route = useRoute()
const userId = computed(() => route.params.id)
const searchQuery = computed(() => route.query.q)
```

**适用于**：
- 搜索条件
- 分页状态
- 可分享的状态

---

## When to Use Global State

### 使用全局状态的标准

1. **多个组件需要访问相同数据**
```typescript
// ✅ 需要全局状态
// 用户信息在多个组件中使用（导航栏、用户设置、权限检查）
export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const token = ref<string | null>(null)
  // ...
})
```

2. **状态需要持久化**
```typescript
// ✅ 需要全局状态（本地存储）
export const useSettingsStore = defineStore('settings', () => {
  const theme = ref('light')
  const language = ref('en')

  // 持久化到 localStorage
  watch(theme, (value) => {
    localStorage.setItem('theme', value)
  }, { immediate: true })

  return { theme, language }
})
```

3. **复杂的状态逻辑**
```typescript
// ✅ 需要全局状态（复杂的认证流程）
export const useAuthStore = defineStore('auth', () => {
  // 登录、登出、token 刷新、2FA 等复杂逻辑
  // ...
})
```

### 不使用全局状态的情况

```vue
<!-- ❌ 不需要全局状态：单个组件的表单状态 -->
<script setup lang="ts">
// 组件内部表单，不需要全局状态
const form = reactive({
  username: '',
  email: ''
})
</script>

<!-- ❌ 不需要全局状态：UI 交互状态 -->
<script setup lang="ts">
// 组件内部的 UI 状态，不需要全局状态
const isOpen = ref(false)
const isSelected = ref(false)
</script>
```

---

## Store Organization

### Store 分层

```typescript
// stores/auth.ts - 认证状态
export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)
  const token = ref<string | null>(null)
  const isAuthenticated = computed(() => !!token.value)

  return { user, token, isAuthenticated }
})

// stores/app.ts - 应用配置和 UI 状态
export const useAppStore = defineStore('app', () => {
  const sidebarOpen = ref(true)
  const theme = ref('light')
  const language = ref('en')

  return { sidebarOpen, theme, language }
})

// stores/adminSettings.ts - 管理后台设置
export const useAdminSettingsStore = defineStore('adminSettings', () => {
  const settings = ref<AdminSettings | null>(null)

  return { settings }
})
```

### Store 命名规范

```typescript
// ✅ 正确：使用 use 前缀 + Store 后缀
const authStore = useAuthStore()
const appStore = useAppStore()

// Store 文件命名
// stores/auth.ts -> useAuthStore
// stores/app.ts -> useAppStore
```

---

## Server State Management

### API 数据获取模式

```typescript
// 方式 1：组合式函数（推荐）
const { data, loading, error } = useUserData(ref(userId))

// 方式 2：Store Action
const userStore = useUserStore()
await userStore.fetchUser(userId)
```

### 数据缓存和同步

```typescript
// stores/subscriptions.ts
export const useSubscriptionsStore = defineStore('subscriptions', () => {
  const subscriptions = ref<Subscription[]>([])
  const lastFetched = ref<Date | null>(null)
  const CACHE_TTL = 5 * 60 * 1000 // 5 分钟

  const isCacheValid = computed(() => {
    if (!lastFetched.value) return false
    return Date.now() - lastFetched.value.getTime() < CACHE_TTL
  })

  const fetchSubscriptions = async (forceRefresh = false) => {
    if (!forceRefresh && isCacheValid.value) {
      return // 使用缓存
    }

    subscriptions.value = await subscriptionAPI.getSubscriptions()
    lastFetched.value = new Date()
  }

  // 定期刷新
  let timer: ReturnType<typeof setInterval> | null = null
  const startPolling = () => {
    timer = setInterval(() => {
      fetchSubscriptions(true)
    }, 60000) // 每分钟刷新
  }

  const stopPolling = () => {
    if (timer) clearInterval(timer)
  }

  return {
    subscriptions,
    fetchSubscriptions,
    startPolling,
    stopPolling
  }
})
```

### Token 刷新和认证

```typescript
// stores/auth.ts
export const useAuthStore = defineStore('auth', () => {
  const token = ref<string | null>(null)
  const refreshToken = ref<string | null>(null)
  const user = ref<User | null>(null)

  // 从 localStorage 恢复
  const checkAuth = () => {
    const savedToken = localStorage.getItem('auth_token')
    const savedUser = localStorage.getItem('auth_user')
    if (savedToken && savedUser) {
      token.value = savedToken
      user.value = JSON.parse(savedUser)
    }
  }

  // 持久化到 localStorage
  watch(token, (value) => {
    if (value) {
      localStorage.setItem('auth_token', value)
    } else {
      localStorage.removeItem('auth_token')
    }
  })

  // Token 刷新
  const refreshAccessToken = async () => {
    if (!refreshToken.value) return

    try {
      const response = await authAPI.refreshToken()
      token.value = response.access_token
      refreshToken.value = response.refresh_token
    } catch (e) {
      // Token 刷新失败，清除认证状态
      clearAuth()
      throw e
    }
  }

  // 清除认证状态
  const clearAuth = () => {
    token.value = null
    refreshToken.value = null
    user.value = null
    localStorage.removeItem('auth_token')
    localStorage.removeItem('auth_user')
    localStorage.removeItem('refresh_token')
  }

  return {
    token,
    user,
    checkAuth,
    refreshAccessToken,
    clearAuth
  }
})
```

---

## Derived State Patterns

### 计算属性

```typescript
// ✅ 使用计算属性创建派生状态
const fullName = computed(() => {
  return `${user.value.firstName} ${user.value.lastName}`
})

const isAuthenticated = computed(() => {
  return !!token.value && !!user.value
})

const activeSubscriptions = computed(() => {
  return subscriptions.value.filter(sub => sub.active)
})
```

### Getter 模式

```typescript
export const useAuthStore = defineStore('auth', () => {
  const user = ref<User | null>(null)

  // 计算属性作为 getter
  const isAdmin = computed(() => {
    return user.value?.role === 'admin'
  })

  const hasPermission = (permission: string) => {
    return user.value?.permissions.includes(permission) || false
  }

  return {
    user,
    isAdmin,
    hasPermission
  }
})
```

---

## Common Mistakes

### 1. 过度使用全局状态

```vue
<!-- ❌ 错误：不需要全局状态 -->
<script setup lang="ts">
// 仅仅在单个组件中使用，不需要全局状态
const formStore = useFormStore()
</script>

<!-- ✅ 正确：使用本地状态 -->
<script setup lang="ts">
const form = reactive({
  username: '',
  email: ''
})
</script>
```

### 2. 在 Store 中直接修改状态

```typescript
// ❌ 错误：直接修改其他 Store 的状态
export const useUserStore = defineStore('user', () => {
  const updateUser = () => {
    const authStore = useAuthStore()
    authStore.user = newUser // ❌ 不要直接修改
  }
})

// ✅ 正确：通过 action 修改
export const useAuthStore = defineStore('auth', () => {
  const setUser = (newUser: User) => {
    user.value = newUser
  }

  return { user, setUser }
})
```

### 3. 忽略服务器状态的缓存

```typescript
// ❌ 错误：每次都重新请求
const loadUsers = async () => {
  users.value = await userAPI.getUsers() // 没有缓存
}

// ✅ 正确：使用缓存和验证
const loadUsers = async (forceRefresh = false) => {
  if (!forceRefresh && isCacheValid()) {
    return // 使用缓存
  }
  users.value = await userAPI.getUsers()
  lastFetched.value = new Date()
}
```

### 4. 不清理副作用

```typescript
// ❌ 错误：不清理定时器
export const useSubscriptionStore = defineStore('subscriptions', () => {
  const startPolling = () => {
    setInterval(() => {
      // 轮询订阅
    }, 60000) // 永远不会被清理
  }

  return { startPolling }
})

// ✅ 正确：清理定时器
export const useSubscriptionStore = defineStore('subscriptions', () => {
  let timer: ReturnType<typeof setInterval> | null = null

  const startPolling = () => {
    timer = setInterval(() => {
      // 轮询订阅
    }, 60000)
  }

  const stopPolling = () => {
    if (timer) clearInterval(timer)
  }

  // 组件卸载时清理
  onUnmounted(() => {
    stopPolling()
  })

  return { startPolling, stopPolling }
})
```

---

## Best Practices

### 1. 选择合适的状态类型

```typescript
// ✅ 本地状态：组件内部使用
const isOpen = ref(false)

// ✅ 全局状态：跨组件共享
const appStore = useAppStore()

// ✅ 服务器状态：API 数据
const { data } = useUserData(ref(userId))
```

### 2. 状态分层清晰

```typescript
// 本地状态 -> 组件状态 -> 全局状态 -> 服务器状态
// 越右边的状态越"重"，使用应该越谨慎
```

### 3. 使用 TypeScript 类型

```typescript
interface User {
  id: number
  name: string
  email: string
}

export const useUserStore = defineStore('user', () => {
  const user = ref<User | null>(null) // ✅ 类型定义
  return { user }
})
```

---

## Examples

### Store 示例
查看这些文件了解实际模式：
- `src/stores/auth.ts` - 认证状态管理
- `src/stores/app.ts` - 应用配置管理
- `src/stores/subscriptions.ts` - 订阅数据管理

### 组合式函数示例
- `src/composables/` - 可复用的状态逻辑

---

## Important Notes

1. **本地状态优先**：能用本地状态就不用全局状态
2. **服务器状态分离**：API 数据使用专门的组合式函数
3. **状态分层**：清楚区分本地、全局、服务器状态
4. **类型安全**：使用 TypeScript 定义状态类型
5. **清理副作用**：定时器、订阅等需要在组件卸载时清理
6. **缓存策略**：服务器状态要有合理的缓存和刷新策略
