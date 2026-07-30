# Type Safety

> Type safety patterns in this project.

---

## Overview

项目使用 TypeScript 提供类型安全。所有代码应该充分利用 TypeScript 的类型系统，避免使用 `any`，优先使用类型推断而非显式类型注解。

**类型系统**：
- TypeScript 5.x
- 严格模式启用（`strict: true`）
- 组合式函数使用泛型提供类型推断

---

## Type Organization

### 目录结构

```
src/
├── types/
│   ├── index.ts           # 通用类型定义
│   ├── api.ts             # API 相关类型
│   ├── user.ts            # 用户相关类型
│   ├── admin.ts           # 管理后台类型
│   └── components/        # 组件特定类型
└── components/
    └── *.vue              # 组件内部类型（使用 <script setup lang="ts">）
```

### 类型定义规范

```typescript
// types/user.ts
export interface User {
  id: number
  email: string
  username: string
  role: 'user' | 'admin'
  created_at: string
  updated_at: string
}

export interface LoginRequest {
  email: string
  password: string
}

export interface LoginResponse {
  access_token: string
  refresh_token?: string
  expires_in: number
  user: User
}

// 错误类型
export interface APIError {
  code: number
  message: string
  data?: unknown
}

// 分页类型
export interface PaginatedResponse<T> {
  data: T[]
  total: number
  page: number
  pageSize: number
}
```

### 组件类型定义

```vue
<script setup lang="ts">
// 1. Props 类型
interface Props {
  userId: number
  title?: string
  metadata?: Record<string, unknown>
}

const props = withDefaults(defineProps<Props>(), {
  title: 'Default Title',
  metadata: () => ({})
})

// 2. Emits 类型
interface Emits {
  (e: 'update', value: string): void
  (e: 'delete', id: number): void
}

const emit = defineEmits<Emits>()

// 3. 组件内部类型
interface FormData {
  username: string
  email: string
  password: string
}

const form = reactive<FormData>({
  username: '',
  email: '',
  password: ''
})
</script>
```

---

## Validation Patterns

### 运行时验证

虽然 TypeScript 提供编译时类型检查，但处理外部数据（API 响应、用户输入）时需要运行时验证。

```typescript
// utils/validation.ts
// 类型守卫
export function isUser(value: unknown): value is User {
  return (
    typeof value === 'object' &&
    value !== null &&
    'id' in value &&
    'email' in value &&
    'username' in value
  )
}

export function isAPIError(value: unknown): value is APIError {
  return (
    typeof value === 'object' &&
    value !== null &&
    'code' in value &&
    'message' in value
  )
}
```

### Zod 验证（推荐）

```typescript
import { z } from 'zod'

// 定义 schema
const UserSchema = z.object({
  id: z.number(),
  email: z.string().email(),
  username: z.string().min(3).max(50),
  role: z.enum(['user', 'admin']),
  created_at: z.string(),
  updated_at: z.string()
})

// 类型推断
type User = z.infer<typeof UserSchema>

// 使用
const validateUser = (data: unknown) => {
  const result = UserSchema.safeParse(data)
  if (!result.success) {
    throw new Error('Invalid user data')
  }
  return result.data // 类型为 User
}
```

### Props 验证

```vue
<script setup lang="ts">
import { z } from 'zod'

// 定义 schema
const UserPropsSchema = z.object({
  userId: z.number().positive(),
  title: z.string().min(1).max(100).optional(),
  active: z.boolean().default(false)
})

type UserProps = z.infer<typeof UserPropsSchema>

const props = defineProps<UserProps>()

// 运行时验证
const validatedProps = UserPropsSchema.parse(props)
</script>
```

---

## Common Patterns

### 泛型类型

```typescript
// API 响应类型
interface APIResponse<T> {
  code: number
  message: string
  data: T
}

// 使用
type UserListResponse = APIResponse<User[]>
type UserDetailResponse = APIResponse<User>

// 组合式函数泛型
export function useAPI<T>(
  fetcher: () => Promise<T>
) {
  const data = ref<T | null>(null)
  const loading = ref(false)
  const error = ref<Error | null>(null)

  const execute = async () => {
    loading.value = true
    try {
      data.value = await fetcher()
    } catch (e) {
      error.value = e as Error
    } finally {
      loading.value = false
    }
  }

  return { data, loading, error, execute }
}

// 使用时类型推断
const { data } = useAPI(() => userAPI.getUsers())
// data 类型自动推断为 User[] | null
```

