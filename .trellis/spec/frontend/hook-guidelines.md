# Hook Guidelines

> How composables (hooks) are used in this project.

---

## Overview

在 Vue 3 项目中，组合式函数（Composables）实现了类似 React Hooks 的逻辑复用模式。所有可复用的有状态逻辑都应该提取到组合式函数中。

**注意**：本文档中的 "Hook" 指的是 Vue 的组合式函数（Composables），使用 `use` 前缀命名。

---

## Custom Hook Patterns

### 标准组合式函数结构

```typescript
// composables/useUserData.ts
import { ref, computed, onMounted, watch } from 'vue'
import type { Ref } from 'vue'

export interface UseUserDataOptions {
  immediate?: boolean
  pollingInterval?: number
}

export function useUserData(
  userId: Ref<number> | number,
  options: UseUserDataOptions = {}
) {
  // 1. 状态定义
  const data = ref<User | null>(null)
  const loading = ref(false)
  const error = ref<Error | null>(null)

  // 2. 计算属性
  const isAuthenticated = computed(() => !!data.value)

  // 3. 方法
  const fetchUserData = async () => {
    const id = typeof userId === 'number' ? userId : userId.value
    if (!id) return

    loading.value = true
    error.value = null

    try {
      data.value = await userAPI.getUser(id)
    } catch (e) {
      error.value = e as Error
    } finally {
      loading.value = false
    }
  }

  const refresh = () => {
    return fetchUserData()
  }

  // 4. 副作用
  let timer: ReturnType<typeof setInterval> | null = null

  const startPolling = () => {
    if (timer) clearInterval(timer)
    timer = setInterval(fetchUserData, options.pollingInterval || 30000)
  }

  const stopPolling = () => {
    if (timer) {
      clearInterval(timer)
      timer = null
    }
  }

  // 5. 响应式监听
  if (typeof userId === 'object') {
    watch(userId, () => {
      fetchUserData()
    }, { immediate: options.immediate })
  }

  // 6. 生命周期
  onMounted(() => {
    if (options.immediate) {
      fetchUserData()
    }
  })

  onUnmounted(() => {
    stopPolling()
  })

  // 7. 返回公共接口
  return {
    data,
    loading,
    error,
    isAuthenticated,
    refresh,
    startPolling,
    stopPolling
  }
}
```

---

## Data Fetching Patterns

### 基础数据获取

```typescript
// composables/useAPI.ts
export function useAPI<T>(
  fetcher: () => Promise<T>,
  options = { immediate: true }
) {
  const data = ref<T | null>(null)
  const loading = ref(false)
  const error = ref<Error | null>(null)

  const fetch = async () => {
    loading.value = true
    error.value = null

    try {
      data.value = await fetcher()
    } catch (e) {
      error.value = e as Error
    } finally {
      loading.value = false
    }
  }

  if (options.immediate) {
    fetch()
  }

  return { data, loading, error, refresh: fetch }
}
```

### 在组件中使用

```vue
<script setup lang="ts">
import { useAPI } from '@/composables/useAPI'

const { data, loading, error } = useAPI(() =>
  userAPI.getCurrentUser()
)
</script>

<template>
  <div v-if="loading">Loading...</div>
  <div v-else-if="error">Error: {{ error.message }}</div>
  <div v-else>{{ data?.name }}</div>
</template>
```

### 分页数据获取

```typescript
export function usePaginatedData<T>(
  fetcher: (page: number, pageSize: number) => Promise<{ data: T[]; total: number }>
) {
  const page = ref(1)
  const pageSize = ref(10)
  const { data, loading, error, refresh } = useAPI(async () => {
    return fetcher(page.value, pageSize.value)
  })

  const nextPage = () => {
    page.value++
    refresh()
  }

  const prevPage = () => {
    if (page.value > 1) {
      page.value--
      refresh()
    }
  }

  return {
    data,
    loading,
    error,
    page,
    pageSize,
    nextPage,
    prevPage,
    refresh
  }
}
```

---

## Naming Conventions

### 组合式函数命名

```typescript
// ✅ 正确：使用 use 前缀
useUserData()
useAPI()
useRoutePrefetch()
useAuthStore()

// ❌ 错误：不使用 use 前缀
getUserData()
fetchAPI()
getRoutePrefetch()
```

### 参数命名

```typescript
// 选项参数使用 options
useUserData(userId, options)
useAPI(fetcher, options)

// 响应式参数使用 Ref
useUserData(ref(userId))
useAPI(ref(fetcher))

// 布尔选项使用 is/has/should 前缀
useUserData(userId, { immediate: true, shouldCache: false })
```

### 返回值命名

```typescript
// 状态使用简单名词
const { data, loading, error } = useAPI()

// 方法使用动词
const { refresh, startPolling, stopPolling } = useUserData()

// 计算属性使用形容词或 is 前缀
const { isAuthenticated, hasPermission } = useAuth()
```

---

## Built-in Composables

### Vue 核心组合式函数

```typescript
// 响应式状态
const count = ref(0)
const state = reactive({ count: 0 })

// 计算属性
const doubleCount = computed(() => count.value * 2)

// 生命周期
onMounted(() => {})
onUnmounted(() => {})

// 监听器
watch(source, callback)
watchEffect(() => {})

// 组件
const router = useRouter()
const route = useRoute()

// 状态管理
const authStore = useAuthStore()
```

