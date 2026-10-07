import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import FingerprintBindingModal from '../FingerprintBindingModal.vue'
import type { FingerprintRecord } from '@/api/admin/accountFingerprints'

const api = vi.hoisted(() => ({ bindFingerprint: vi.fn(), getFingerprintBinding: vi.fn(), listFingerprints: vi.fn(), listFingerprintAccounts: vi.fn() }))
vi.mock('@/api/admin/accountFingerprints', () => api)
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
const dialog = defineComponent({ props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' })
const profile = (id: number): FingerprintRecord => ({
  id, source: 'cache', source_account_id: 14, client_id_origin: 'cache', request_count: 0,
  incoming_headers: {}, first_seen_at: '', last_seen_at: '', bound_accounts: [],
  fingerprint: { ClientID: 'device', UserAgent: `claude-cli/2.1.${id}`, StainlessOS: 'Linux', StainlessArch: 'x64', StainlessLang: 'js', StainlessPackageVersion: '0.1.0', StainlessRuntime: 'node', StainlessRuntimeVersion: 'v24' }
})
function render(props: { account?: { id: number; name: string }; fingerprint?: FingerprintRecord }) {
  return mount(FingerprintBindingModal, { props: { show: true, ...props }, global: { stubs: { BaseDialog: dialog } } })
}

describe('FingerprintBindingModal', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    api.getFingerprintBinding.mockResolvedValue({ fingerprint: profile(7), applied: false })
    api.listFingerprints.mockResolvedValue({ items: [profile(8)], total: 1 })
    api.listFingerprintAccounts.mockResolvedValue({ items: [{ id: 14, name: 'Claude', fingerprint_id: 7 }], total: 1 })
    api.bindFingerprint.mockResolvedValue({ applied: false })
  })

  it('loads the existing binding outside the current results and saves an account-page selection', async () => {
    const wrapper = render({ account: { id: 14, name: 'Claude' } })
    await flushPromises()
    expect(api.getFingerprintBinding).toHaveBeenCalledWith(14)
    expect(wrapper.get('[data-testid="binding-fingerprint"]').element).toHaveProperty('value', '7')
    expect(wrapper.text()).toContain('admin.fingerprints.stagedNotice')
    await wrapper.get('[data-testid="binding-fingerprint"]').setValue(8)
    await wrapper.get('[data-testid="save-binding"]').trigger('click')
    await flushPromises()
    expect(api.bindFingerprint).toHaveBeenCalledWith(14, 8)
    expect(wrapper.emitted('saved')).toHaveLength(1)
    wrapper.unmount()
  })

  it('uses the same binding API when choosing an account on the fingerprint page', async () => {
    const wrapper = render({ fingerprint: profile(8) })
    await flushPromises()
    expect(wrapper.get('[data-testid="save-binding"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-testid="binding-account"]').setValue(14)
    await flushPromises()
    expect(wrapper.text()).toContain('#7')
    await wrapper.get('[data-testid="save-binding"]').trigger('click')
    await flushPromises()
    expect(api.bindFingerprint).toHaveBeenCalledWith(14, 8)
    wrapper.unmount()
  })

  it('requires an explicit unbind choice and blocks save if the existing binding cannot be read', async () => {
    const wrapper = render({ account: { id: 14, name: 'Claude' } })
    await flushPromises()
    const select = wrapper.get('[data-testid="binding-fingerprint"]')
    await select.setValue((select.element as HTMLSelectElement).options[0].value)
    await wrapper.get('[data-testid="save-binding"]').trigger('click')
    await flushPromises()
    expect(api.bindFingerprint).toHaveBeenCalledWith(14, null)
    wrapper.unmount()
    api.bindFingerprint.mockClear()
    api.getFingerprintBinding.mockRejectedValue(new Error('unavailable'))
    const failed = render({ account: { id: 14, name: 'Claude' } })
    await flushPromises()
    expect(failed.find('[role="alert"]').exists()).toBe(true)
    expect(failed.get('[data-testid="save-binding"]').attributes('disabled')).toBeDefined()
    expect(api.bindFingerprint).not.toHaveBeenCalled()
    failed.unmount()
  })

  it('rereads the binding when reopening the same account', async () => {
    const wrapper = render({ account: { id: 14, name: 'Claude' } })
    await flushPromises()
    await wrapper.setProps({ show: false })
    await flushPromises()
    api.getFingerprintBinding.mockResolvedValue({ fingerprint: profile(8), applied: false })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(api.getFingerprintBinding).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-testid="binding-fingerprint"]').element).toHaveProperty('value', '8')
    wrapper.unmount()
  })

  it('keeps the selected identity and account visible when search results change', async () => {
    const wrapper = render({ account: { id: 14, name: 'Claude' } })
    await flushPromises()
    await wrapper.get('[data-testid="binding-fingerprint"]').setValue(8)
    api.listFingerprints.mockResolvedValue({ items: [profile(9)], total: 1 })
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(wrapper.get('[data-testid="binding-fingerprint"]').element).toHaveProperty('value', '8')
    await wrapper.get('[data-testid="save-binding"]').trigger('click')
    await flushPromises()
    expect(api.bindFingerprint).toHaveBeenLastCalledWith(14, 8)
    wrapper.unmount()

    const fromFingerprint = render({ fingerprint: profile(8) })
    await flushPromises()
    await fromFingerprint.get('[data-testid="binding-account"]').setValue(14)
    await flushPromises()
    api.listFingerprintAccounts.mockResolvedValue({ items: [{ id: 15, name: 'Other', fingerprint_id: null }], total: 1 })
    await fromFingerprint.get('form').trigger('submit')
    await flushPromises()
    expect(fromFingerprint.get('[data-testid="binding-account"]').element).toHaveProperty('value', '14')
    await fromFingerprint.get('[data-testid="save-binding"]').trigger('click')
    await flushPromises()
    expect(api.bindFingerprint).toHaveBeenLastCalledWith(14, 8)
    fromFingerprint.unmount()
  })
})
