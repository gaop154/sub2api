# Directory Structure

> How frontend code is organized in this project.

---

## Overview

前端使用 Vue 3 + Vite + TypeScript 构建，采用组合式 API 和 Pinia 状态管理。代码按功能和层次组织，遵循关注点分离原则。

**技术栈**：
- Vue 3.4+（Composition API）
- TypeScript
- Vite 5+
- Pinia（状态管理）
- Vue Router（路由）
- Vitest（测试）
- TailwindCSS（样式）

---

## Directory Layout

```
frontend/
├── public/                  # 静态资源
│   └── vite.svg
├── src/
│   ├── api/                 # API 调用层
│   │   ├── client.ts        # Axios 客户端配置
│   │   ├── index.ts         # 用户 API 出口
│   │   ├── admin/           # 管理 API
│   │   └── setup.ts         # Setup 流程 API
│   ├── assets/              # 资源文件
│   ├── components/          # 可复用组件
│   │   ├── common/          # 通用组件（表格、分页、弹窗等）
│   │   ├── layout/          # 布局组件
│   │   ├── admin/           # 管理相关业务组件
│   │   └── account/         # 账号相关业务组件
│   ├── composables/         # 组合式函数
│   ├── constants/           # 常量定义
│   ├── features/            # 功能模块
│   ├── i18n/                # 国际化
│   │   ├── locales/         # 语言文件（en、zh）
│   │   └── index.ts
│   ├── router/              # 路由配置
│   │   └── index.ts
│   ├── stores/              # Pinia 状态管理
│   ├── styles/              # 全局样式
│   ├── types/               # TypeScript 类型定义
│   ├── utils/               # 工具函数
│   ├── views/               # 页面组件
│   │   ├── auth/            # 认证页面
│   │   ├── user/            # 用户页面
│   │   ├── admin/           # 管理页面
│   │   └── setup/           # Setup 页面
│   ├── App.vue              # 应用根组件
│   └── main.ts              # 应用入口
├── .eslintrc.cjs            # ESLint 配置
├── index.html               # HTML 模板
├── package.json             # 依赖配置
├── pnpm-workspace.yaml      # PNPM 工作空间
├── tsconfig.json            # TypeScript 配置
├── tsconfig.node.json       # Node TypeScript 配置
├── vite.config.ts           # Vite 配置
└── vitest.config.ts         # Vitest 配置
```

---

## Module Organization

### 新增功能模块时的组织原则

1. **页面组件** (`src/views/`)
   - 按功能分组：`auth/`、`user/`、`admin/`、`setup/`
   - 命名规范：使用描述性名称，如 `Dashboard.vue`、`UserManagement.vue`
   - 使用路由懒加载

2. **可复用组件** (`src/components/`)
   - `common/`：通用组件（Button、Modal、Table 等）
   - `layout/`：布局组件
   - `admin/`、`account/`：业务相关组件

3. **API 层** (`src/api/`)
   - `client.ts`：统一 Axios 配置
   - `index.ts`：用户 API 出口
   - `admin/`：管理后台 API

4. **状态管理** (`src/stores/`)
   - 每个 Store 管理一个领域的状态
   - 使用 Composition API 风格

5. **类型定义** (`src/types/`)
   - 集中定义 TypeScript 接口和类型

---

## File Naming Conventions

### Vue 组件
- 使用 PascalCase：`UserManagement.vue`、`ApiKeyList.vue`
- 组件文件应与组件名一致

### TypeScript 文件
- 使用 camelCase：`userService.ts`、`apiClient.ts`
- 测试文件：`*.spec.ts` 或 `*.test.ts`

### 目录
- 使用小写字母：`api/`、`stores/`、`utils/`
- 多单词使用连字符：`i18n/`、`__tests__/`

---

## Component Organization

### 组件分层

1. **页面组件** (`src/views/`)
   - 完整的页面，包含业务逻辑
   - 使用路由懒加载
   - 通过 `useRouter()` 和 `useRoute()` 访问路由

