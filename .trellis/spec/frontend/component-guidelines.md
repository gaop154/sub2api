# Component Guidelines

> How components are built in this project.

---

## Overview

项目使用 Vue 3 Composition API + TypeScript + TailwindCSS 构建组件。所有组件应遵循关注点分离原则，优先使用组合式函数和可复用组件。

**组件技术栈**：
- Vue 3.4+（Composition API）
- TypeScript
- TailwindCSS
- VueUse（组合式工具库）

---

## Component Structure

### 标准组件结构

```vue
<script setup lang="ts">
// 1. 导入
import { ref, computed, onMounted } from 'vue'
import { useRouter } from 'vue-router'

// 2. Props 定义
interface Props {
  userId: number
  title?: string
}
const props = withDefaults(defineProps<Props>(), {
  title: 'Default Title'
})

// 3. Emits 定义
interface Emits {
  (e: 'update', value: string): void
  (e: 'delete', id: number): void
}
const emit = defineEmits<Emits>()

// 4. 组合式函数
const { data, loading, error } = useUserData(props.userId)

// 5. 响应式状态
const count = ref(0)

// 6. 计算属性
const doubleCount = computed(() => count.value * 2)

// 7. 方法
const increment = () => {
  count.value++
  emit('update', count.value.toString())
}

// 8. 生命周期
onMounted(() => {
  console.log('Component mounted')
})

// 9. 暴露给父组件
defineExpose({
  increment,
  count
})
</script>

<template>
  <div class="component-wrapper">
    <h2>{{ props.title }}</h2>
    <p>Count: {{ count }}</p>
    <button @click="increment">Increment</button>
  </div>
</template>

<style scoped>
.component-wrapper {
  /* 组件样式 */
}
</style>
```

---

## Props Conventions

### Props 定义

```typescript
// 1. 使用 TypeScript 接口定义 Props
interface Props {
  userId: number
  title: string
  isActive?: boolean
  metadata?: Record<string, any>
}

// 2. 使用 withDefaults 设置默认值
const props = withDefaults(defineProps<Props>(), {
  isActive: false,
  metadata: () => ({})
})

// 3. 简单 Props 可以直接定义
const props = defineProps<{
  userId: number
  title: string
}>()
```

### Props 命名
- 使用 camelCase：`userId`、`isActive`
- 布尔值使用 `is/has/should` 前缀
- 事件处理器使用 `on` 前缀：`onUpdate`、`onDelete`

### Props 校验
```typescript
// 使用 TypeScript 进行类型检查
interface Props {
  // 必需属性
  userId: number
  
  // 可选属性
  title?: string
  
  // 带默认值的属性
  pageSize?: number
  
  // 复杂类型
  items?: Array<{ id: number; name: string }>
}
```

---

## Emits Conventions

### 事件定义

```typescript
// 1. 使用 TypeScript 接口定义 Emits
interface Emits {
  (e: 'update', value: string): void
  (e: 'delete', id: number): void
  (e: 'change', payload: ChangePayload): void
}
const emit = defineEmits<Emits>()

// 2. 触发事件
const handleClick = () => {
  emit('update', 'new value')
  emit('delete', props.userId)
}

// 3. 简单事件
const emit = defineEmits<{
  update: [value: string]
  delete: [id: number]
}>()
```

### 事件命名
- 使用 kebab-case：`update-value`、`delete-item`
- 使用描述性名称：`form-submit`、`data-loaded`

---

## Component Composition

### 组合式函数（Composables）

```typescript
// composables/useUserData.ts
export function useUserData(userId: Ref<number>) {
  const data = ref<User | null>(null)
  const loading = ref(false)
  const error = ref<Error | null>(null)

  const fetchUserData = async () => {
    loading.value = true
    try {
      data.value = await userAPI.getUser(userId.value)
    } catch (e) {
      error.value = e as Error
    } finally {
      loading.value = false
    }
  }

  onMounted(() => {
    fetchUserData()
  })

  return {
    data,
    loading,
    error,
    refresh: fetchUserData
  }
}
```

### 在组件中使用组合式函数

```vue
<script setup lang="ts">
import { useUserData } from '@/composables/useUserData'

const props = defineProps<{ userId: number }>()
const userIdRef = computed(() => props.userId)
const { data, loading, error } = useUserData(userIdRef)
</script>
```

---

## Styling Patterns

### TailwindCSS 优先

```vue
<template>
  <!-- 使用 Tailwind 类 -->
  <div class="flex items-center justify-between p-4 bg-white rounded-lg shadow">
    <h2 class="text-xl font-semibold text-gray-900">{{ title }}</h2>
    <button class="px-4 py-2 bg-blue-600 text-white rounded hover:bg-blue-700">
      Click Me
    </button>
  </div>
</template>
```

### Scoped CSS

```vue
<style scoped>
/* 组件特定样式 */
.custom-component {
  /* 无法用 Tailwind 表达的样式 */
  background: linear-gradient(45deg, #f3f4f6, #e5e7eb);
}

/* 深度选择器 */
.custom-component :deep(.child-element) {
  color: red;
}
</style>
```

### 响应式设计

```vue
<template>
  <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
    <!-- 响应式网格布局 -->
  </div>
</template>
```

---

## Component Patterns

### 受控组件

