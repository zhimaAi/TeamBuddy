<script setup lang="ts">
import { ref, watch } from 'vue'
import CliThinkingSelect from '@/components/CliThinkingSelect.vue'
import { useCliThinkingSelection } from '@/composables/useCliThinkingSelection'
import { useCLIModelCatalogStore } from '@/stores/cli-models'
import { message } from 'ant-design-vue'
import apiClient from '@/api/client'
import { useAppI18n } from '@/i18n'

const { t } = useAppI18n()
const directories = useCLIModelCatalogStore()

interface DiscoveredCLI {
  type: string
  name: string
  installed: boolean
  exec_path: string
  version: string
  models: string[]
}

const props = defineProps<{
  open: boolean
  taskUuid?: string
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
  confirm: [payload: { cli_type: string; model: string; thinking_level: string }]
}>()

const loading = ref(false)
const cliList = ref<DiscoveredCLI[]>([])
const selectedType = ref('')
const selectedModel = ref('')
const thinkingLevel = ref('')
const useCustomModel = ref(false)
const customModel = ref('')
const { resetThinkingDrafts } = useCliThinkingSelection(() => selectedType.value, () => useCustomModel.value ? customModel.value.trim() : selectedModel.value, thinkingLevel)
const models = ref<string[]>([])
const modelsLoading = ref(false)
let modelReqSeq = 0

const selectedCli = () => cliList.value.find(item => item.type === selectedType.value)

// ---- Local persistence of custom models ----
const CUSTOM_MODELS_KEY = 'cli_custom_models'

function loadCustomModels(cliType: string): string[] {
  try {
    const raw = localStorage.getItem(CUSTOM_MODELS_KEY)
    if (!raw) return []
    const all = JSON.parse(raw) as Record<string, string[]>
    return all[cliType] || []
  } catch {
    return []
  }
}

function saveCustomModel(cliType: string, model: string) {
  if (!model) return
  try {
    const raw = localStorage.getItem(CUSTOM_MODELS_KEY)
    const all: Record<string, string[]> = raw ? JSON.parse(raw) : {}
    const list = all[cliType] || []
    if (!list.includes(model)) {
      list.unshift(model)
      // Keep at most 20 entries
      all[cliType] = list.slice(0, 20)
      localStorage.setItem(CUSTOM_MODELS_KEY, JSON.stringify(all))
    }
  } catch { /* ignore */ }
}

/** Merge freshly probed models + locally recorded custom models */
function mergedModels(probed: string[]): { name: string; custom: boolean }[] {
  const result: { name: string; custom: boolean }[] = probed.map(m => ({ name: m, custom: false }))
  for (const m of loadCustomModels(selectedType.value)) {
    if (!probed.includes(m)) {
      result.push({ name: m, custom: true })
    }
  }
  return result
}

watch(() => props.open, async (visible) => {
  if (visible) {
    selectedType.value = ''
    selectedModel.value = ''
    useCustomModel.value = false
    customModel.value = ''
    resetThinkingDrafts()
    await loadCLIs()
  }
})

async function loadCLIs() {
  loading.value = true
  models.value = []
  try {
    const result = await apiClient.get<{ items: DiscoveredCLI[] }>('/tasks/cli-discovery')
    // 已安装的 CLI 排在前面，未安装的置后且不可选择
    cliList.value = [...(result.items || [])].sort((a, b) => Number(b.installed) - Number(a.installed))
    // Auto-select the first installed CLI and load its model list right away
    const firstInstalled = cliList.value.find(item => item.installed)
    if (firstInstalled) {
      selectedType.value = firstInstalled.type
      await loadModels(firstInstalled.type)
    }
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('components.cliSelect.detectFailed'))
  } finally {
    loading.value = false
  }
}

async function loadModels(cliType: string) {
  const seq = ++modelReqSeq
  modelsLoading.value = true
  models.value = []
  selectedModel.value = ''
  let fetched: string[] = []
  try {
    const result = await directories.load(cliType, { taskUuid: props.taskUuid })
    fetched = result.models || []
  } catch (error) {
    if (seq === modelReqSeq) {
      message.error(error instanceof Error ? error.message : t('components.cliSelect.modelsLoadFailed'))
    }
  } finally {
    if (seq === modelReqSeq) {
      modelsLoading.value = false
      models.value = fetched
      const merged = mergedModels(fetched)
      selectedModel.value = merged.length > 0 ? merged[0].name : ''
      useCustomModel.value = merged.length === 0
    }
  }
}

async function selectCli(type: string) {
  const cli = cliList.value.find(item => item.type === type)
  if (!cli || !cli.installed || type === selectedType.value) return
  // Only load the model list of the clicked CLI on demand
  selectedType.value = type
  customModel.value = ''
  await loadModels(type)
}

