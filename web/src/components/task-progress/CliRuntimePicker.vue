<template>
  <a-popover
    :open="open"
    trigger="click"
    placement="topLeft"
    :arrow="false"
    destroy-tooltip-on-hide
    overlay-class-name="cli-picker-popover"
    @open-change="emit('update:open', $event)"
  >
    <template #content>
      <div class="cli-picker" role="dialog" :aria-label="t('workflows.task.progress.chooseRuntime')">
        <div class="cli-picker-clis" role="listbox" :aria-label="t('workflows.task.progress.cliList')">
          <a-spin :spinning="cliLoading">
            <button v-if="showExecutionSwitch" type="button" class="cli-picker-item execution-switch" @click="emit('switch-execution')"><SwapOutlined /><span>{{ t('workflows.task.detail.switchExecutionMode') }}</span></button>
            <button
              v-for="cli in visibleCliOptions"
              :key="cli.type"
              type="button"
              class="cli-picker-item"
              :class="{
                'is-selected': cli.type === selectedCliType,
                'is-unavailable': !cli.installed,
              }"
              role="option"
              :aria-selected="cli.type === selectedCliType"
              :aria-disabled="!cli.installed"
              :disabled="!cli.installed"
              @click="selectCli(cli)"
            >
              <span class="cli-picker-name">
                <img
                  class="cli-picker-logo"
                  :src="cliLogo(cli.type)"
                  alt=""
                  aria-hidden="true"
                />
                <span
                  class="cli-picker-dot"
                  :class="cli.installed ? 'is-available' : 'is-unavailable'"
                  aria-hidden="true"
                />
                <span class="cli-picker-label">{{ cli.name }}</span>
              </span>
            </button>
            <p
              v-if="!cliLoading && !visibleCliOptions.length"
              class="cli-picker-empty"
            >
              {{ t('workflows.task.progress.noCli') }}
            </p>
          </a-spin>
        </div>
        <div class="cli-picker-models" role="group" :aria-label="t('workflows.task.progress.modelList')">
          <CliModelReadButton :cli-type="selectedCliType" :task-uuid="taskUuid" />
          <a-spin :spinning="modelLoading">
            <div
              v-for="model in visibleModelOptions"
              :key="model.value"
              class="cli-picker-model"
              :class="{ 'is-selected': model.value === selectedModelName }"
            >
              <button type="button" class="cli-picker-model-name" :title="model.label" :aria-pressed="model.value === selectedModelName" @click="selectModel(model.value)">
                <img class="cli-picker-model-logo" :src="modelLogo(model.value, selectedCliType)" alt="" aria-hidden="true" />
                <span class="cli-picker-model-label">{{ model.label }}</span>
              </button>
              <CliThinkingSelect
                v-if="model.value === selectedModelName"
                :cli-type="selectedCliType"
                :model-name="model.value"
                :model-value="thinkingLevel || ''"
                :task-uuid="taskUuid"
                :popup-z-index="1200"
                compact
                @update:model-value="emit('select-thinking', $event)"
              />
              <img
                v-if="model.value === selectedModelName"
                class="cli-picker-check"
                :src="cliPickerCheckIcon"
                alt=""
                aria-hidden="true"
              />
            </div>
            <p
              v-if="!selectedCliType"
              class="cli-picker-empty"
            >
              {{ t('workflows.task.progress.chooseCli') }}
            </p>
            <p
              v-else-if="!modelLoading && !visibleModelOptions.length"
              class="cli-picker-empty"
            >
              {{ t('workflows.task.progress.noModels') }}
            </p>
          </a-spin>
        </div>
      </div>
    </template>
    <slot />
  </a-popover>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import CliModelReadButton from '@/components/CliModelReadButton.vue'
import CliThinkingSelect from '@/components/CliThinkingSelect.vue'
import { SwapOutlined } from '@ant-design/icons-vue'
import cliPickerCheckIcon from '@/assets/icons/cli-picker-check.svg'
import { cliLogo } from '@/composables/useCliLogo'
import { modelLogo } from '@/composables/useModelLogo'
import type { DiscoveredCLI } from '@/views/agents/components/agentPipeline'
import { useAppI18n } from '@/i18n'
import { cliModelChoices } from '@/utils/cliModelOptions'

const { t } = useAppI18n()

const props = defineProps<{
  open: boolean
  cliOptions: DiscoveredCLI[]
  cliLoading: boolean
  selectedCliType: string
  selectedModelName: string
  thinkingLevel?: string
  taskUuid?: string
  modelOptions: string[]
  modelLoading: boolean
  showExecutionSwitch?: boolean
}>()

