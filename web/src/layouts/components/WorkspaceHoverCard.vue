<template>
  <a-popover
    :open="open"
    placement="rightTop"
    :trigger="['hover', 'focus']"
    :mouse-enter-delay="0.3"
    :arrow="false"
    :overlay-inner-style="overlayStyle"
    @update:open="updateOpen"
  >
    <template #content>
      <div class="workspace-preview">
        <div class="workspace-preview__title">{{ group.displayName }}</div>
        <div class="workspace-preview__directory">
          <img :src="directoryIcon" alt="" aria-hidden="true" />
          <span class="workspace-preview__path" :title="group.fullPath">{{ group.fullPath || t('workflows.task.common.unsetDirectory') }}</span>
        </div>
      </div>
    </template>
    <div
      class="workspace-preview-trigger"
      @mouseenter="suppressed = false"
      @click.capture="closePreview"
      @focusin="showOnFocus"
      @focusout="open = false"
      @keydown.esc="closePreview"
      @keydown.enter="closePreview"
      @keydown.space="closePreview"
    >
      <slot />
    </div>
  </a-popover>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useAppI18n } from '@/i18n'
import type { ConversationGroup } from '@/stores/conversation'
import directoryIcon from '@/assets/icons/workspace-hover-directory.svg'

const props = defineProps<{ group: ConversationGroup }>()
const { t } = useAppI18n()
const route = useRoute()
const open = ref(false)
let suppressed = false
const overlayStyle = {
  width: '220px',
  maxWidth: 'calc(100vw - 48px)',
  padding: '12px',
  borderRadius: '6px',
  background: '#fff',
  boxShadow: '0 1px 10px rgba(0,0,0,.05), 0 4px 5px rgba(0,0,0,.08), 0 2px 4px -1px rgba(0,0,0,.12)',
}

function updateOpen(value: boolean) { open.value = value && !suppressed }
function closePreview() { suppressed = true; open.value = false }
function showOnFocus() { suppressed = false; open.value = true }

watch(() => [route.fullPath, props.group.key], closePreview)
</script>

<style scoped>
.workspace-preview-trigger { min-width: 0; }
.workspace-preview { display: flex; flex-direction: column; gap: 8px; }
.workspace-preview__title { color: #262626; font-size: 14px; font-weight: 500; line-height: 22px; overflow-wrap: anywhere; }
.workspace-preview__directory { display: flex; align-items: center; gap: 8px; color: #8c8c8c; font-size: 12px; line-height: 20px; }
.workspace-preview__directory img { display: block; flex-shrink: 0; }
.workspace-preview__path { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
