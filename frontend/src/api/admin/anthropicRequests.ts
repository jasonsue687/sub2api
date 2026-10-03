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

export interface AnthropicSessionSummary {
  inbound_requests: number
  outbound_attempts: number
  multi_attempts: number
  failed_requests: number
}
export interface AnthropicAccountOption {
  id: number
  name: string
}
export interface AnthropicSessionRow {
  client_request_id: string
  request_id: string
  created_at: string
  api_key_id: number
  api_key_name: string
  username: string
  user_id: number
  endpoint: string
  inbound_model: string
  outbound_model: string
  stream: boolean
  status: number
  account_id: number
  account_name: string
  attempt_count: number
  duration_ms: number
  has_inbound: boolean
  has_outbound: boolean
  error_class: string
  consistency: string
  truncated: boolean
}
export interface AnthropicCaptureHealth {
  queue_capacity: number
  queue_depth: number
  dropped_count: number
  write_failed_count: number
  written_count: number
}
export interface AnthropicSessionList {
  summary: AnthropicSessionSummary
  records: AnthropicSessionRow[]
  total: number
  page: number
  page_size: number
  accounts: AnthropicAccountOption[]
  models: string[]
  start_time: string
  end_time: string
  inbound_enabled: boolean
  capture_health: AnthropicCaptureHealth
}
export interface AnthropicSessionQuery {
  start_time: string
  end_time: string
  page: number
  page_size: number
  account_id?: number
  only_multi?: boolean
  only_failed?: boolean
  only_mismatch?: boolean
  model?: string
  q?: string
}
export interface AnthropicCapture {
  direction: string
  client_request_id: string
  request_id: string
  account_id: number
  account_name: string
  user_id: number
  api_key_id: number
  api_key_name: string
  username: string
  endpoint: string
  client_path: string
  model: string
  stream?: boolean
  attempt_seq: number
  retry_reason: string
  account_switch_count: number
  attempt_count: number
  status: number
  error_class: string
  upstream_request_id: string
  duration_ms: number
  headers_ms: number
  headers?: unknown
  body?: unknown
  body_state: string
  summary?: unknown
  consistency: string
  truncated: boolean
  original_bytes: number
  created_at: string
}
export interface AnthropicSessionDetail {
  session: AnthropicSessionRow
  inbound: AnthropicCapture | null
  attempts: AnthropicCapture[]
}
export async function listAnthropicSessions(params: AnthropicSessionQuery, signal?: AbortSignal) {
  const { data } = await apiClient.get<AnthropicSessionList>('/admin/ops/anthropic-request-sessions', { params, signal })
  return data
}
export async function getAnthropicSession(clientRequestId: string, signal?: AbortSignal) {
  const { data } = await apiClient.get<AnthropicSessionDetail>('/admin/ops/anthropic-request-sessions/detail', {
    params: { client_request_id: clientRequestId },
    signal
  })
  return data
}