### 类型守卫

```typescript
// 窄化类型
function isString(value: unknown): value is string {
  return typeof value === 'string'
}

function isNumber(value: unknown): value is number {
  return typeof value === 'number'
}

// 使用
function processValue(value: unknown) {
  if (isString(value)) {
    // value 类型为 string
    console.log(value.toUpperCase())
  } else if (isNumber(value)) {
    // value 类型为 number
    console.log(value * 2)
  }
}
```

### 联合类型和交叉类型

```typescript
// 联合类型
type Status = 'pending' | 'active' | 'inactive' | 'banned'

type UserRole = 'user' | 'admin' | 'superadmin'

// 交叉类型
type UserWithTimestamp = User & {
  created_at: string
  updated_at: string
}

// 辨别联合类型
interface SuccessState {
  status: 'success'
  data: string
}

interface ErrorState {
  status: 'error'
  error: Error
}

interface LoadingState {
  status: 'loading'
}

type AsyncState = SuccessState | ErrorState | LoadingState

// 使用
function handleState(state: AsyncState) {
  switch (state.status) {
    case 'success':
      // state.data 可用
      console.log(state.data)
      break
    case 'error':
      // state.error 可用
      console.error(state.error)
      break
    case 'loading':
      console.log('Loading...')
  }
}
```

### 工具类型

```typescript
// Partial - 所有属性可选
type UserUpdate = Partial<User>

// Required - 所有属性必需
type RequiredUser = Required<User>

// Readonly - 只读
type ReadonlyUser = Readonly<User>

// Pick - 选择部分属性
type UserBasic = Pick<User, 'id' | 'username' | 'email'>

// Omit - 排除部分属性
type UserWithoutPassword = Omit<User, 'password'>

// Record - 键值对类型
type UserDictionary = Record<number, User>

// ReturnType - 获取函数返回值类型
type FetchUserReturnType = ReturnType<typeof userAPI.getUser>
```

---

## Type Inference

### 优先使用类型推断

```typescript
// ✅ 类型推断（推荐）
const count = ref(0) // 推断为 Ref<number>
const name = ref('test') // 推断为 Ref<string>

// ❌ 显式类型（不必要）
const count = ref<number>(0)
const name = ref<string>('test')

// ✅ 需要显式类型的情况
const data = ref<User | null>(null)
```

### 函数返回值类型推断

```typescript
// ✅ 类型推断
const getUserById = (id: number) => {
  return userAPI.getUser(id)
  // 推断返回 Promise<User>
}

// ❌ 显式返回类型（不必要）
const getUserById = (id: number): Promise<User> => {
  return userAPI.getUser(id)
}
```

### 组合式函数类型推断

```typescript
// ✅ 使用泛型提供类型推断
export function useUserData(userId: Ref<number>) {
  const data = ref<User | null>(null)
  const loading = ref(false)

  const fetch = async () => {
    loading.value = true
    data.value = await userAPI.getUser(userId.value)
    loading.value = false
  }

  return {
    data, // Ref<User | null>
    loading, // Ref<boolean>
    refresh: fetch // () => Promise<void>
  }
}
```

---

## Forbidden Patterns

### 禁止使用 `any`

```typescript
// ❌ 禁止：使用 any
function processData(data: any) {
  return data.map((item: any) => item.value)
}

// ✅ 正确：使用具体类型或泛型
function processData<T extends { value: unknown }>(data: T[]) {
  return data.map(item => item.value)
}

// ✅ 正确：定义接口
interface Item {
  value: string
}

function processData(data: Item[]) {
  return data.map(item => item.value)
}
```

### 禁止类型断言（除非必要）

```typescript
// ❌ 禁止：过度使用类型断言
const element = document.getElementById('app') as HTMLDivElement
const value = (data as any).value

// ✅ 正确：使用类型守卫
const element = document.getElementById('app')
if (element instanceof HTMLDivElement) {
  // element 类型为 HTMLDivElement
}

// ✅ 正当使用类型断言：JSON.parse
const user = JSON.parse(jsonString) as User
```

