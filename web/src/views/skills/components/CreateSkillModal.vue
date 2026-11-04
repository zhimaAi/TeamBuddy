<template>
  <a-modal
    :open="open"
    :title="t('skills.createTitle')"
    width="730px"
    centered
    class="create-skill-modal"
    :confirm-loading="saving"
    :closable="!saving"
    :keyboard="!saving"
    :mask-closable="!saving"
    :cancel-button-props="{ disabled: saving }"
    :ok-text="t('skills.confirm')"
    :cancel-text="t('skills.cancel')"
    @cancel="close"
    @ok="save"
  >
    <a-form ref="formRef" :model="form" :rules="rules" layout="vertical" :disabled="saving">
      <a-form-item :label="t('skills.name')" name="name">
        <a-input v-model:value="form.name" :placeholder="t('skills.namePlaceholder')" :maxlength="100" autofocus />
      </a-form-item>
      <a-form-item :label="t('skills.icon')">
        <div class="icon-options">
          <button type="button" :disabled="saving" :class="{ selected: form.icon === AUTO_SKILL_ICON }" :aria-label="t('skills.autoIcon')" :aria-pressed="form.icon === AUTO_SKILL_ICON" @click="form.icon = AUTO_SKILL_ICON">
            <SkillIcon :icon="AUTO_SKILL_ICON" :name="form.name" />
          </button>
          <button v-for="(preset, index) in SKILL_ICON_PRESETS" :key="preset.value" type="button" :disabled="saving" :class="{ selected: form.icon === preset.value }" :aria-label="t('skills.presetIcon', { index: index + 1 })" :aria-pressed="form.icon === preset.value" @click="form.icon = preset.value">
            <SkillIcon :icon="preset.value" :name="form.name" />
          </button>
        </div>
        <p class="hint">{{ t('skills.iconHint') }}</p>
      </a-form-item>
      <a-form-item :label="t('skills.description')" name="description">
        <a-textarea v-model:value="form.description" :placeholder="t('skills.descriptionPlaceholder')" :maxlength="10000" :auto-size="{ minRows: 2, maxRows: 5 }" />
      </a-form-item>
      <a-form-item :label="t('skills.sharer')" name="sharer">
        <a-input v-model:value="form.sharer" :placeholder="t('skills.sharerPlaceholder')" :maxlength="100" />
      </a-form-item>
      <a-form-item :label="t('skills.sourceURL')" name="source_url">
        <a-input v-model:value="form.source_url" :placeholder="t('skills.sourcePlaceholder')" :maxlength="2048" />
        <p class="hint">{{ t('skills.sourceHint') }}</p>
      </a-form-item>
      <a-form-item :label="t('skills.source')" name="source" required>
        <a-radio-group v-model:value="form.source" class="source-options" :aria-label="t('skills.source')" :disabled="saving">
          <a-radio-button v-for="option in sourceOptions" :key="option.value" :value="option.value">
            <span class="source-dot" :style="{ backgroundColor: option.color }" aria-hidden="true" />
            {{ skillSourceLabel(option.value) }}
          </a-radio-button>
        </a-radio-group>
      </a-form-item>
    </a-form>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { message, type FormInstance, type FormProps } from 'ant-design-vue'
