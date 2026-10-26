<template>
  <a-modal
    :open="open"
    :title="t('knowledge.appearanceDialogTitle')"
    :width="480"
    :ok-text="t('knowledge.confirm')"
    :cancel-text="t('knowledge.cancel')"
    :ok-button-props="{ disabled: !valid }"
    @ok="apply"
    @cancel="emit('update:open', false)"
  >
    <div class="appearance-settings">
      <div class="appearance-row">
        <span class="appearance-label">{{ t('knowledge.fontSizeLabel') }}</span>
        <a-slider
          :value="draft.fontSize ?? limits.fontSize.min"
          @update:value="updateFontSize"
          class="appearance-slider"
          :min="limits.fontSize.min"
          :max="limits.fontSize.max"
          :step="limits.fontSize.step"
          :aria-label-for-handle="t('knowledge.fontSizeLabel')"
        />
        <a-input-number
          v-model:value="draft.fontSize"
          class="appearance-number"
          size="small"
          :min="limits.fontSize.min"
          :max="limits.fontSize.max"
          :step="limits.fontSize.step"
          :precision="0"
          :addon-after="t('knowledge.fontSizeUnit')"
          :aria-label="t('knowledge.fontSizeLabel')"
          :status="fontSizeValid ? undefined : 'error'"
          :aria-invalid="!fontSizeValid"
          :aria-describedby="fontSizeValid ? undefined : 'knowledge-font-size-error'"
        />
        <span v-if="!fontSizeValid" id="knowledge-font-size-error" class="appearance-error" role="alert">
          {{ t('knowledge.appearanceNumberRange', { min: limits.fontSize.min, max: limits.fontSize.max }) }}
        </span>
      </div>
      <div class="appearance-row">
        <span class="appearance-label">{{ t('knowledge.lineHeightLabel') }}</span>
        <a-slider
          :value="draft.lineHeight ?? limits.lineHeight.min"
          @update:value="updateLineHeight"
          class="appearance-slider"
          :min="limits.lineHeight.min"
          :max="limits.lineHeight.max"
          :step="limits.lineHeight.step"
          :aria-label-for-handle="t('knowledge.lineHeightLabel')"
        />
        <a-input-number
          v-model:value="draft.lineHeight"
          class="appearance-number"
          size="small"
          :min="limits.lineHeight.min"
          :max="limits.lineHeight.max"
          :step="limits.lineHeight.step"
          :precision="2"
          :aria-label="t('knowledge.lineHeightLabel')"
          :status="lineHeightValid ? undefined : 'error'"
          :aria-invalid="!lineHeightValid"
          :aria-describedby="lineHeightValid ? undefined : 'knowledge-line-height-error'"
        />
        <span v-if="!lineHeightValid" id="knowledge-line-height-error" class="appearance-error" role="alert">
          {{ t('knowledge.appearanceNumberRange', { min: limits.lineHeight.min, max: limits.lineHeight.max }) }}
        </span>
      </div>
      <div class="appearance-row appearance-row--color">
        <label for="knowledge-appearance-color" class="appearance-label">{{ t('knowledge.textColorLabel') }}</label>
        <div class="appearance-color">
          <input
            id="knowledge-appearance-color"
            :value="normalizedTextColor ?? DEFAULT_KNOWLEDGE_APPEARANCE.textColor"
            class="appearance-color-input"
            type="color"
            @input="updatePickedColor"
          />
          <a-input
            v-model:value="draft.textColor"
            class="appearance-color-text"
            size="small"
            :maxlength="7"
            :placeholder="DEFAULT_KNOWLEDGE_APPEARANCE.textColor"
            :aria-label="t('knowledge.textColorLabel')"
            :status="normalizedTextColor ? undefined : 'error'"
            :aria-invalid="!normalizedTextColor"
            :aria-describedby="normalizedTextColor ? undefined : 'knowledge-text-color-error'"
          />
        </div>
        <span v-if="!normalizedTextColor" id="knowledge-text-color-error" class="appearance-error" role="alert">
          {{ t('knowledge.appearanceColorInvalid') }}
        </span>
      </div>
    </div>
    <a-button class="appearance-reset" type="link" size="small" @click="restoreDefaults">
      {{ t('knowledge.resetAppearance') }}
    </a-button>
  </a-modal>