function handleConfirm() {
  if (!selectedType.value) {
    message.warning(t('components.cliSelect.chooseCliWarning'))
    return
  }
  const cli = selectedCli()
  if (!cli) return

  const model = useCustomModel.value ? customModel.value.trim() : selectedModel.value
  // Persist manually entered models so they can be picked directly from the list next time
  if (useCustomModel.value && model) {
    saveCustomModel(selectedType.value, model)
  }
  emit('confirm', { cli_type: selectedType.value, model, thinking_level: thinkingLevel.value })
  emit('update:open', false)
}

function handleCancel() {
  emit('update:open', false)
}
</script>

<template>
  <a-modal
    :open="open"
    :title="t('components.cliSelect.title')"
    :width="560"
    :mask-closable="false"
    @ok="handleConfirm"
    @cancel="handleCancel"
  >
    <template #footer>
      <a-button @click="handleCancel">{{ t('common.actions.cancel') }}</a-button>
      <a-button type="primary" :disabled="!selectedType" @click="handleConfirm">{{ t('components.cliSelect.start') }}</a-button>
    </template>

    <a-spin :spinning="loading">
      <div class="cli-select-body">
        <div class="cli-card-list">
          <div
            v-for="cli in cliList"
            :key="cli.type"
            class="cli-card"
            :class="{
              'cli-card--selected': selectedType === cli.type,
              'cli-card--disabled': !cli.installed,
            }"
            @click="selectCli(cli.type)"
          >
            <div class="cli-card__header">
              <span class="cli-card__name">{{ cli.name }}</span>
              <a-tag v-if="!cli.installed" color="default">{{ t('components.cliSelect.notInstalled') }}</a-tag>
              <a-tag v-else-if="selectedType === cli.type" color="green">{{ t('components.cliSelect.selected') }}</a-tag>
            </div>
            <div v-if="cli.installed" class="cli-card__meta">
              <span v-if="cli.version" class="cli-card__version">{{ cli.version }}</span>
            </div>
            <div v-else class="cli-card__meta">
              <span class="cli-card__hint">{{ t('components.cliSelect.installHint') }}</span>
            </div>
          </div>
        </div>

        <div v-if="selectedCli()?.installed" class="cli-model-section">
          <div class="cli-model-section__label">{{ t('components.cliSelect.modelConfig') }}</div>
          <a-spin :spinning="modelsLoading">
            <a-select
              v-if="!useCustomModel && mergedModels(models).length > 0"
              v-model:value="selectedModel"
              :placeholder="t('components.cliSelect.chooseModel')"
              style="width: 100%"
              show-search
              :filter-option="(input: string, option: any) => (option?.value || '').toLowerCase().includes(input.toLowerCase())"
            >
              <a-select-option v-for="m in mergedModels(models)" :key="m.name" :value="m.name">
                {{ m.name }}
                <a-tag v-if="m.custom" color="blue" size="small" style="margin-left: 6px">{{ t('components.cliSelect.custom') }}</a-tag>
              </a-select-option>
            </a-select>
            <a-input
              v-else
              v-model:value="customModel"
              :placeholder="t('components.cliSelect.customPlaceholder')"
              allow-clear
            />
          </a-spin>
          <CliThinkingSelect v-if="(useCustomModel ? customModel.trim() : selectedModel)" v-model="thinkingLevel" :cli-type="selectedType" :model-name="useCustomModel ? customModel.trim() : selectedModel" :task-uuid="taskUuid" />
          <div class="cli-model-section__footer">
            <a
              v-if="mergedModels(models).length > 0"
              class="cli-model-section__toggle"
              @click="useCustomModel = !useCustomModel"
            >
              {{ useCustomModel ? t('components.cliSelect.chooseFromList') : t('components.cliSelect.manualInput') }}
            </a>
            <span v-if="useCustomModel" class="cli-model-section__hint">{{ t('components.cliSelect.customHint') }}</span>
          </div>
        </div>
      </div>
    </a-spin>
  </a-modal>
</template>

<style scoped>
.cli-select-body {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.cli-card-list {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 10px;
}

.cli-card {
  padding: 12px 14px;
  border: 1.5px solid #e8e8e8;
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.2s;
  user-select: none;
}

.cli-card:hover:not(.cli-card--disabled) {
  border-color: #91caff;
  background: #f0f7ff;
}

.cli-card--selected {
  border-color: #1677ff;
  background: #e6f4ff;
}

.cli-card--disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.cli-card__header {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 4px;
}

.cli-card__name {
  font-weight: 600;
  font-size: 14px;
}

.cli-card__meta {
  display: flex;
  gap: 10px;
  font-size: 12px;
  color: #8c8c8c;
}

.cli-card__hint {
  color: #bfbfbf;
}

.cli-model-section {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.cli-model-section__label {
  font-weight: 500;
  font-size: 13px;
  color: #595959;
}

.cli-model-section__footer {
  display: flex;
  align-items: center;
  gap: 12px;
}

.cli-model-section__toggle {
  font-size: 12px;
  color: #1677ff;
  cursor: pointer;
}

.cli-model-section__toggle:hover {
  text-decoration: underline;
}

.cli-model-section__hint {
  font-size: 12px;
  color: #8c8c8c;
}
</style>
