import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import { applyWarmupSettings, readWarmupSettings } from '../warmupSettings'
import WarmupSettingsFields from '../WarmupSettingsFields.vue'

describe('warmup settings', () => {
  it('keeps existing switches in mock mode and does not return credentials', () => {
    const settings = readWarmupSettings({ intercept_warmup_requests: true, warmup_api_key: 'secret' })
    expect(settings.mode).toBe('mock')
    expect(settings.apiKey).toBe('')
    expect(settings.timeoutSeconds).toBe(30)
  })
  it('preserves an omitted stored key and only sends a replacement when entered', () => {
    const settings = readWarmupSettings({ warmup_mode: 'forward', warmup_model: 'deepseek-chat' }, { has_warmup_api_key: true })
    const credentials: Record<string, unknown> = {}
    applyWarmupSettings(credentials, settings)
    expect(credentials).not.toHaveProperty('warmup_api_key')
    expect(settings.hasAPIKey).toBe(true)
    expect(credentials.warmup_model).toBe('deepseek-chat')
    settings.apiKey = ' replacement '
    applyWarmupSettings(credentials, settings)
    expect(credentials.warmup_api_key).toBe('replacement')
  })
  it('shows external settings only for forwarding and masks the key input', async () => {
    const settings = readWarmupSettings({}, { has_warmup_api_key: true })
    const wrapper = mount(WarmupSettingsFields, {
      props: { modelValue: settings },
      global: { plugins: [createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false })] }
    })
    expect(wrapper.find('input[type="password"]').exists()).toBe(false)
    await wrapper.find('select').setValue('forward')
    const updated = wrapper.emitted('update:modelValue')![0][0] as typeof settings
    expect(updated.mode).toBe('forward')
    await wrapper.setProps({ modelValue: updated })
    expect(wrapper.find('input[type="password"]').attributes('required')).toBeUndefined()
    expect(wrapper.find('input[type="password"]').element.getAttribute('autocomplete')).toBe('new-password')
    expect(wrapper.find('input[type="number"]').attributes('max')).toBe('120')
  })
})