2. **布局组件** (`src/components/layout/`)
   - `AppLayout`：已登录用户和管理后台的布局
   - `AuthLayout`：认证页面布局

3. **通用组件** (`src/components/common/`)
   - 可在任何地方复用的基础组件
   - 不包含业务逻辑

4. **业务组件** (`src/components/admin/`、`src/components/account/`)
   - 特定领域的业务组件
   - 通常被对应的页面组件使用

---

## Route Organization

### 路由分层
```typescript
// 公开路由
/home, /login, /register, /setup

// 用户路由
/dashboard, /keys, /usage, /profile

// 管理路由
/admin/dashboard, /admin/users, /admin/groups
```

### 路由配置位置
- 主路由文件：`src/router/index.ts`
- 路由守卫：在同文件中定义
- 路由预加载：使用 `useRoutePrefetch` composable

---

## API Organization

### API 分层

1. **客户端配置** (`src/api/client.ts`)
   - Axios 实例配置
   - 请求/响应拦截器
   - Token 刷新逻辑

2. **用户 API** (`src/api/index.ts`)
   - 普通用户接口
   - 按功能分组导出

3. **管理 API** (`src/api/admin/index.ts`)
   - 管理后台接口
   - 按模块分组

4. **Setup API** (`src/api/setup.ts`)
   - 安装向导接口
   - 使用独立的 Axios 实例

---

## Store Organization

### Store 分层
主要 Store：
- `auth.ts`：认证状态
- `app.ts`：全局 UI 和配置
- `subscriptions.ts`：订阅数据
- `announcements.ts`：公告管理
- `adminSettings.ts`：管理设置

---

## Naming Conventions

### 变量和函数
- 使用 camelCase：`userName`、`getUserData()`
- 布尔值使用 `is/has/should` 前缀：`isAdmin`、`hasPermission`

### 组件名
- 使用 PascalCase：`UserProfile`、`ApiKeyList`
- 多单词组合：`UserManagementPanel`

### 接口和类型
- 使用 PascalCase：`User`、`LoginRequest`
- 后缀类型：`UserResponse`、`LoginRequest`

### 常量
- 使用 SCREAMING_SNAKE_CASE：`API_BASE_URL`、`MAX_RETRIES`

---

## Examples

### 页面组件示例
- `src/views/user/Dashboard.vue` - 用户仪表盘
- `src/views/admin/UserManagement.vue` - 用户管理

### 可复用组件示例
- `src/components/common/DataTable.vue` - 数据表格
- `src/components/layout/AppLayout.vue` - 应用布局

### Store 示例
- `src/stores/auth.ts` - 认证状态管理
- `src/stores/app.ts` - 全局状态管理

### API 示例
- `src/api/index.ts` - 用户 API
- `src/api/admin/index.ts` - 管理 API

---

## Special Conventions

### 构建输出
- 前端构建输出到 `../backend/internal/web/dist`
- 这是与后端集成的发布目录
- 不是前端目录内的 `dist`

### 环境变量
- `VITE_DEV_PORT`：开发服务器端口
- `VITE_DEV_PROXY_TARGET`：代理目标
- `VITE_API_BASE_URL`：API 基础 URL

### 包管理器
- 统一使用 `pnpm`
- 不要使用 `npm` 或 `yarn`

### 开发命令
```bash
pnpm install      # 安装依赖
pnpm dev          # 启动开发服务器
pnpm build        # 构建生产版本
pnpm test         # 运行测试
pnpm lint         # ESLint 检查
pnpm typecheck    # TypeScript 类型检查
```

---

## Important Notes

1. **组件使用 Composition API**：不使用 Options API
2. **状态管理使用 Pinia**：不使用 Vuex
3. **路由懒加载**：所有页面组件应该懒加载
4. **类型安全**：充分利用 TypeScript 类型系统
5. **组件复用**：优先使用 `components/common` 中的通用组件
6. **API 调用统一**：使用 `src/api/` 中的 API 函数，不在组件中直接调用 Axios