</template>

<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { useAppI18n } from '@/i18n'
import {
  DEFAULT_KNOWLEDGE_APPEARANCE,
  KNOWLEDGE_APPEARANCE_LIMITS as limits,
  isKnowledgeFontSize,
  isKnowledgeLineHeight,
  normalizeKnowledgeTextColor,
  type KnowledgeAppearance,
} from '@/views/knowledge/composables/knowledgePreferences'

const props = defineProps<{ open: boolean; value: KnowledgeAppearance }>()
const emit = defineEmits<{
  'update:open': [value: boolean]
  change: [value: KnowledgeAppearance]
}>()
const { t } = useAppI18n()
const draft = reactive<{ fontSize: number | null; lineHeight: number | null; textColor: string }>({ ...props.value })
const fontSizeValid = computed(() => isKnowledgeFontSize(draft.fontSize))
const lineHeightValid = computed(() => isKnowledgeLineHeight(draft.lineHeight))
const normalizedTextColor = computed(() => normalizeKnowledgeTextColor(draft.textColor))
const valid = computed(() => fontSizeValid.value && lineHeightValid.value && Boolean(normalizedTextColor.value))
watch(() => props.open, (open) => {
  if (open) Object.assign(draft, props.value)
})
function updateFontSize(value: number | [number, number]) {
  if (typeof value === 'number') draft.fontSize = value
}

function updateLineHeight(value: number | [number, number]) {
  if (typeof value === 'number') draft.lineHeight = value
}

function updatePickedColor(event: Event) {
  if (event.target instanceof HTMLInputElement) draft.textColor = event.target.value
}

function restoreDefaults() {
  Object.assign(draft, DEFAULT_KNOWLEDGE_APPEARANCE)
}

function apply() {
  const textColor = normalizedTextColor.value
  if (!isKnowledgeFontSize(draft.fontSize) || !isKnowledgeLineHeight(draft.lineHeight) || !textColor) return
  emit('change', { fontSize: draft.fontSize, lineHeight: draft.lineHeight, textColor })
  emit('update:open', false)
}
</script>

<style scoped>
.appearance-settings {
  padding: 8px 0;
}

.appearance-reset {
  padding-inline: 0;
}

.appearance-row {
  display: grid;
  grid-template-columns: minmax(80px, 1fr) minmax(80px, 160px) 104px;
  min-height: 56px;
  align-items: center;
  gap: 12px;
  border-bottom: 1px solid #f0f0f0;
}

.appearance-row:last-child {
  border-bottom: 0;
}

.appearance-label {
  color: #595959;
  font-size: 14px;
  line-height: 22px;
}

.appearance-number {
  width: 104px;
}

.appearance-color-text {
  width: 104px;
}

.appearance-error {
  grid-column: 1 / -1;
  padding-bottom: 8px;
  color: #fb363f;
  font-size: 12px;
  line-height: 20px;
}

.appearance-slider {
  margin: 10px 0;
}

.appearance-row--color {
  grid-template-columns: minmax(90px, 1fr) auto;
}

.appearance-color {
  display: flex;
  align-items: center;
  gap: 12px;
}

.appearance-color-input {
  width: 36px;
  height: 28px;
  padding: 4px;
  border: 1px solid #d9d9d9;
  border-radius: 6px;
  background: #fff;
  cursor: pointer;
}

.appearance-color-input::-webkit-color-swatch-wrapper {
  padding: 0;
}

.appearance-color-input::-webkit-color-swatch {
  border: 0;
}

.appearance-color-input::-moz-color-swatch {
  border: 0;
}

.appearance-color-input:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

@media (max-width: 480px) {
  .appearance-row {
    grid-template-columns: minmax(64px, 1fr) minmax(48px, 2fr) 104px;
    gap: 8px;
  }

  .appearance-row--color {
    grid-template-columns: minmax(72px, 1fr) auto;
  }
}
</style>
