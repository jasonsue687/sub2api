import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { AnthropicCapture, AnthropicSessionDetail } from '@/api/admin/anthropicRequests'
import View from '../AnthropicRequestDetailView.vue'

const { getSession, push, writeText } = vi.hoisted(() => ({ getSession: vi.fn(), push: vi.fn(), writeText: vi.fn() }))
vi.mock('@/api/admin/anthropicRequests', () => ({ getAnthropicSession: getSession }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { clientRequestId: 'c7f3a1e2abcd' } }),
  useRouter: () => ({ push })
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string, params?: Record<string, string | number>) => params ? key + JSON.stringify(params) : key }) }))
vi.mock('@/components/layout/AppLayout.vue', () => ({ default: { template: '<main><slot /></main>' } }))

const capture = (patch: Partial<AnthropicCapture> = {}): AnthropicCapture => ({
  direction: 'outbound', client_request_id: 'c7f3a1e2abcd', request_id: 'req-1', account_id: 2, account_name: 'oauth-acc-02',
  user_id: 3, api_key_id: 12, api_key_name: '研发共享', username: 'dev-alice', endpoint: '/v1/messages', client_path: '/v1/messages',
  model: 'claude-sonnet-4-5-20250929', stream: true, attempt_seq: 1, retry_reason: 'initial', account_switch_count: 0, attempt_count: 1,
  status: 200, error_class: '', upstream_request_id: 'req_up_1', duration_ms: 2410, headers_ms: 710,
  headers: [{ name: 'x-api-key', value: '***' }],
  body: { model: 'claude-sonnet-4-5-20250929', max_tokens: 100, messages: [{ content: { _redacted: true, len: 8, sha256: 'abcdef99' } }] },
  body_state: 'parsed', consistency: 'matched', truncated: false, original_bytes: 40, created_at: '2026-10-03T13:14:07Z',
  ...patch
})
const detail = (patch: Partial<AnthropicSessionDetail> = {}): AnthropicSessionDetail => ({
  session: {
    client_request_id: 'c7f3a1e2abcd', request_id: 'req-1', created_at: '2026-10-03T13:14:07Z', api_key_id: 12, api_key_name: '研发共享',
    username: 'dev-alice', user_id: 3, endpoint: '/v1/messages', inbound_model: 'claude-sonnet-4-5', outbound_model: 'claude-sonnet-4-5-20250929',
    stream: true, status: 200, account_id: 2, account_name: 'oauth-acc-02', attempt_count: 1, duration_ms: 2410,
    has_inbound: true, has_outbound: true, error_class: '', consistency: 'matched', truncated: false
  },
  inbound: capture({ direction: 'inbound', model: 'claude-sonnet-4-5', body: { model: 'claude-sonnet-4-5', max_tokens: 100, messages: [{ content: { _redacted: true, len: 8, sha256: 'abcdef99' } }] } }),
  attempts: [capture()],
  ...patch
})
const wrappers: ReturnType<typeof mount>[] = []
const render = () => { const wrapper = mount(View); wrappers.push(wrapper); return wrapper }
beforeEach(() => {
  vi.clearAllMocks()
  getSession.mockResolvedValue(detail())
  Object.assign(navigator, { clipboard: { writeText } })
  writeText.mockResolvedValue(undefined)
})
afterEach(() => { wrappers.splice(0).forEach(wrapper => wrapper.unmount()) })