### 禁止 `@ts-ignore` 和 `@ts-expect-error`

```typescript
// ❌ 禁止：忽略类型错误
// @ts-ignore
const result = riskyOperation()

// ✅ 正确：修复类型错误
interface Result {
  value: string
}

const result: Result = riskyOperation()
```

### 避免可选类型滥用

```typescript
// ❌ 不必要：过度使用可选
interface User {
  id?: number
  email?: string
  name?: string
}

// ✅ 正确：区分必需和可选
interface User {
  id: number
  email: string
  name?: string // 真正可选的字段
}
```

---

## API Type Safety

### API 客户端类型

```typescript
// api/client.ts
import axios, type { AxiosInstance, AxiosRequestConfig, AxiosResponse } from 'axios'

// 配置类型
interface APIConfig {
  baseURL: string
  timeout?: number
  headers?: Record<string, string>
}

// 响应拦截器类型
interface AxiosInterceptor {
  onFulfilled?: (value: AxiosResponse) => AxiosResponse | Promise<AxiosResponse>
  onRejected?: (error: unknown) => unknown
}

// 类型安全的 API 客户端
class APIClient {
  private instance: AxiosInstance

  constructor(config: APIConfig) {
    this.instance = axios.create(config)
  }

  async get<T>(url: string, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.get<T>(url, config)
    return response.data
  }

  async post<T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> {
    const response = await this.instance.post<T>(url, data, config)
    return response.data
  }
}

// 使用
const client = new APIClient({ baseURL: '/api/v1' })

type UserList = User[]
const users = await client.get<UserList>('/users')
// users 类型为 User[]
```

### API 类型定义

```typescript
// types/api.ts
export interface UserListResponse {
  code: number
  message: string
  data: User[]
}

export interface UserDetailResponse {
  code: number
  message: string
  data: User
}

// API 函数
export const userAPI = {
  getUsers: (): Promise<UserListResponse> => {
    return client.get('/users')
  },

  getUser: (id: number): Promise<UserDetailResponse> => {
    return client.get(`/users/${id}`)
  }
}

// 使用
const response = await userAPI.getUsers()
// response 类型为 UserListResponse
```

---

## Common Mistakes

### 1. 不使用类型

```typescript
// ❌ 错误：不使用类型
function getUser(id: number) {
  return fetch(`/users/${id}`).then(res => res.json())
}

// ✅ 正确：定义返回类型
async function getUser(id: number): Promise<User> {
  const response = await fetch(`/users/${id}`)
  return response.json() as User
}
```

### 2. 过度使用 `any`

```typescript
// ❌ 错误：过度使用 any
function handleData(data: any) {
  console.log(data.value)
}

// ✅ 正确：定义具体类型
interface Data {
  value: string
}

function handleData(data: Data) {
  console.log(data.value)
}
```

### 3. 不处理 `null`/`undefined`

```typescript
// ❌ 错误：可能为 null
function getUserEmail(user: User | null) {
  return user.email // 编译错误
}

// ✅ 正确：处理 null 情况
function getUserEmail(user: User | null) {
  if (!user) return null
  return user.email
}
```

---

## Best Practices

### 1. 启用严格模式

```json
// tsconfig.json
{
  "compilerOptions": {
    "strict": true,
    "noImplicitAny": true,
    "strictNullChecks": true,
    "strictFunctionTypes": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true
  }
}
```

### 2. 使用类型检查命令

```bash
# TypeScript 类型检查
pnpm typecheck
```

### 3. 充分利用 IDE 类型提示

- 使用 VSCode 或 WebStorm
- 安装 Vue Language Features (Volar)
- 启用 TypeScript 服务

---

## Important Notes

1. **严格模式**：项目启用 TypeScript 严格模式
2. **避免 any**：除特殊情况外不使用 `any`
3. **类型优先**：优先使用类型推断而非显式注解
4. **运行时验证**：处理外部数据时使用运行时验证
5. **类型检查**：提交代码前运行 `pnpm typecheck`
6. **组件类型**：组件 Props 和 Emits 必须定义类型
