<template>
  <a-popover
    v-if="visible"
    v-model:open="open"
    trigger="click"
    placement="bottomRight"
    :arrow="false"
    :overlay-style="popupStyle"
    :overlay-inner-style="{ padding: '8px', borderRadius: '16px' }"
    @open-change="handleOpen"
  >
    <template #content>
      <div ref="menu" class="thinking-menu" @click.stop @keydown="handleMenuKeydown" @keydown.esc.stop.prevent="close">
        <div class="thinking-header">
          <div class="thinking-heading">
            <div :id="`${menuId}-title`" class="thinking-title">{{ menuTitle }}</div>
            <p class="thinking-model" :title="modelName">{{ modelName }}</p>
          </div>
          <a-button type="text" size="small" class="thinking-refresh" :loading="loading" :disabled="disabled" :aria-label="t('components.cliThinking.refresh')" :title="t('components.cliThinking.refresh')" @click="load(true)">
            <template #icon><ReloadOutlined aria-hidden="true" /></template>
          </a-button>
        </div>
        <p v-if="savedUnavailable" class="thinking-warning" role="status">
          <ExclamationCircleOutlined aria-hidden="true" />
          <span>{{ t('components.cliThinking.saved', { level: levelLabel(modelValue) }) }}</span>
        </p>
        <p v-else-if="!loading && !canChooseLevel" class="thinking-warning" role="status">
          <ExclamationCircleOutlined aria-hidden="true" />
          <span>{{ error || t('components.cliThinking.unavailable') }}</span>
        </p>
        <div :id="menuId" class="thinking-options" role="menu" :aria-labelledby="`${menuId}-title`" :aria-busy="loading">
          <button type="button" class="thinking-option thinking-follow" :class="{ selected: modelValue === '' }" role="menuitemradio" :aria-checked="modelValue === ''" :tabindex="modelValue === '' ? 0 : -1" :disabled="disabled" @click="choose('')">
            <span class="thinking-option__copy">
              <span>{{ t('components.cliThinking.follow') }}</span>
              <span class="thinking-option__hint">{{ t('components.cliThinking.followHint') }}</span>
            </span>
            <CheckOutlined v-if="modelValue === ''" class="thinking-check" aria-hidden="true" />
          </button>
          <div v-if="thinking?.levels?.length" class="thinking-divider" role="separator" />
          <button v-for="level in thinking?.levels || []" :key="level.value" type="button" class="thinking-option" :class="{ selected: modelValue === level.value }" role="menuitemradio" :aria-checked="modelValue === level.value" :tabindex="modelValue === level.value ? 0 : -1" :disabled="disabled || !canChooseLevel" :title="level.description" @click="choose(level.value)">
            <span>{{ levelLabel(level.value) }}</span>
            <CheckOutlined v-if="modelValue === level.value" class="thinking-check" aria-hidden="true" />
          </button>
        </div>
        <p v-if="thinking?.source === 'client_token_limit'" class="thinking-notice">{{ t('components.cliThinking.clientLimit') }}</p>
        <p v-if="thinking?.kind === 'variant'" class="thinking-notice">{{ t('components.cliThinking.variantHint') }}</p>
        <p v-if="thinking?.kind === 'cli' || thinking?.kind === 'toggle'" class="thinking-notice">{{ t('components.cliThinking.cliHint') }}</p>
        <div class="thinking-footer">
          <span>{{ t('components.cliThinking.costHint') }}</span>
          <a-tooltip :title="t('components.cliThinking.hint')" :trigger="['hover', 'focus']" :overlay-style="tooltipStyle">
            <button type="button" class="thinking-info" :aria-label="t('components.cliThinking.hint')">
              <InfoCircleOutlined aria-hidden="true" />
            </button>
          </a-tooltip>
        </div>
      </div>
    </template>
    <button
      ref="trigger"
      type="button"
      class="thinking-trigger"
      :class="{ configured: !!modelValue, 'is-open': open, 'is-warning': savedUnavailable, 'is-compact': compact }"
      :disabled="disabled"
      :aria-label="triggerLabel"
      :aria-expanded="open"
      :aria-controls="open ? menuId : undefined"
      :aria-busy="loading"
      aria-haspopup="menu"
      :title="triggerLabel"
      @click.stop
      @keydown.down.stop.prevent="handleOpen(true)"
      @keydown.up.stop.prevent="handleOpen(true)"
    >
      <LoadingOutlined v-if="loading && !knownChoices" aria-hidden="true" />
      <BulbOutlined v-else aria-hidden="true" />
      <span v-if="!compact" class="thinking-trigger__label">{{ modelValue ? levelLabel(modelValue) : t('components.cliThinking.follow') }}</span>
      <span v-if="savedUnavailable" class="thinking-trigger__warning" aria-hidden="true" />
    </button>
  </a-popover>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import { BulbOutlined, CheckOutlined, ExclamationCircleOutlined, InfoCircleOutlined, LoadingOutlined, ReloadOutlined } from '@ant-design/icons-vue'