const emit = defineEmits<{
  'update:open': [open: boolean]
  'select-cli': [cliType: string]
  'select-model': [model: string]
  'select-thinking': [level: string]
  'switch-execution': []
}>()

const visibleCliOptions = computed(() => {
  const items = [...props.cliOptions]
  if (props.selectedCliType && !items.some(item => item.type === props.selectedCliType)) {
    items.unshift({
      type: props.selectedCliType,
      name: props.selectedCliType,
      installed: true,
    })
  }
  return items
})

const visibleModelOptions = computed(() => cliModelChoices(props.selectedCliType, props.modelOptions, props.selectedModelName, t('components.cliSelect.modelDefault')))



function selectCli(cli: DiscoveredCLI) {
  if (!cli.installed) return
  emit('select-cli', cli.type)
}

function selectModel(model: string) {
  emit('select-model', model)
}
</script>

<style scoped>
.cli-picker-item.execution-switch { justify-content: flex-start; color: #3157e2; gap: 8px; }
.cli-picker {
  display: flex;
  width: min(520px, calc(100vw - 32px));
  height: 279px;
  overflow: hidden;
}

.cli-picker-clis,
.cli-picker-models {
  min-height: 0;
  overflow-y: auto;
}

.cli-picker-clis {
  width: min(206px, 40vw);
  flex: 0 0 min(206px, 40vw);
  background: #f5f5f5;
  border-right: 1px solid #f1f5f9;
}

.cli-picker-models {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  padding: 8px;
  background: #fff;
}

.cli-picker-clis :deep(.ant-spin-nested-loading),
.cli-picker-models :deep(.ant-spin-nested-loading),
.cli-picker-clis :deep(.ant-spin-container),
.cli-picker-models :deep(.ant-spin-container) {
  min-height: 100%;
}

.cli-picker-item,
.cli-picker-model {
  display: flex;
  width: 100%;
  align-items: center;
  border: 0;
  background: transparent;
  cursor: pointer;
  font: inherit;
  text-align: left;
}

.cli-picker-item {
  position: relative;
  min-height: 40px;
  justify-content: space-between;
  gap: 8px;
  padding: 8px 16px 8px 18px;
}

.cli-picker-item.is-selected {
  background: #fff;
}

.cli-picker-item.is-selected::before {
  position: absolute;
  top: 0;
  bottom: 0;
  left: 0;
  width: 2px;
  background: #262626;
  content: '';
}

.cli-picker-item.is-unavailable {
  cursor: not-allowed;
}

.cli-picker-name {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 8px;
  color: #595959;
  font-size: 14px;
  line-height: 22px;
}

.cli-picker-item.is-selected .cli-picker-name {
  color: #262626;
  font-weight: 600;
}

.cli-picker-item.is-unavailable .cli-picker-name {
  color: #bfbfbf;
}

.cli-picker-logo {
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
  object-fit: contain;
}

.cli-picker-item.is-unavailable .cli-picker-logo {
  opacity: 0.4;
  filter: grayscale(1);
}

.cli-picker-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cli-picker-dot {
  width: 8px;
  height: 8px;
  flex: 0 0 8px;
  border-radius: 50%;
}

.cli-picker-dot.is-available {
  background: #21a665;
}

.cli-picker-dot.is-unavailable {
  background: #bfbfbf;
}


.cli-picker-model {
  min-height: 36px;
  justify-content: space-between;
  gap: 8px;
  padding: 6px 12px;
  border-radius: 8px;
  color: #1d293d;
  font-size: 14px;
  line-height: 22px;
}

.cli-picker-model.is-selected {
  background: #f5f5f5;
}

.cli-picker-model:hover {
  background: #f5f5f5;
}

.cli-picker-model-name {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
  min-height: 32px;
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  font: inherit;
  text-align: left;
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cli-picker-model-logo {
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
  object-fit: contain;
}

.cli-picker-model-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.cli-picker-check {
  display: block;
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
}

.cli-picker-empty {
  margin: 0;
  padding: 16px 12px;
  color: #8c8c8c;
  font-size: 12px;
  line-height: 20px;
}

.cli-picker-item:focus-visible,
.cli-picker-model-name:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: -2px;
}

:global(.cli-picker-popover .ant-popover-inner) {
  padding: 0;
  overflow: hidden;
  border: 1px solid #e2e8f0;
  border-radius: 14px;
  box-shadow: 0 12px 40px -8px rgba(15, 23, 42, 0.18);
}

:global(.cli-picker-popover .ant-popover-inner-content) {
  padding: 0;
}
</style>
