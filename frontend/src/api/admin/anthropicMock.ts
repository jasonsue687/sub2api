import { apiClient } from '../client'

export interface AnthropicMockStatus {
  enabled: boolean
  source: 'config' | 'db' | string
  hook_armed: boolean
  replay_ready: boolean
  store_ready: boolean
  inbound_dropped: number
  outbound_dropped: number
  inbound_queued: number
  outbound_queued: number
  sample_count: number
}

export interface AnthropicMockSample {
  id: number
  created_at: string
  dedupe_key: string
  method: string
  path: string
  client_label: string
  content_type: string
  headers: Record<string, string>
  body_preview: string
  body_bytes: number
  body_sha256: string
}

export interface AnthropicMockOutbound {
  id: number
  created_at: string
  account_id: number
  method: string
  url: string
  headers: Record<string, string>
  body_preview: string
  body_bytes: number
  body_truncated: boolean
  mock_reason: string
  status_code: number
}

export interface AnthropicMockPage<T> {
  items: T[]
  total: number
  page: number
  page_size: number
  pages: number
}

export interface AnthropicMockRunReport {
  total: number
  completed: number
  failed: number
  mock_guaranteed: boolean
  results: Array<{ sample_id: number; method: string; path: string; status: number; error?: string }>
}

export async function getAnthropicMockStatus() {
  const { data } = await apiClient.get<AnthropicMockStatus>('/admin/anthropic-mock')
  return data
}

export async function updateAnthropicMock(enabled: boolean) {
  const { data } = await apiClient.put<AnthropicMockStatus>('/admin/anthropic-mock', { enabled })
  return data
}

export async function listAnthropicMockSamples(page = 1) {
  const { data } = await apiClient.get<AnthropicMockPage<AnthropicMockSample>>('/admin/anthropic-mock/samples', { params: { page, page_size: 20 } })
  return data
}

export async function listAnthropicMockOutbound(page = 1) {
  const { data } = await apiClient.get<AnthropicMockPage<AnthropicMockOutbound>>('/admin/anthropic-mock/outbound', { params: { page, page_size: 20 } })
  return data
}

export async function runAnthropicMockTests(apiKey: string, limit = 20) {
  const { data } = await apiClient.post<AnthropicMockRunReport>('/admin/anthropic-mock/run', { api_key: apiKey, limit }, { timeout: 120000 })
  return data
}
