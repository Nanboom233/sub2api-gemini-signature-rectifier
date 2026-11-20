import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import SettingsView from '@/views/admin/SettingsView.vue'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

const mocks = vi.hoisted(() => ({
  getRectifierSettings: vi.fn(),
  updateRectifierSettings: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api', () => ({
  adminAPI: {
    settings: {
      getSettings: vi.fn().mockResolvedValue({ backend_mode_enabled: false }),
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getAdminApiKey: vi.fn().mockResolvedValue({ exists: false, masked_key: '' }),
      getOverloadCooldownSettings: vi.fn().mockResolvedValue({}),
      getRateLimit429CooldownSettings: vi.fn().mockResolvedValue({}),
      getPanelRateLimitSettings: vi.fn().mockResolvedValue({}),
      getStreamTimeoutSettings: vi.fn().mockResolvedValue({}),
      getBetaPolicySettings: vi.fn().mockResolvedValue({ rules: [] }),
      getRectifierSettings: mocks.getRectifierSettings,
      updateRectifierSettings: mocks.updateRectifierSettings,
    },
    accounts: {
      getUpstreamBillingProbeSettings: vi.fn().mockResolvedValue({ enabled: false }),
      getOllamaCloudUsageSettings: vi.fn().mockResolvedValue({ enabled: false }),
      getOpenCodeGoUsageSettings: vi.fn().mockResolvedValue({ enabled: false }),
    },
    groups: { getAll: vi.fn().mockResolvedValue([]) },
    proxies: { list: vi.fn().mockResolvedValue({ items: [] }) },
    payment: { getProviders: vi.fn().mockResolvedValue({ data: [] }) },
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError: mocks.showError,
    showSuccess: mocks.showSuccess,
    fetchPublicSettings: vi.fn(),
  }),
}))

vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({ fetch: vi.fn() }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() }),
}))

describe('native Gemini rectifier settings', () => {
  beforeEach(() => vi.clearAllMocks())

  for (const locale of ['en', 'zh'] as const) {
    it(`saves both boolean states and preserves siblings in ${locale}`, async () => {
      let saved = {
        enabled: true,
        thinking_signature_enabled: true,
        gemini_native_signature_enabled: false,
        thinking_budget_enabled: false,
        apikey_signature_enabled: true,
        apikey_signature_patterns: ['synthetic-pattern'],
      }
      mocks.getRectifierSettings.mockImplementation(async () => ({ ...saved }))
      mocks.updateRectifierSettings.mockImplementation(async (payload) => {
        saved = { ...payload }
        return { ...saved }
      })
      const mountView = () => shallowMount(SettingsView, {
        global: {
          plugins: [createI18n({ legacy: false, locale, messages: { en, zh } })],
          stubs: { AppLayout: { template: '<div><slot /></div>' }, Toggle: false, RouterLink: true },
        },
      })
      for (const enabled of [true, false]) {
        const wrapper = mountView()
        try {
          await flushPromises()
          await wrapper.get('#settings-tab-gateway').trigger('click')
          const label = locale === 'en'
            ? 'Gemini Native Signature Rectifier'
            : 'Gemini 原生签名整流'
          const hint = locale === 'en'
            ? 'Fix rejected thought signatures and retry native Gemini requests once. Configured error policies take priority.'
            : '原生 Gemini 请求遇到思考签名错误时，修正后重试一次。已配置的错误策略优先。'
          expect(wrapper.get('label[for="gemini-native-signature"]').text()).toBe(label)
          expect(wrapper.get('#gemini-native-signature-hint').text()).toBe(hint)
          expect(wrapper.get('#gemini-native-signature').attributes()).toMatchObject({
            role: 'switch',
            'aria-checked': String(!enabled),
            'aria-label': label,
            'aria-describedby': 'gemini-native-signature-hint',
          })
          await wrapper.get('#gemini-native-signature').trigger('click')
          const card = wrapper.findAll('.card').find(candidate => candidate.find('#gemini-native-signature').exists())!
          await card.get('button.btn-primary').trigger('click')
          await flushPromises()
          expect(mocks.updateRectifierSettings).toHaveBeenLastCalledWith({
            enabled: true,
            thinking_signature_enabled: true,
            gemini_native_signature_enabled: enabled,
            thinking_budget_enabled: false,
            apikey_signature_enabled: true,
            apikey_signature_patterns: ['synthetic-pattern'],
          })
          expect(wrapper.get('#gemini-native-signature').attributes('aria-checked')).toBe(String(enabled))
        } finally {
          wrapper.unmount()
        }
      }
      const wrapper = mountView()
      try {
        await flushPromises()
        await wrapper.get('#settings-tab-gateway').trigger('click')
        expect(wrapper.get('#gemini-native-signature').attributes('aria-checked')).toBe('false')
        const card = wrapper.findAll('.card').find(candidate => candidate.find('#gemini-native-signature').exists())!
        expect(card.findAll('[role="switch"]')).toHaveLength(5)
        await card.findAll('[role="switch"]')[0].trigger('click')
        expect(card.findAll('[role="switch"]')).toHaveLength(1)
        expect(card.find('#gemini-native-signature').exists()).toBe(false)
        expect(mocks.showError).not.toHaveBeenCalled()
      } finally {
        wrapper.unmount()
      }
    })
  }
})
