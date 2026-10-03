import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import View from '../AnthropicMockView.vue'

const api = vi.hoisted(() => ({
  getAnthropicMockStatus: vi.fn(),
  updateAnthropicMock: vi.fn(),
  listAnthropicMockSamples: vi.fn(),
  listAnthropicMockOutbound: vi.fn(),
  runAnthropicMockTests: vi.fn()
}))
vi.mock('@/api/admin/anthropicMock', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))

const status = (enabled = false) => ({
  enabled, source: 'config', hook_armed: true, replay_ready: true, store_ready: true,
  inbound_dropped: 0, outbound_dropped: 0, inbound_queued: 0, outbound_queued: 0, sample_count: 1
})
const wrappers: ReturnType<typeof mount>[] = []
const render = () => { const wrapper = mount(View); wrappers.push(wrapper); return wrapper }

beforeEach(() => {
  vi.clearAllMocks()
  api.getAnthropicMockStatus.mockResolvedValue(status())
  api.listAnthropicMockSamples.mockResolvedValue({ items: [{ id: 7, created_at: '2026-10-03T00:00:00Z', dedupe_key: 'k', method: 'POST', path: '/v1/messages', client_label: 'claude-cli/test', content_type: 'application/json', headers: {}, body_preview: '{"model":"claude"}', body_bytes: 18, body_sha256: 'abc' }], total: 1, page: 1, page_size: 20, pages: 1 })
  api.listAnthropicMockOutbound.mockResolvedValue({ items: [{ id: 3, created_at: '2026-10-03T00:00:00Z', account_id: 11, method: 'POST', url: 'https://api.anthropic.com/v1/messages', headers: { Authorization: '[redacted]' }, body_preview: '{}', body_bytes: 2, body_truncated: false, mock_reason: 'switch', status_code: 200 }], total: 1, page: 1, page_size: 20, pages: 1 })
  api.updateAnthropicMock.mockResolvedValue(status(true))
  api.runAnthropicMockTests.mockResolvedValue({ total: 1, completed: 1, failed: 0, mock_guaranteed: true, results: [] })
})
afterEach(() => { wrappers.forEach(wrapper => wrapper.unmount()); wrappers.length = 0 })

describe('Anthropic mock admin page', () => {
  it('shows the corpus and toggles the switch', async () => {
    const wrapper = render()
    await flushPromises()
    expect(wrapper.get('[data-testid="sample-7"]').text()).toContain('/v1/messages')
    expect(wrapper.get('[data-testid="outbound-3"]').text()).toContain('switch')
    await wrapper.get('[data-testid="mock-switch"]').trigger('click')
    await flushPromises()
    expect(api.updateAnthropicMock).toHaveBeenCalledWith(true)
  })

  it('replays through the run endpoint with the typed key', async () => {
    const wrapper = render()
    await flushPromises()
    await wrapper.get('[data-testid="run-api-key"]').setValue('sk-test')
    await wrapper.get('[data-testid="run-tests"]').trigger('click')
    await flushPromises()
    expect(api.runAnthropicMockTests).toHaveBeenCalledWith('sk-test')
    expect(wrapper.get('[data-testid="run-summary"]').text()).toContain('admin.anthropicMock.runSummary')
  })
})
