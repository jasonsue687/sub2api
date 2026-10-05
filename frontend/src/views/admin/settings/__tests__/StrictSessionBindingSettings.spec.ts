import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import StrictSessionBindingSettings from '../StrictSessionBindingSettings.vue'
import {
  strictSessionBindingFormDefaults,
  strictSessionBindingUpdatePayload,
  type StrictSessionBindingForm,
} from '../strictSessionBindingForm'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('StrictSessionBindingSettings', () => {
  it('emits editable session settings without mutating the parent form', async () => {
    const initial = Object.freeze({ ...strictSessionBindingFormDefaults })
    const wrapper = mount(StrictSessionBindingSettings, {
      props: { form: initial },
    })
    const latestForm = () => {
      const events = wrapper.emitted('update:form')!
      return events[events.length - 1][0] as StrictSessionBindingForm
    }

    await wrapper.get('input[type="text"]').setValue('X-Conversation-Id')
    expect(initial.strict_session_session_header).toBe('X-Session-Id')
    expect(latestForm().strict_session_session_header).toBe('X-Conversation-Id')
    await wrapper.setProps({ form: latestForm() })

    await wrapper.findAll('[role="switch"]')[0].trigger('click')
    expect(initial.strict_session_binding_enabled).toBe(true)
    expect(latestForm().strict_session_binding_enabled).toBe(false)
    await wrapper.setProps({ form: latestForm() })

    await wrapper.get('input[type="number"][min="-1"]').setValue('2')
    const payload = strictSessionBindingUpdatePayload(latestForm())
    expect(payload.strict_session_same_account_retry_limit).toBe(2)
    expect(payload.strict_session_binding_enabled).toBe(false)
    expect(payload.strict_session_session_header).toBe('X-Conversation-Id')
    expect(Object.keys(payload)).toHaveLength(3)
    expect(wrapper.find('select').exists()).toBe(false)
    expect(wrapper.find('input[type="password"]').exists()).toBe(false)
    expect(payload).not.toHaveProperty('strict_session_end_user_header')
    expect(payload).not.toHaveProperty('strict_session_end_user_header_trusted')
  })
})
