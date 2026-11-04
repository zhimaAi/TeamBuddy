<template>
  <div
    class="batch-bar"
    role="toolbar"
    :aria-label="t('agents.batchCli')"
  >
    <span class="batch-count">
      <img
        class="batch-count-icon"
        :src="batchSelectedCountIcon"
        alt=""
        aria-hidden="true"
      />
      {{ t('agents.selectedCount', { count: selectedCount }) }}
    </span>
    <a-select
      v-model:value="batchCliType"
      class="batch-select"
      :loading="cliLoading"
      :disabled="saving"
      :placeholder="t('agents.selectCli')"
      @change="handleCliChange(String($event))"
    >
      <a-select-option
        v-for="cli in cliOptions"
        :key="cli.type"
        :value="cli.type"
        :disabled="!cli.installed"
      >
        {{ cli.name }}
      </a-select-option>
    </a-select>
    <a-select
      v-model:value="batchModelName"
      class="batch-select"
      :loading="modelLoading"
      :disabled="!batchCliType || saving"
      show-search
      :placeholder="batchCliType ? t('agents.selectModel') : t('agents.selectCliFirst')"
    >
      <a-select-option
        v-for="model in modelOptions"
        :key="model"
        :value="model"
      >
        {{ model }}
      </a-select-option>
    </a-select>
    <a-checkbox v-model:checked="configureThinking" :disabled="saving">{{ t('components.cliThinking.configure') }}</a-checkbox>
    <CliThinkingSelect v-if="configureThinking && batchCliType && batchModelName" v-model="thinkingLevel" :cli-type="batchCliType" :model-name="batchModelName" />
    <a-button
      type="primary"
      size="small"
      class="batch-apply"
      :loading="saving"
      :disabled="!canApply"
      @click="apply"
    >
      {{ t('agents.apply') }}
    </a-button>
    <a-button
      size="small"
      class="batch-clear"
      :disabled="saving"
      @click="cancel"
    >
      {{ t('agents.cancel') }}
    </a-button>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import CliThinkingSelect from '@/components/CliThinkingSelect.vue'
import { useCliThinkingSelection } from '@/composables/useCliThinkingSelection'
import { message } from 'ant-design-vue'
import batchSelectedCountIcon from '@/assets/icons/batch-selected-count.svg'
import { useCliModelOptions } from '@/composables/useCliModelOptions'
import { useAppI18n } from '@/i18n'

const props = defineProps<{
  selectedCount: number
  saving?: boolean
}>()

const emit = defineEmits<{
  apply: [payload: { cliType: string; modelName: string; thinkingLevel?: string }]
  cancel: []
}>()

const { t } = useAppI18n()
const batchCliType = ref('')
const batchModelName = ref('')
const configureThinking = ref(false)
const thinkingLevel = ref('')
const { resetThinkingDrafts } = useCliThinkingSelection(() => batchCliType.value, () => batchModelName.value, thinkingLevel)
const {
  cliLoading,
  cliOptions,
  loadCliOptions,
  loadModelOptions,
  modelLoading,
  modelOptions,
  resetModelOptions,
} = useCliModelOptions()

const canApply = computed(
  () =>
    props.selectedCount > 0 &&
    Boolean(batchCliType.value) &&
    Boolean(batchModelName.value) &&
    !props.saving,
)

async function handleCliChange(cliType: string) {
  batchModelName.value = ''
  try {
    await loadModelOptions(cliType)
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('agents.modelListLoadFailed'))
  }
}

function apply() {
  if (!canApply.value) return
  emit('apply', { cliType: batchCliType.value, modelName: batchModelName.value, thinkingLevel: configureThinking.value ? thinkingLevel.value : undefined })
}

function cancel() {
  if (props.saving) return
  batchCliType.value = ''
  batchModelName.value = ''
  configureThinking.value = false
  resetThinkingDrafts()
  resetModelOptions()
  emit('cancel')
}

onMounted(async () => {
  try {
    await loadCliOptions()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('agents.cliListLoadFailed'))
  }
})
</script>

<style scoped>
.batch-bar {
  position: absolute;
  z-index: 2;
  right: 24px;
  bottom: 24px;
  left: 24px;
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 16px 12px;
  border: 1px solid #abcafc;
  border-radius: 16px;
  background: #fff;
  box-shadow: 0 6px 24px rgba(0, 0, 0, 0.18);
}

.batch-count {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  color: #595959;
  font-size: 14px;
  line-height: 22px;
  white-space: nowrap;
}

.batch-count-icon {
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
}

.batch-select {
  min-width: 0;
  flex: 1 1 180px;
}

.batch-apply,
.batch-clear {
  flex: 0 0 auto;
}

.batch-bar :deep(.batch-apply.ant-btn-primary:disabled) {
  color: #fff;
  border-color: #a7c8fe;
  background: #a7c8fe;
}

@media (max-width: 900px) {
  .batch-bar {
    right: 16px;
    bottom: 16px;
    left: 16px;
    flex-wrap: wrap;
  }

  .batch-select {
    min-width: 160px;
  }
}
</style>