import { useCLIModelCatalogStore } from '@/stores/cli-models'
import { useCLIThinkingPreferencesStore } from '@/stores/cli-thinking-preferences'
import { useAppI18n } from '@/i18n'

const props = withDefaults(defineProps<{
  cliType: string
  modelName: string
  modelValue?: string
  taskUuid?: string
  channelType?: string
  compact?: boolean
  popupZIndex?: number
  disabled?: boolean
}>(), { modelValue: '', taskUuid: '', compact: false, disabled: false })
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useAppI18n()
const open = ref(false)
const trigger = ref<HTMLButtonElement>()
const menu = ref<HTMLDivElement>()
const directories = useCLIModelCatalogStore()
const preferences = useCLIThinkingPreferencesStore()
const scope = computed(() => ({ taskUuid: props.taskUuid || undefined, channelType: props.channelType || undefined }))
const loading = computed(() => directories.isLoading(props.cliType, scope.value))
const error = computed(() => directories.error(props.cliType, scope.value))
const thinking = computed(() => {
  const options = directories.catalog(props.cliType, scope.value)?.model_options || []
  const exact = options.find(option => option.id === props.modelName) || options.find(option => option.aliases?.includes(props.modelName))
  if (exact || props.cliType !== 'cursor') return exact?.thinking
  const separator = props.modelName.indexOf('[')
  if (separator < 1 || !props.modelName.endsWith(']')) return undefined
  const base = props.modelName.slice(0, separator)
  const pairs = props.modelName.slice(separator + 1, -1).split(',')
  if (!pairs.every(pair => /^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}=[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$/.test(pair))
    || new Set(pairs.map(pair => pair.split('=')[0])).size !== pairs.length) return undefined
  return options.find(option => option.id === base)?.thinking
})
const menuId = `cli-thinking-${useId()}`
const menuTitle = computed(() => t(thinking.value?.kind === 'variant' ? 'components.cliThinking.variant'
  : thinking.value?.kind === 'toggle' ? 'components.cliThinking.toggle' : 'components.cliThinking.title'))
const knownChoices = computed(() => thinking.value?.status === 'supported' && !!thinking.value.levels?.length)
const visible = computed(() => !!props.cliType && !!props.modelName && (!!props.modelValue || knownChoices.value))
const popupStyle = computed(() => props.popupZIndex === undefined ? undefined : { zIndex: props.popupZIndex })
const tooltipStyle = computed(() => props.popupZIndex === undefined ? undefined : { zIndex: props.popupZIndex + 1 })
const canChooseLevel = computed(() => knownChoices.value && thinking.value?.state === 'fresh')
const savedUnavailable = computed(() => !!props.modelValue && (!!thinking.value || !loading.value) && (!canChooseLevel.value || !thinking.value?.levels?.some(level => level.value === props.modelValue)))
const triggerLabel = computed(() => t('components.cliThinking.triggerLabel', {
  title: menuTitle.value,
  level: savedUnavailable.value ? t('components.cliThinking.saved', { level: levelLabel(props.modelValue) })
    : props.modelValue ? levelLabel(props.modelValue) : canChooseLevel.value ? t('components.cliThinking.follow') : t('components.cliThinking.unavailable'),
}))
const levelKeys: Record<string, string> = { off: 'off', on: 'on', none: 'none', minimal: 'minimal', low: 'low', medium: 'medium', high: 'high', xhigh: 'xhigh', max: 'max' }
function levelLabel(value: string) { return thinking.value?.kind !== 'variant' && levelKeys[value] ? t(`components.cliThinking.${levelKeys[value]}`) : value }

async function load(refresh = false) {
  if (!props.cliType) return
  try { await directories.load(props.cliType, scope.value, refresh) } catch { /* The directory owner retains stale choices and the retry error. */ }
}

async function close() {
  const focusScope = trigger.value?.parentElement
  open.value = false
  await nextTick()
  // 清空失效历史值后灯泡会隐藏，焦点退回同一模型行，避免落到页面根部。
  const target = trigger.value || focusScope?.querySelector<HTMLElement>('button:not(:disabled), input[role="combobox"]:not(:disabled)')
  target?.focus()
}
function choose(value: string) {
  if (props.disabled || (value && (!canChooseLevel.value || !thinking.value?.levels?.some(level => level.value === value)))) return
  preferences.remember(props.cliType, props.modelName, value)
  emit('update:modelValue', value)
  void close()
}
function handleMenuKeydown(event: KeyboardEvent) {
  if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return
  const options = Array.from(menu.value?.querySelectorAll<HTMLButtonElement>('.thinking-option:not(:disabled)') || [])
  if (!options.length) return
  event.preventDefault()
  event.stopPropagation()
  const current = options.findIndex(option => option === document.activeElement)
  const index = event.key === 'Home' ? 0 : event.key === 'End' ? options.length - 1
    : event.key === 'ArrowUp' ? (current <= 0 ? options.length - 1 : current - 1) : (current + 1) % options.length
  options[index]?.focus()
}
function handleOpen(value: boolean) {
  if (value && props.disabled) return
  open.value = value
  if (value) void load()
}
// Portal 挂载后还要完成浮层定位；跨两帧再聚焦，关闭/切换/卸载时取消，不轮询。
watch([open, menu], ([isOpen, element], _, onCleanup) => {
  if (!isOpen || !element) return
  let frame = requestAnimationFrame(() => {
    frame = requestAnimationFrame(() => {
      if (!open.value || menu.value !== element) return
      const selected = element.querySelector<HTMLButtonElement>('.thinking-option[aria-checked="true"]:not(:disabled)')
        || element.querySelector<HTMLButtonElement>('.thinking-option:not(:disabled)')
      selected?.focus()
    })
  })
  onCleanup(() => cancelAnimationFrame(frame))
}, { flush: 'post' })
watch(() => [props.cliType, props.taskUuid, props.channelType] as const, () => {
  open.value = false
  void load()
}, { immediate: true })
// Switching models only projects the already shared directory; never start a probe.
watch(() => props.modelName, () => { open.value = false })
</script>