describe('Anthropic request detail', () => {
  it('places inbound and outbound side by side and copies stored JSON', async () => {
    const wrapper = render(); await flushPromises()
    expect(getSession).toHaveBeenCalledWith('c7f3a1e2abcd')
    expect(wrapper.get('[data-testid="inbound-pane"]').text()).toContain('inboundTitle')
    expect(wrapper.get('[data-testid="outbound-pane"]').text()).toContain('outboundTitle')
    const changed = wrapper.get('[data-testid="inbound-pane"]').findAll('[data-testid="diff-line"]').filter(line => line.attributes('data-kind') === 'chg')
    expect(changed.length).toBeGreaterThan(0)
    await wrapper.get('[data-testid="copy-inbound"]').trigger('click')
    expect(writeText.mock.calls[0][0]).toContain('claude-sonnet-4-5')
    expect(writeText.mock.calls[0][0]).toContain('"_redacted": true')
    expect(writeText.mock.calls[0][0]).not.toContain('已省略')
  })

  it('hides unchanged lines when only differences are shown', async () => {
    const wrapper = render(); await flushPromises()
    expect(wrapper.get('[data-testid="inbound-pane"]').find('[data-kind="same"]').exists()).toBe(true)
    await wrapper.get('[data-testid="only-diff"]').trigger('click')
    const kinds = wrapper.get('[data-testid="inbound-pane"]').findAll('[data-testid="diff-line"]').map(line => line.attributes('data-kind'))
    expect(kinds).not.toContain('same')
    expect(kinds).toContain('chg')
  })

  it('selects the latest successful attempt and can switch retries', async () => {
    getSession.mockResolvedValue(detail({
      session: { ...detail().session, attempt_count: 3 },
      attempts: [
        capture({ attempt_seq: 1, status: 429, retry_reason: 'initial', account_name: 'oauth-acc-01', upstream_request_id: 'up-1' }),
        capture({ attempt_seq: 2, status: 529, retry_reason: 'account_switch', account_name: 'oauth-acc-02', upstream_request_id: 'up-2' }),
        capture({ attempt_seq: 3, status: 200, retry_reason: 'account_switch', account_name: 'oauth-acc-05', upstream_request_id: 'up-3', body: { model: 'claude-opus' } })
      ]
    }))
    const wrapper = render(); await flushPromises()
    expect(wrapper.get('[data-testid="attempt-chips"]').text()).toContain('attemptTotal')
    expect(wrapper.get('[data-testid="attempt-3"]').classes()).toContain('bg-primary-600')
    expect(wrapper.get('[data-testid="outbound-pane"]').text()).toContain('claude-opus')
    await wrapper.get('[data-testid="attempt-1"]').trigger('click')
    expect(wrapper.get('[data-testid="attempt-1"]').classes()).toContain('bg-primary-600')
    expect(wrapper.get('[data-testid="attempt-chips"]').text()).toContain('reason_initial')
  })

  it('displays selected outbound evidence independently of inbound and other attempts', async () => {
    getSession.mockResolvedValue(detail({
      session: { ...detail().session, attempt_count: 2, consistency: 'mismatch' },
      inbound: capture({ direction: 'inbound', consistency: 'mismatch', summary: { issues: ['version_mismatch'] } }),
      attempts: [
        capture({ attempt_seq: 1, status: 429, consistency: 'mismatch', summary: { issues: ['entrypoint_mismatch'], cc_entrypoint: 'local-agent', cc_version: '2.1.283' } }),
        capture({ attempt_seq: 2, consistency: 'matched', headers: [{ name: 'User-Agent', value: 'claude-cli/2.1.283 (external, cli)' }], summary: { issues: [], cc_entrypoint: 'cli', cc_version: '2.1.283' } })
      ]
    }))
    const wrapper = render(); await flushPromises()
    expect(wrapper.get('[data-testid="all-outbound-check"] [data-consistency]').attributes('data-consistency')).toBe('mismatch')
    expect(wrapper.get('[data-testid="selected-outbound-check"] [data-consistency]').attributes('data-consistency')).toBe('matched')
    expect(wrapper.get('[data-testid="outbound-ua"]').text()).toBe('claude-cli/2.1.283 (external, cli)')
    expect(wrapper.get('[data-testid="outbound-check"]').text()).not.toContain('issue_version_mismatch')
    await wrapper.get('[data-testid="attempt-1"]').trigger('click')
    expect(wrapper.get('[data-testid="selected-outbound-check"]').text()).toContain('issue_entrypoint_mismatch')
    expect(wrapper.get('[data-testid="selected-outbound-check"] [data-consistency]').attributes('data-consistency')).toBe('mismatch')
    await wrapper.get('[data-testid="outbound-audit"]').trigger('click')
    expect(push).toHaveBeenCalledWith({ name: 'AdminAnthropicOutboundAudit', query: { account_id: '2' } })
  })

  it('does not treat a missing inbound record as a field-by-field deletion', async () => {
    getSession.mockResolvedValue(detail({ inbound: null, session: { ...detail().session, has_inbound: false } }))
    const wrapper = render(); await flushPromises()
    expect(wrapper.get('[data-testid="no-inbound-banner"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="outbound-pane"]').find('[data-kind="add"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="outbound-pane"]').find('[data-kind="del"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="only-diff"]').attributes('disabled')).toBeDefined()
  })

  it('shows inbound alone when nothing was sent upstream', async () => {
    getSession.mockResolvedValue(detail({ attempts: [], session: { ...detail().session, has_outbound: false, attempt_count: 0 } }))
    const wrapper = render(); await flushPromises()
    expect(wrapper.get('[data-testid="no-outbound-pane"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="inbound-pane"]').text()).toContain('claude-sonnet-4-5')
    expect(wrapper.get('[data-testid="copy-outbound"]').attributes('disabled')).toBeDefined()
  })
})
