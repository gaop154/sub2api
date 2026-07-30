# Quality Guidelines

> Code quality standards for frontend development.

---

## Overview

项目通过 ESLint、TypeScript 类型检查、Vitest 测试和代码审查流程确保前端代码质量。所有代码必须通过 `pnpm lint`、`pnpm typecheck` 和 `pnpm test` 才能合并。

**质量检查工具**：
- ESLint（代码风格和质量）
- TypeScript（类型安全）
- Vitest（单元测试和集成测试）
- Vue Test Utils（组件测试）

---

## Forbidden Patterns

### 禁止的模式

1. **直接修改 props**
```vue
<!-- ❌ 禁止：直接修改 props -->
<script setup lang="ts">
const props = defineProps<{ userId: number }>()
props.userId = 123 // ❌ 不要这样做
</script>

<!-- ✅ 正确：通过 emit 通知父组件 -->
<script setup lang="ts">
interface Emits {
  (e: 'update:user-id', value: number): void
}
const props = defineProps<{ userId: number }>()
const emit = defineEmits<Emits>()

const updateUserId = () => {
  emit('update:user-id', 123)
}
</script>
```

2. **在模板中使用复杂表达式**
```vue
<!-- ❌ 禁止：复杂的模板表达式 -->
<template>
  <div>{{ formatDate(parseDate(user.birthDate)) }}</div>
</template>

<!-- ✅ 正确：使用计算属性 -->
<script setup lang="ts">
const formattedBirthDate = computed(() => {
  return formatDate(parseDate(user.value.birthDate))
})
</script>

<template>
  <div>{{ formattedBirthDate }}</div>
</template>
```

3. **使用 Options API**
```vue
<!-- ❌ 禁止：使用 Options API -->
<script>
export default {
  data() {
    return { count: 0 }
  },
  methods: {
    increment() {
      this.count++
    }
  }
}
</script>

<!-- ✅ 正确：使用 Composition API -->
<script setup lang="ts">
const count = ref(0)
const increment = () => {
  count.value++
}
</script>
```

4. **直接操作 DOM**
```vue
<!-- ❌ 禁止：直接操作 DOM -->
<script setup lang="ts">
onMounted(() => {
  document.getElementById('app').style.color = 'red'
})
</script>

<!-- ✅ 正确：使用 ref 和响应式状态 -->
<script setup lang="ts">
const appRef = ref<HTMLElement>()

onMounted(() => {
  if (appRef.value) {
    appRef.value.style.color = 'red'
  }
})
</script>

<template>
  <div ref="appRef">Content</div>
</template>
```

5. **使用 `any` 类型**
```typescript
// ❌ 禁止：使用 any
function processData(data: any) {
  return data.value
}

// ✅ 正确：使用具体类型
interface Data {
  value: string
}

function processData(data: Data) {
  return data.value
}
```

6. **不清理副作用**
```vue
<!-- ❌ 禁止：不清理副作用 -->
<script setup lang="ts>
onMounted(() => {
  setInterval(() => {
    // 轮询逻辑
  }, 1000)
  // 定时器永远不会被清理
})
</script>

<!-- ✅ 正确：清理副作用 -->
<script setup lang="ts>
let timer: ReturnType<typeof setInterval> | null = null

onMounted(() => {
  timer = setInterval(() => {
    // 轮询逻辑
  }, 1000)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
})
</script>
```

---

## Required Patterns

### 必须使用的模式

1. **Composition API**
```vue
<!-- ✅ 所有组件使用 Composition API -->
<script setup lang="ts">
// 组合式 API 代码
</script>
```

2. **TypeScript 类型定义**
```vue
<script setup lang="ts">
// ✅ Props 必须定义类型
interface Props {
  userId: number
  title?: string
}

const props = withDefaults(defineProps<Props>(), {
  title: 'Default Title'
})

// ✅ Emits 必须定义类型
interface Emits {
  (e: 'update', value: string): void
}

const emit = defineEmits<Emits>()
</script>
```