<style scoped>
.thinking-trigger {
  position: relative;
  display: inline-flex;
  min-width: 32px;
  min-height: 32px;
  max-width: 100%;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  gap: 6px;
  border: 0;
  border-radius: 10px;
  padding: 4px 8px;
  background: transparent;
  color: #595959;
  font: inherit;
  font-size: 12px;
  line-height: 20px;
  cursor: pointer;
  transition: color 180ms ease, background-color 180ms ease;
}
.thinking-trigger :deep(.anticon) { flex: 0 0 auto; font-size: 16px; }
.thinking-trigger.is-compact { width: 32px; padding: 4px; }
.thinking-trigger:hover { background: #f5f6f8; }
.thinking-trigger.configured, .thinking-trigger.is-open { background: #e5efff; color: #3157e2; }
.thinking-trigger.is-warning { background: #fff5e5; color: #595959; }
.thinking-trigger:disabled { color: #bfbfbf; cursor: not-allowed; }
.thinking-trigger__label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.thinking-trigger__warning { position: absolute; top: 4px; right: 4px; width: 6px; height: 6px; border-radius: 50%; background: #ed744a; }
.thinking-trigger:focus-visible, .thinking-option:focus-visible, .thinking-info:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.thinking-menu { width: 240px; max-width: calc(100vw - 48px); color: #262626; }
.thinking-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; padding: 8px 8px 12px; }
.thinking-heading { min-width: 0; }
.thinking-title { font-size: 14px; font-weight: 600; line-height: 22px; }
.thinking-model { overflow: hidden; margin: 2px 0 0; color: #8c8c8c; font-size: 12px; line-height: 20px; text-overflow: ellipsis; white-space: nowrap; }
.thinking-refresh { flex: 0 0 auto; color: #8c8c8c; }
.thinking-options { max-height: min(320px, calc(100vh - 220px)); overflow-y: auto; padding: 2px; }
.thinking-option { display: flex; width: 100%; min-height: 36px; align-items: center; justify-content: space-between; gap: 12px; border: 0; border-radius: 8px; padding: 6px 10px; background: #fff; color: #262626; cursor: pointer; text-align: left; font: inherit; font-size: 14px; line-height: 22px; overflow-wrap: anywhere; }
.thinking-option:hover { background: #f5f6f8; }
.thinking-option.selected { background: #e5efff; color: #3157e2; }
.thinking-option:disabled { color: #bfbfbf; cursor: not-allowed; }
.thinking-option__copy { display: flex; min-width: 0; flex-direction: column; gap: 2px; }
.thinking-option__hint { color: #8c8c8c; font-size: 12px; line-height: 20px; }
.thinking-check { flex: 0 0 auto; font-size: 14px; }
.thinking-divider { height: 1px; margin: 6px 8px; background: #f0f0f0; }
.thinking-warning { display: flex; align-items: flex-start; gap: 6px; margin: 0 2px 8px; border-radius: 8px; padding: 8px; background: #fff5e5; color: #595959; font-size: 12px; line-height: 20px; overflow-wrap: anywhere; }
.thinking-warning :deep(.anticon) { margin-top: 4px; color: #ed744a; }
.thinking-notice { margin: 8px 10px 0; color: #595959; font-size: 12px; line-height: 20px; overflow-wrap: anywhere; }
.thinking-footer { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin-top: 8px; border-top: 1px solid #f0f0f0; padding: 8px 8px 0; color: #8c8c8c; font-size: 12px; line-height: 20px; }
.thinking-info { display: inline-flex; width: 24px; height: 24px; flex: 0 0 auto; align-items: center; justify-content: center; border: 0; border-radius: 6px; padding: 0; background: transparent; color: #8c8c8c; cursor: help; }
.thinking-info:hover { background: #f5f6f8; color: #595959; }
@media (prefers-reduced-motion: reduce) { .thinking-trigger { transition: none; } }
</style>
