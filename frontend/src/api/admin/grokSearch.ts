/**
 * Admin Grok Search (console.x.ai SSO 通道) API endpoints
 *
 * 与现有 grok 平台物理隔离：不走 OAuth token 兑换，SSO cookie 直接作为
 * console.x.ai/v1/responses 的凭证，按网页订阅配额计费，绕开 Responses API
 * 的 personal-team 402。
 */

import { apiClient } from '../client'

/**
 * 批量导入 Grok Search SSO 账号请求体。
 * `sso_tokens` 为多行字符串（每行一个 SSO token），由后端逐行解析归一化。
 */
export interface GrokSearchSSOImportRequest {
  sso_tokens: string
  base_url?: string
  proxy_id?: number | null
  group_ids?: number[]
}

/** 单条导入失败结果（保留出错的 token 便于定位） */
export interface GrokSearchSSOImportFailure {
  token: string
  error: string
}

/** 单条导入成功结果 */
export interface GrokSearchSSOImportSuccess {
  token?: string
  name?: string
  email?: string
  account?: unknown
}

export interface GrokSearchSSOImportResponse {
  created: GrokSearchSSOImportSuccess[]
  failed: GrokSearchSSOImportFailure[]
}

// SSO 导入按批次计算超时，避免大量 token 时请求被前端提前中断。
// 单批耗时主要来自账号落库（无远程 OAuth 兑换），相对 grok SSO→OAuth 更快，
// 这里沿用相同的超时估算策略以保留足够余量。
const GROK_SEARCH_SSO_IMPORT_CONCURRENCY = 3
const GROK_SEARCH_SSO_IMPORT_TIMEOUT_PER_BATCH_MS = 60_000
const GROK_SEARCH_SSO_IMPORT_TIMEOUT_BUFFER_MS = 60_000

export function getGrokSearchSSOImportTimeout(lineCount: number): number {
  const batches = Math.ceil(Math.max(1, lineCount) / GROK_SEARCH_SSO_IMPORT_CONCURRENCY)
  return batches * GROK_SEARCH_SSO_IMPORT_TIMEOUT_PER_BATCH_MS + GROK_SEARCH_SSO_IMPORT_TIMEOUT_BUFFER_MS
}

/**
 * 批量导入 Grok Search SSO 账号。
 * POST /admin/grok-search/sso
 */
export async function createFromSSO(
  payload: GrokSearchSSOImportRequest
): Promise<GrokSearchSSOImportResponse> {
  // 按行数估算超时（空行也会被计入，与后端逐行解析一致）
  const lineCount = payload.sso_tokens
    ? payload.sso_tokens.split('\n').filter((l) => l.trim()).length
    : 0
  const { data } = await apiClient.post<GrokSearchSSOImportResponse>(
    '/admin/grok-search/sso',
    payload,
    { timeout: getGrokSearchSSOImportTimeout(lineCount) }
  )
  return data
}

export default { createFromSSO }
