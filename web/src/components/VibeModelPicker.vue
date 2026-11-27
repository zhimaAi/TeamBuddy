<template>
  <a-popover v-model:open="open" trigger="click" placement="bottomRight" :overlay-style="{ zIndex: 1600 }">
    <template #content>
      <div class="vibe-model-menu" @click.stop>
        <div class="vibe-model-heading">{{ t('workflows.task.assign.chooseModel') }}</div>
        <a-spin :spinning="modelLoading">
          <a-radio-group :value="modelValue" class="vibe-model-options" @change="selectModel($event.target.value)">
            <a-radio v-for="model in options" :key="model.value" :value="model.value" :title="model.label" @click="model.value === modelValue && selectModel(model.value)">
              {{ model.label }}
            </a-radio>
          </a-radio-group>
        </a-spin>
        <CliModelReadButton :cli-type="cliType" :disabled="disabled" />
      </div>
    </template>
    <button type="button" class="vibe-model-trigger" :disabled="disabled" :title="label" @click.stop>
      {{ label }}
    </button>
  </a-popover>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useAppI18n } from '@/i18n'
import CliModelReadButton from '@/components/CliModelReadButton.vue'
import { useCLIModelCatalogStore } from '@/stores/cli-models'
import { vibeToolDefinition } from '@/composables/useVibeCoding'

const props = defineProps<{ tool: string; modelValue: string; selected?: boolean; disabled?: boolean }>()
const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()
const { t } = useAppI18n()
const directories = useCLIModelCatalogStore()
const cliType = computed(() => vibeToolDefinition(props.tool)?.cliType || '')
const modelOptions = computed(() => directories.catalog(cliType.value)?.models || [])
const modelLoading = computed(() => directories.isLoading(cliType.value))
const open = ref(false)
const label = computed(() => props.modelValue || t(props.selected
  ? 'workflows.task.assign.vibeModelDefault'
  : 'workflows.task.assign.vibeModelConfigure'))
const options = computed(() => [
  { value: '', label: t('workflows.task.assign.vibeModelDefault') },
  ...[...new Set([...modelOptions.value, props.modelValue].filter(Boolean))].map(model => ({ value: model, label: model })),
])

function selectModel(model: string) {
  emit('update:modelValue', model)
  open.value = false
}

watch(() => props.disabled, value => { if (value) open.value = false })
</script>

<style scoped>
.vibe-model-trigger { overflow: hidden; max-width: 160px; flex: 0 1 auto; min-width: 24px; min-height: 24px; padding: 0 4px; border: 0; border-radius: 6px; background: transparent; color: #3157e2; font-size: 12px; text-overflow: ellipsis; white-space: nowrap; cursor: pointer; }
.vibe-model-trigger:disabled { color: #8c8c8c; cursor: default; }
.vibe-model-trigger:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.vibe-model-menu { width: 300px; max-width: 75vw; }
.vibe-model-heading { margin-bottom: 8px; font-weight: 600; }
.vibe-model-options { display: flex; max-height: 300px; overflow-y: auto; flex-direction: column; gap: 4px; width: 100%; }
.vibe-model-options :deep(.ant-radio-wrapper) { margin: 0; padding: 8px 4px; overflow-wrap: anywhere; }
</style>