```vue
<script setup lang="ts">
interface Props {
  modelValue: string
}

interface Emits {
  (e: 'update:modelValue', value: string): void
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()

const updateValue = (event: Event) => {
  const target = event.target as HTMLInputElement
  emit('update:modelValue', target.value)
}
</script>

<template>
  <input
    :value="props.modelValue"
    @input="updateValue"
    class="border rounded px-2 py-1"
  />
</template>
```

### 列表组件

```vue
<script setup lang="ts">
interface Item {
  id: number
  name: string
}

interface Props {
  items: Item[]
  loading?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  loading: false
})

interface Emits {
  (e: 'item-click', item: Item): void
}

const emit = defineEmits<Emits>()

const handleClick = (item: Item) => {
  emit('item-click', item)
}
</script>

<template>
  <div v-if="props.loading">Loading...</div>
  <ul v-else>
    <li
      v-for="item in props.items"
      :key="item.id"
      @click="handleClick(item)"
      class="cursor-pointer hover:bg-gray-100"
    >
      {{ item.name }}
    </li>
  </ul>
</template>
```

### 异步数据组件

```vue
<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useUserData } from '@/composables/useUserData'

const props = defineProps<{ userId: number }>()
const { data, loading, error, refresh } = useUserData(computed(() => props.userId))
</script>

<template>
  <div v-if="loading">Loading...</div>
  <div v-else-if="error">Error: {{ error.message }}</div>
  <div v-else-if="data">
    {{ data.name }}
  </div>
  <button @click="refresh" class="mt-2 px-4 py-2 bg-blue-600 text-white rounded">
    Refresh
  </button>
</template>
```

---

## Accessibility

### ARIA 属性

```vue
<template>
  <!-- 语义化 HTML -->
  <button
    @click="handleAction"
    :aria-label="ariaLabel"
    :disabled="disabled"
    class="px-4 py-2 bg-blue-600 text-white rounded"
  >
    {{ buttonText }}
  </button>

  <!-- 表单标签 -->
  <label for="email" class="block text-sm font-medium text-gray-700">
    Email
  </label>
  <input
    id="email"
    type="email"
    v-model="email"
    aria-describedby="email-hint"
    class="mt-1 block w-full border rounded"
  />
  <p id="email-hint" class="text-sm text-gray-500">
    We'll never share your email.
  </p>
</template>
```

### 键盘导航

```vue
<template>
  <div
    @keydown.enter="handleSubmit"
    @keydown.escape="handleCancel"
    tabindex="0"
    role="button"
    :aria-label="label"
  >
    {{ content }}
  </div>
</template>
```

### 焦点管理

```vue
<script setup lang="ts">
import { ref, onMounted, nextTick } from 'vue'

const inputRef = ref<HTMLInputElement>()

onMounted(() => {
  nextTick(() => {
    inputRef.value?.focus()
  })
})
</script>

<template>
  <input ref="inputRef" type="text" v-model="text" />
</template>
```

---

## Performance Optimization

### 计算属性缓存

```typescript
// ✅ 使用计算属性（缓存）
const filteredItems = computed(() => {
  return items.value.filter(item => item.active)
})

// ❌ 避免在模板中使用方法（每次都重新计算）
// <template v-for="item in filterItems(items)">
```

### 列表优化

```vue
<template>
  <!-- 使用 key 帮助 Vue 跟踪节点 -->
  <div v-for="item in items" :key="item.id">
    {{ item.name }}
  </div>

  <!-- 大列表使用虚拟滚动 -->
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
// 路由懒加载
const Dashboard = () => import('@/views/user/Dashboard.vue')

// 组件懒加载
const HeavyComponent = defineAsyncComponent(() =>
  import('@/components/HeavyComponent.vue')
)
```

---

## Common Mistakes

1. **直接修改 props**
```typescript
// ❌ 错误
props.userId = 123

// ✅ 正确：通过 emit 通知父组件
emit('update-user-id', 123)
```

2. **在模板中使用复杂表达式**
```vue
<!-- ❌ 错误 -->
<div>{{ formatDate(parseDate(user.birthDate)) }}</div>

<!-- ✅ 正确：使用计算属性 -->
<div>{{ formattedBirthDate }}</div>
```

3. **不使用 TypeScript 类型**
```typescript
// ❌ 错误
const props = defineProps({
  userId: Number,
  title: String
})

// ✅ 正确：使用 TypeScript
interface Props {
  userId: number
  title: string
}
const props = defineProps<Props>()
```

4. **过度使用 watch**
```typescript
// ❌ 错误：能用 computed 就不要用 watch
const fullName = ref('')
watch(() => `${firstName.value} ${lastName.value}`, (value) => {
  fullName.value = value
})

// ✅ 正确：使用计算属性
const fullName = computed(() => `${firstName.value} ${lastName.value}`)
```

---

## Examples

### 查看实际组件示例
- `src/components/common/` - 通用组件示例
- `src/components/layout/AppLayout.vue` - 布局组件
- `src/components/TurnstileWidget.vue` - 完整的组件实现

---

## Important Notes

1. **使用 Composition API**：不使用 Options API
2. **TypeScript 类型定义**：所有 Props、Emits 都要定义类型
3. **组合式函数优先**：可复用逻辑提取到 composables
4. **TailwindCSS 优先**：优先使用 Tailwind 类而非自定义 CSS
5. **性能考虑**：合理使用计算属性、列表 key、懒加载
6. **可访问性**：添加必要的 ARIA 属性和键盘支持
