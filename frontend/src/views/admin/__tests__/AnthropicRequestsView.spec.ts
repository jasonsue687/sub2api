import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { comparisonFields } from '@/utils/anthropicRequests'
import type { AnthropicAudit, AnthropicSessionList, AnthropicSessionRow } from '@/api/admin/anthropicRequests'
import View from '../AnthropicRequestsView.vue'

const { query, push } = vi.hoisted(() => ({ query: vi.fn(), push: vi.fn() }))
vi.mock('@/api/admin/anthropicRequests', () => ({ listAnthropicSessions: query }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }), useRouter: () => ({ push }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))

const row = (patch: Partial<AnthropicSessionRow> = {}): AnthropicSessionRow => ({
  client_request_id: 'c7f3a1e2abcd', request_id: 'req-1', created_at: '2026-10-03T13:14:07Z',
  api_key_id: 12, api_key_name: '研发共享', username: 'dev-alice', user_id: 3,
  endpoint: '/v1/messages', inbound_model: 'claude-sonnet-4-5', outbound_model: 'claude-sonnet-4-5-20250929',
  stream: true, status: 200, account_id: 2, account_name: 'oauth-acc-02', attempt_count: 1,
  duration_ms: 2410, has_inbound: true, has_outbound: true, error_class: '', consistency: 'matched', truncated: false,
  ...patch
})
const result = (records: AnthropicSessionRow[] = [row()]): AnthropicSessionList => ({
  summary: { inbound_requests: 1284, outbound_attempts: 1327, multi_attempts: 31, failed_requests: 12 },
  records, total: records.length, page: 1, page_size: 50,
  accounts: [{ id: 2, name: 'oauth-acc-02' }], models: ['claude-sonnet-4-5'],
  start_time: '2026-10-02T13:14:07Z', end_time: '2026-10-03T13:14:07Z', inbound_enabled: true,
  capture_health: { queue_capacity: 512, queue_depth: 0, dropped_count: 0, write_failed_count: 0, written_count: 1 }
})
const wrappers: ReturnType<typeof mount>[] = []
const render = () => { const wrapper = mount(View); wrappers.push(wrapper); return wrapper }
beforeEach(() => { vi.clearAllMocks(); query.mockResolvedValue(result()) })
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })

describe('Anthropic request monitor list', () => {
  it('shows the four summary cards and a single outbound row', async () => {
    const wrapper = render(); await flushPromises()
    expect(wrapper.get('[data-testid="stat-inbound"]').text().replace(/,/g, '')).toBe('1284')
    expect(wrapper.get('[data-testid="stat-outbound"]').text().replace(/,/g, '')).toBe('1327')
    expect(wrapper.get('[data-testid="stat-multi"]').text().replace(/,/g, '')).toBe('31')
    expect(wrapper.get('[data-testid="stat-failed"]').text().replace(/,/g, '')).toBe('12')
    const line = wrapper.get('[data-testid="request-c7f3a1e2abcd"]')
    expect(line.text()).toContain('2.41s')
    expect(line.get('[data-testid="outbound-consistency"] [data-consistency]').attributes('data-consistency')).toBe('matched')
    expect(line.get('[data-testid="status"]').classes()).toContain('bg-emerald-100')
    expect(line.get('[data-stream="true"]').exists()).toBe(true)
    expect(line.get('[data-testid="attempt-count"]').attributes('data-attempts')).toBe('1')
  })

  it('highlights retries and labels missing inbound or outbound', async () => {
    query.mockResolvedValue(result([
      row({ client_request_id: 'multi', attempt_count: 3, status: 200 }),
      row({ client_request_id: 'chat', has_inbound: false, endpoint: '/v1/chat/completions', inbound_model: '' }),
      row({ client_request_id: 'none', has_outbound: false, attempt_count: 0, status: 503, outbound_model: '', account_name: '', duration_ms: 20 })
    ]))
    const wrapper = render(); await flushPromises()
    const multi = wrapper.get('[data-testid="request-multi"]')
    expect(multi.classes()).toContain('bg-amber-50/40')
    expect(multi.get('[data-testid="attempt-count"]').attributes('data-attempts')).toBe('3')
    expect(multi.get('[data-testid="attempt-count"]').classes()).toContain('bg-amber-100')
    expect(wrapper.get('[data-testid="request-chat"]').find('[data-testid="no-inbound"]').exists()).toBe(true)
    const missing = wrapper.get('[data-testid="request-none"]')
    expect(missing.find('[data-testid="no-outbound"]').exists()).toBe(true)
    expect(missing.get('[data-testid="status"]').classes()).toContain('bg-red-100')
    expect(missing.text()).toContain('0.02s')
  })

  it('sends filters for failures, multiple attempts, search, and the 30-day window', async () => {
    const wrapper = render(); await flushPromises()
    await wrapper.get('[data-testid="status-filter"]').setValue('failed')
    await wrapper.get('[data-testid="only-multi"]').trigger('click')
    await wrapper.get('[data-testid="search"]').setValue('c7f3')
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(query.mock.calls.at(-1)![0]).toMatchObject({ only_failed: true, only_multi: true, q: 'c7f3', page: 1 })
    await wrapper.get('[data-testid="time-window"]').setValue('720')
    await flushPromises()
    const params = query.mock.calls.at(-1)![0]
    expect(new Date(params.end_time).getTime() - new Date(params.start_time).getTime()).toBe(30 * 24 * 60 * 60 * 1000)
  })

  it('shows outbound states, applies the outbound filter and opens historical statistics', async () => {
    query.mockResolvedValue(result([
      row({ client_request_id: 'bad', consistency: 'mismatch' }),
      row({ client_request_id: 'unknown', consistency: 'unknown' }),
      row({ client_request_id: 'none', consistency: 'unknown', attempt_count: 0, has_outbound: false })
    ]))
    const wrapper = render(); await flushPromises()
    expect(wrapper.get('[data-testid="request-bad"] [data-consistency]').attributes('data-consistency')).toBe('mismatch')
    expect(wrapper.get('[data-testid="request-unknown"] [data-consistency]').attributes('data-consistency')).toBe('unknown')
    expect(wrapper.get('[data-testid="request-none"] [data-testid="outbound-consistency"]').find('[data-consistency]').exists()).toBe(false)
    await wrapper.get('[data-testid="status-filter"]').setValue('mismatch'); await flushPromises()
    expect(query.mock.calls.at(-1)![0]).toMatchObject({ only_mismatch: true })
    await wrapper.get('[data-testid="outbound-audit"]').trigger('click')
    expect(push).toHaveBeenCalledWith({ name: 'AdminAnthropicOutboundAudit', query: {} })
  })

  it('opens the detail route and ignores a stale response', async () => {
    const wrapper = render(); await flushPromises()
    await wrapper.get('[data-testid="open-c7f3a1e2abcd"]').trigger('click')
    expect(push).toHaveBeenCalledWith({ name: 'AdminAnthropicRequestDetail', params: { clientRequestId: 'c7f3a1e2abcd' } })
    let resolve!: (value: AnthropicSessionList) => void
    query.mockReturnValueOnce(new Promise<AnthropicSessionList>(done => { resolve = done }))
    await wrapper.get('[data-testid="time-window"]').setValue('1')
    query.mockResolvedValueOnce(result([row({ client_request_id: 'fresh' })]))
    await wrapper.get('[data-testid="model-filter"]').setValue('claude-sonnet-4-5')
    resolve(result([row({ client_request_id: 'stale' })]))
    await flushPromises()
    expect(wrapper.find('[data-testid="request-stale"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="request-fresh"]').exists()).toBe(true)
  })

  it('shows an empty list, disabled capture, and query failures', async () => {
    const empty = result([]); empty.inbound_enabled = false
    query.mockResolvedValueOnce(empty)
    const wrapper = render(); await flushPromises()
    expect(wrapper.text()).toContain('inactive')
    expect(wrapper.text()).toContain('empty')
    const dropped = result(); dropped.capture_health.dropped_count = 4
    query.mockResolvedValueOnce(dropped)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.get('[data-testid="sink-warning"]').text()).toContain('sinkWarning')
    query.mockRejectedValueOnce(new Error('failure'))
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.text()).toContain('loadFailed')
  })
})

describe('legacy parameter comparison', () => {
  const audit: AnthropicAudit = {
    schema: 1, attempt_id: 'demo', started_at: '2026-09-27T12:00:00Z', endpoint: '/v1/messages',
    origin: { source: 'gateway', mimic: false, subscription: true }, headers: { 'user-agent': 'claude-cli' },
    parameters: { model: 'claude-sonnet-4-5', stream: false }, body_state: 'parsed', identity_signature: 'demo',
    consistency: 'mismatch', issues: ['entrypoint_mismatch'], status: 200, headers_ms: 400
  }
  it('compares missing fields, false and zero distinctly', () => {
    const rows = comparisonFields(audit, { ...audit, parameters: { temperature: 0 }, cc_entrypoint: 'cli' })
    expect(rows.find(item => item.key === 'parameters.stream')).toMatchObject({ values: [false, undefined], different: true })
    expect(rows.find(item => item.key === 'parameters.temperature')).toMatchObject({ values: [undefined, 0], different: true })
  })
})