import type { RuleObject } from 'ant-design-vue/es/form/interface'
import { useAppI18n } from '@/i18n'
import type { CreateSkillInput, SkillSource } from '@/api/skills'
import { THEME_COLORS } from '@/config/theme'
import { AUTO_SKILL_ICON, SKILL_ICON_PRESETS, safeSkillURL } from '@/views/skills/icons'
import { useSkillStore } from '@/stores/skill'
import { detectSkillSource, skillSourceLabel } from '@/views/skills/source'
import SkillIcon from './SkillIcon.vue'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; saved: [] }>()
const { t } = useAppI18n()
const store = useSkillStore()
const formRef = ref<FormInstance>()
const saving = ref(false)
const sourceOptions: { value: SkillSource; color: string }[] = [
  { value: 'github', color: THEME_COLORS.textSecondary },
  { value: 'skillhub', color: THEME_COLORS.primary },
  { value: 'clawhub', color: '#722ED1' },
]
const form = reactive<CreateSkillInput>({ name: '', icon: AUTO_SKILL_ICON, description: '', sharer: '', source_url: '', source: 'github' })
watch(() => form.source_url, value => { form.source = detectSkillSource(value) }, { flush: 'sync' })
const rules = computed<FormProps['rules']>(() => ({
  name: [{ required: true, whitespace: true, message: t('skills.namePlaceholder'), trigger: 'blur' }],
  description: [{ required: true, whitespace: true, message: t('skills.descriptionPlaceholder'), trigger: 'blur' }],
  sharer: [{ required: true, whitespace: true, message: t('skills.sharerPlaceholder'), trigger: 'blur' }],
  source_url: [{ required: true, validator: async (_rule: RuleObject, value: string) => {
    if (!safeSkillURL(value.trim())) throw new Error(t('skills.invalidURL'))
  }, trigger: 'blur' }],
}))
watch(() => props.open, open => {
  if (!open) return
  Object.assign(form, { name: '', icon: AUTO_SKILL_ICON, description: '', sharer: '', source_url: '', source: 'github' })
  formRef.value?.clearValidate()
})
function close() { if (!saving.value) emit('update:open', false) }
async function save() {
  if (saving.value) return
  saving.value = true
  try {
    try { await formRef.value?.validate() } catch { return }
    await store.create({ ...form, name: form.name.trim(), sharer: form.sharer.trim(), source_url: form.source_url.trim() })
    message.success(t('skills.created'))
    emit('update:open', false)
    emit('saved')
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('skills.createFailed'))
  } finally { saving.value = false }
}
</script>

<style scoped>
.source-options { display: flex; flex-wrap: wrap; gap: 12px; }
.source-options :deep(.ant-radio-button-wrapper) { height: 40px; line-height: 38px; border-inline-start-width: 1px; border-radius: 6px; }
.source-options :deep(.ant-radio-button-wrapper::before) { display: none; }
.source-dot { display: inline-block; width: 8px; height: 8px; margin-inline-end: 8px; border-radius: 50%; }
.icon-options { display: flex; flex-wrap: wrap; gap: 10px; }
.icon-options button { padding: 2px; border: 2px solid transparent; background: transparent; border-radius: 16px; cursor: pointer; }
.icon-options button.selected { border-color: #3157E2; box-shadow: 0 0 0 2px rgba(49, 87, 226, .15); }
.icon-options button:focus-visible { outline: 2px solid #3157E2; outline-offset: 2px; }
.icon-options button:disabled { cursor: default; opacity: .6; }
.hint { margin: 8px 0 0; color: #8C8C8C; font-size: 12px; line-height: 20px; }
</style>

<style>
.create-skill-modal .ant-modal-content { padding: 0; border-radius: 24px; overflow: hidden; }
.create-skill-modal .ant-modal-header { padding: 24px; margin: 0; border-bottom: 1px solid #F0F0F0; }
.create-skill-modal .ant-modal-title { font-size: 24px; font-weight: 600; line-height: 32px; }
.create-skill-modal .ant-modal-body { padding: 16px 48px 24px; max-height: calc(100dvh - 230px); overflow-y: auto; }
.create-skill-modal .ant-modal-footer { margin: 0; padding: 16px 48px; border-top: 1px solid #D9D9D9; }
.create-skill-modal .ant-form-item { margin-bottom: 20px; }
.create-skill-modal .ant-form-item:last-child { margin-bottom: 0; }
.create-skill-modal .ant-form-item-label { padding-bottom: 8px; }
.create-skill-modal .ant-form-item-label > label { height: 24px; color: #262626; font-size: 16px; font-weight: 600; line-height: 24px; }
.create-skill-modal .ant-input { padding: 8px 12px; border-radius: 12px; font-size: 14px; line-height: 22px; }
.create-skill-modal input.ant-input { min-height: 40px; }
.create-skill-modal textarea.ant-input { resize: none; }
@media (max-width: 640px) { .create-skill-modal .ant-modal-header { padding: 20px 24px; } .create-skill-modal .ant-modal-title { font-size: 20px; line-height: 28px; } .create-skill-modal .ant-modal-body, .create-skill-modal .ant-modal-footer { padding-left: 24px; padding-right: 24px; } }
</style>