### VueUse 组合式函数

```typescript
// 常用 VueUse 函数
import { useLocalStorage, useMouse, useWindowSize } from '@vueuse/core'

const storage = useLocalStorage('key', defaultValue)
const { x, y } = useMouse()
const { width, height } = useWindowSize()
```

---

## State Sharing Patterns

### 全局状态组合式函数

```typescript
// composables/useGlobalState.ts
import { ref } from 'vue'

const globalState = ref('default')

export function useGlobalState() {
  const setState = (value: string) => {
    globalState.value = value
  }

  return {
    state: globalState,
    setState
  }
}

// 多个组件共享同一个状态
// Component A
const { state } = useGlobalState()

// Component B
const { state } = useGlobalState() // 同一个引用
```

### 本地状态组合式函数

```typescript
// 每次调用创建新的状态实例
export function useCounter(initialValue = 0) {
  const count = ref(initialValue)

  const increment = () => {
    count.value++
  }

  const decrement = () => {
    count.value--
  }

  return {
    count,
    increment,
    decrement
  }
}
```

---

## Common Patterns

### 错误处理

```typescript
export function useAPI() {
  const error = ref<Error | null>(null)

  const execute = async () => {
    try {
      // API 调用
    } catch (e) {
      error.value = e as Error
      // 统一错误处理
      console.error('API call failed:', error.value)
      throw error.value // 重新抛出让调用者处理
    }
  }

  return { error, execute }
}
```

### 加载状态

```typescript
export function useForm() {
  const loading = ref(false)
  const errors = ref<Record<string, string>>({})

  const submit = async (data: FormData) => {
    loading.value = true
    errors.value = {}

    try {
      await submitForm(data)
    } catch (e) {
      errors.value = parseErrors(e)
    } finally {
      loading.value = false
    }
  }

  return { loading, errors, submit }
}
```

### 资源清理

```typescript
export function useWebSocket(url: string) {
  const ws = ref<WebSocket | null>(null)
  const data = ref<any>(null)

  const connect = () => {
    ws.value = new WebSocket(url)

    ws.value.onmessage = (event) => {
      data.value = JSON.parse(event.data)
    }
  }

  const disconnect = () => {
    ws.value?.close()
  }

  onUnmounted(() => {
    disconnect()
  })

  return { data, connect, disconnect }
}
```

---

## Common Mistakes

### 1. 在组合式函数中使用 `this`

```typescript
// ❌ 错误：组合式函数中不使用 this
export function useMyHook() {
  const value = this.someValue // ❌
}

// ✅ 正确：使用参数和返回值
export function useMyHook(initialValue: number) {
  const value = ref(initialValue) // ✅
}
```

### 2. 解构响应式对象

```typescript
// ❌ 错误：解构会失去响应性
const { count } = reactive({ count: 0 })

// ✅ 正确：使用 toRefs
const { count } = toRefs(reactive({ count: 0 }))

// ✅ 或直接使用 ref
const count = ref(0)
```

### 3. 不清理副作用

```typescript
// ❌ 错误：不清理定时器
export function usePolling() {
  setInterval(() => {
    // poll
  }, 1000)
  // 定时器永远不会被清理
}

// ✅ 正确：清理副作用
export function usePolling() {
  let timer: ReturnType<typeof setInterval> | null = null

  const start = () => {
    timer = setInterval(() => {
      // poll
    }, 1000)
  }

  const stop = () => {
    if (timer) clearInterval(timer)
  }

  onUnmounted(() => {
    stop()
  })

  return { start, stop }
}
```

### 4. 过早调用组合式函数

```typescript
// ❌ 错误：条件中调用组合式函数
if (someCondition) {
  const data = useData() // ❌
}

// ✅ 正确：组合式函数应该在 setup 顶层调用
const data = useData()
if (someCondition) {
  // 使用 data
}
```

---

## Examples

### 查看实际组合式函数
- `src/composables/` - 项目中的组合式函数
- `src/stores/` - 使用组合式 API 的 Pinia stores

### 实用组合式函数模式

```typescript
// 表单处理
export function useForm<T>(initialValues: T) {
  const form = reactive<T>({ ...initialValues })
  const errors = ref<Record<string, string>>({})

  const reset = () => {
    Object.assign(form, initialValues)
    errors.value = {}
  }

  const validate = () => {
    // 验证逻辑
  }

  return { form, errors, reset, validate }
}

// 防抖/节流
export function useDebounce<T>(value: Ref<T>, delay: number) {
  const debouncedValue = ref<T>(value.value)

  let timer: ReturnType<typeof setTimeout> | null = null

  watch(value, (newValue) => {
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      debouncedValue.value = newValue
    }, delay)
  })

  return debouncedValue
}
```

---

## Important Notes

1. **命名规范**：所有组合式函数使用 `use` 前缀
2. **顶层调用**：组合式函数必须在 `setup` 的顶层同步调用
3. **响应式**：正确使用 `ref` 和 `reactive`，避免解构失去响应性
4. **清理副作用**：在 `onUnmounted` 中清理定时器、事件监听器等
5. **参数类型**：使用 TypeScript 定义参数和返回值类型
6. **错误处理**：统一错误处理模式，提供错误状态给调用者
