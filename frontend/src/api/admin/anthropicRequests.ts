import { apiClient } from '../client'
import type { OpsSystemLogSinkHealth } from './ops'

export interface AnthropicAudit {
  schema: number
  attempt_id: string
  started_at: string
  endpoint: string
  origin: { source: string; mimic?: boolean; subscription: boolean }
  headers: Record<string, string>
  parameters: Record<string, string | number | boolean>
  body_state: string
  cc_version?: string
  cc_entrypoint?: string
  device_hash?: string
  account_hash?: string
  session_hash?: string
  identity_signature: string
  parameter_signature?: string
  consistency: 'matched' | 'mismatch' | 'unknown'
  issues: string[]
  status: number
  error_class?: string
  upstream_request_id?: string
  headers_ms: number
}
export interface AnthropicRecord {
  id: number
  created_at: string
  request_id: string
  client_request_id: string
  audit: AnthropicAudit
}
export interface AnthropicRequestList {
  summary: {
    attempts: number
    correlated_requests: number
    uncorrelated_attempts: number
    matched: number
    mismatched: number
    unknown: number
    http_failures: number
    transport_errors: number
    identity_variants: number
    parameter_variants: number
  }
  variants: Array<{ signature: string; count: number; user_agent: string; entrypoint: string; version: string; device_hash: string; first_seen: string; last_seen: string }>
  records: AnthropicRecord[]
  total: number
  page: number
  page_size: number
  account_id: number
  start_time: string
  end_time: string
  collection_enabled: boolean
  sink_health: OpsSystemLogSinkHealth
}
export interface AnthropicQuery {
  account_id: number
  start_time: string
  end_time: string
  page: number
  page_size: number
  only_mismatch: boolean
}
export async function listAnthropicRequests(params: AnthropicQuery, signal?: AbortSignal) {
  const { data } = await apiClient.get<AnthropicRequestList>('/admin/ops/anthropic-requests', { params, signal })
  return data
}