3. **使用组合式函数**
```typescript
// ✅ 可复用逻辑提取到 composables
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

4. **使用计算属性**
```vue
<script setup lang="ts">
// ✅ 派生状态使用计算属性
const fullName = computed(() => {
  return `${firstName.value} ${lastName.value}`
})

// ❌ 不要使用方法实现计算属性
const fullName = () => {
  return `${firstName.value} ${lastName.value}`
}
</script>
```

5. **响应式状态使用 ref/reactive**
```vue
<script setup lang="ts">
// ✅ 使用 ref/reactive
const count = ref(0)
const user = reactive({ name: 'John' })

// ❌ 不要使用普通变量
let count = 0
let user = { name: 'John' }
</script>
```

---

## Testing Requirements

### 测试分层

1. **单元测试**：测试组合式函数和工具函数
   - 位置：`src/**/__tests__/*.spec.ts`
   - 使用 Vitest
   - 测试纯函数和组合式函数

2. **组件测试**：测试 Vue 组件
   - 位置：`src/components/**/__tests__/*.spec.ts`
   - 使用 Vue Test Utils + Vitest
   - 测试组件交互和渲染

3. **集成测试**：测试跨模块功能
   - 位置：`src/__tests__/integration/*.spec.ts`
   - 测试完整功能流程

### 测试命令

```bash
# 运行所有测试
pnpm test

# 监听模式
pnpm test --watch

# 覆盖率报告
pnpm test:coverage

# 运行单个测试文件
pnpm vitest run src/api/__tests__/client.spec.ts

# 按测试名运行
pnpm vitest run -t "token refresh"
```

### 测试示例

```typescript
// composables/__tests__/useUserData.spec.ts
import { describe, it, expect, vi } from 'vitest'
import { useUserData } from '../useUserData'
import { ref } from 'vue'

describe('useUserData', () => {
  it('should fetch user data', async () => {
    const userId = ref(1)
    const { data, loading, error } = useUserData(userId)

    // 等待异步操作完成
    await nextTick()

    expect(loading.value).toBe(false)
    expect(data.value).toEqual({ id: 1, name: 'John' })
    expect(error.value).toBeNull()
  })

  it('should handle errors', async () => {
    const userId = ref(999)
    const { data, loading, error } = useUserData(userId)

    await nextTick()

    expect(loading.value).toBe(false)
    expect(data.value).toBeNull()
    expect(error.value).not.toBeNull()
  })
})
```

### 组件测试示例

```typescript
// components/__tests__/Button.spec.ts
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import Button from '../Button.vue'

describe('Button', () => {
  it('renders text', () => {
    const wrapper = mount(Button, {
      props: { text: 'Click me' }
    })

    expect(wrapper.text()).toBe('Click me')
  })

  it('emits click event', async () => {
    const wrapper = mount(Button)

    await wrapper.trigger('click')

    expect(wrapper.emitted('click')).toBeTruthy()
  })

  it('disables button when loading', () => {
    const wrapper = mount(Button, {
      props: { loading: true }
    })

    expect(wrapper.find('button').attributes('disabled')).toBeDefined()
  })
})
```

---

## Linting Rules

### ESLint 配置
配置文件：`.eslintrc.cjs`

主要规则：
- **vue/multi-word-component-names**：组件名必须多个单词
- **no-unused-vars**：不允许未使用的变量
- **no-console**：生产代码不允许 console
- **prefer-const**：优先使用 const
- **vue/component-name-in-template-casing**：模板中组件名使用 PascalCase

### 运行 linting

```bash
# ESLint 检查
pnpm lint:check

# ESLint 自动修复
pnpm lint

# TypeScript 类型检查
pnpm typecheck
```

---

## Code Review Checklist

### 功能性
- [ ] 功能正确实现
- [ ] 边界条件处理
- [ ] 错误处理完整
- [ ] 加载状态显示
- [ ] 空状态处理

### 代码质量
- [ ] 通过 ESLint 检查
- [ ] 通过 TypeScript 类型检查
- [ ] 通过所有测试
- [ ] 测试覆盖率达标
- [ ] 无重复代码

### 组件设计
- [ ] 使用 Composition API
- [ ] Props 和 Emits 定义类型
- [ ] 组件职责单一
- [ ] 组件可复用性
- [ ] 组件命名清晰

### 性能
- [ ] 合理使用计算属性
- [ ] 列表使用 key
- [ ] 大组件懒加载
- [ ] 图片优化
- [ ] 避免不必要的重渲染

### 可访问性
- [ ] 语义化 HTML
- [ ] ARIA 属性
- [ ] 键盘导航
- [ ] 焦点管理
- [ ] 颜色对比度

### 类型安全
- [ ] 避免使用 `any`
- [ ] Props 定义类型
- [ ] Emits 定义类型
- [ ] API 响应定义类型
- [ ] 正确使用泛型

---

## Performance Optimization

### 计算属性缓存

```vue
<script setup lang="ts">
// ✅ 使用计算属性（缓存）
const filteredItems = computed(() => {
  return items.value.filter(item => item.active)
})

// ❌ 避免在模板中使用方法
// <template v-for="item in filterItems(items)">
</script>
```

### 列表优化

```vue
<template>
  <!-- ✅ 使用 key 帮助 Vue 跟踪节点 -->
  <div v-for="item in items" :key="item.id">
    {{ item.name }}
  </div>

  <!-- ✅ 大列表使用虚拟滚动 -->
  <RecyclerScroller
    :items="largeList"
    :item-size="50"
    key-field="id"
  >
    <template #default="{ item }">
      <div>{{ item.name }}</div>
    </template>
  </RecyclerScroller>
</template>
```

### 组件懒加载

```typescript
// ✅ 路由懒加载
const Dashboard = () => import('@/views/user/Dashboard.vue')

// ✅ 组件懒加载
const HeavyComponent = defineAsyncComponent(() =>
  import('@/components/HeavyComponent.vue')
)
```

---

## Common Mistakes

### 前端常见错误

1. **不使用 TypeScript**
```vue
<!-- ❌ 错误：不定义类型 -->
<script setup>
const count = ref(0)
</script>

<!-- ✅ 正确：使用 TypeScript -->
<script setup lang="ts">
const count = ref(0)
</script>
```

2. **过度使用 watch**
```vue
<script setup lang="ts">
// ❌ 错误：能用 computed 就不要用 watch
const fullName = ref('')
watch(() => `${firstName.value} ${lastName.value}`, (value) => {
  fullName.value = value
})

// ✅ 正确：使用计算属性
const fullName = computed(() => `${firstName.value} ${lastName.value}`)
</script>
```

3. **不处理异步错误**
```vue
<script setup lang="ts">
// ❌ 错误：不处理错误
const fetchData = async () => {
  const data = await api.getData() // 没有错误处理
}

// ✅ 正确：处理错误
const fetchData = async () => {
  try {
    const data = await api.getData()
  } catch (e) {
    error.value = e as Error
  }
}
</script>
```

4. **在 created 中操作 DOM**
```vue
<script setup lang="ts">
// ❌ 错误：created 阶段不能操作 DOM
onBeforeMount(() => {
  document.getElementById('app').focus() // ❌
})

// ✅ 正确：在 mounted 中操作 DOM
onMounted(() => {
  document.getElementById('app')?.focus() // ✅
})
</script>
```

---

## Examples

### 高质量组件示例
查看这些文件了解最佳实践：
- `src/components/TurnstileWidget.vue` - 完整的组件实现
- `src/stores/auth.ts` - Store 实现
- `src/composables/` - 组合式函数示例

---

## Important Notes

1. **必须通过所有检查**：`pnpm lint`、`pnpm typecheck`、`pnpm test`
2. **使用 Composition API**：不使用 Options API
3. **TypeScript 类型定义**：所有 Props、Emits、状态都要定义类型
4. **组件测试**：新组件必须包含测试
5. **性能考虑**：合理使用计算属性、列表 key、懒加载
6. **可访问性**：添加必要的 ARIA 属性和键盘支持
7. **代码审查**：按 checklist 逐项检查
