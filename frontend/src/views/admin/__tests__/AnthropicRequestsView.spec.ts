import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { comparisonFields } from '@/utils/anthropicRequests'
import type { AnthropicAudit, AnthropicRequestList } from '@/api/admin/anthropicRequests'
import View from '../AnthropicRequestsView.vue'

const { query } = vi.hoisted(() => ({ query: vi.fn() }))
vi.mock('@/api/admin/anthropicRequests', () => ({ listAnthropicRequests: query }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: { account_id: '11' } }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key, te: () => true }) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))
const audit: AnthropicAudit = { schema: 1, attempt_id: 'demo', started_at: '2026-09-27T12:00:00Z', endpoint: '/v1/messages', origin: { source: 'gateway', mimic: false, subscription: true }, headers: { 'user-agent': 'claude-cli/2.1.283 (external, cli)' }, parameters: { model: 'claude-sonnet-4-5', stream: false }, cc_entrypoint: 'local-agent', cc_version: '2.1.283.abc', body_state: 'parsed', identity_signature: 'demo', consistency: 'mismatch', issues: ['entrypoint_mismatch'], status: 200, headers_ms: 400 }
const result = (): AnthropicRequestList => ({
  summary: { attempts: 1, correlated_requests: 1, uncorrelated_attempts: 0, matched: 0, mismatched: 1, unknown: 0, http_failures: 0, transport_errors: 0, identity_variants: 1, parameter_variants: 1 },
  variants: [], records: [{ id: 123, created_at: audit.started_at, request_id: 'r1', client_request_id: 'c1', audit }], total: 1, page: 1, page_size: 50, account_id: 11, start_time: '2026-09-26T12:00:00Z', end_time: '2026-09-27T12:00:00Z', collection_enabled: true,
  sink_health: { queue_capacity: 5000, queue_depth: 0, dropped_count: 0, write_failed_count: 0, written_count: 1, avg_write_delay_ms: 0, last_error: '' }
})
const wrappers: ReturnType<typeof mount>[] = []
const render = () => { const wrapper = mount(View); wrappers.push(wrapper); return wrapper }
beforeEach(() => { vi.clearAllMocks(); query.mockResolvedValue(result()) })
afterEach(() => { wrappers.forEach(wrapper => wrapper.unmount()); wrappers.length = 0 })

describe('Anthropic account monitoring', () => {
  it('loads account scope and renders a mismatch even with HTTP 200', async () => {
    const wrapper = render(); await flushPromises()
    expect(query.mock.calls[0][0]).toMatchObject({ account_id: 11, only_mismatch: false, page: 1 })
    expect(wrapper.get('[data-testid="request-123"]').text()).toContain('issue_entrypoint_mismatch')
    expect(wrapper.text()).toContain('200')
    await wrapper.get('[data-testid="mismatch-only"]').setValue(true); await flushPromises()
    expect(query.mock.calls[1][0]).toMatchObject({ account_id: 11, only_mismatch: true, page: 1 })
  })
  it('discards a stale response after changing accounts', async () => {
    let resolve!: (value: AnthropicRequestList) => void
    query.mockReturnValueOnce(new Promise<AnthropicRequestList>(done => { resolve = done }))
    const wrapper = render()
    await wrapper.get('[data-testid="account"]').setValue('12')
    resolve(result()); await flushPromises()
    expect(wrapper.find('[data-testid="request-123"]').exists()).toBe(false)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(query.mock.calls[1][0].account_id).toBe(12)
  })
  it('shows no records and disabled collection without asserting consistency', async () => {
    const empty = result(); empty.records = []; empty.collection_enabled = false
    query.mockResolvedValue(empty)
    const wrapper = render(); await flushPromises()
    expect(wrapper.text()).toContain('inactive'); expect(wrapper.text()).toContain('empty')
  })
  it('displays failed ingestion and query failures', async () => {
    const failed = result(); failed.sink_health.write_failed_count = 9; query.mockResolvedValueOnce(failed)
    const wrapper = render(); await flushPromises(); expect(wrapper.text()).toContain('sinkWarning')
    query.mockRejectedValueOnce(new Error('failure')); await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('loadFailed'); expect(wrapper.find('[data-testid="request-123"]').exists()).toBe(false)
  })
  it('compares missing fields, false and zero distinctly', () => {
    const rows = comparisonFields(audit, { ...audit, parameters: { temperature: 0 }, cc_entrypoint: 'cli' })
    expect(rows.find(row => row.key === 'parameters.stream')).toMatchObject({ values: [false, undefined], different: true })
    expect(rows.find(row => row.key === 'parameters.temperature')).toMatchObject({ values: [undefined, 0], different: true })
    expect(rows.find(row => row.key === 'cc_entrypoint')?.different).toBe(true)
  })
})
