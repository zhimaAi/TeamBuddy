<template>
  <div class="codex-message">
    <div v-if="content.attachments.length" class="message-attachments">
      <div v-for="attachment in content.attachments" :key="attachment.index" class="attachment-card" :class="{ 'attachment-card-image': attachment.is_image }">
        <button v-if="attachment.is_image" type="button" class="attachment-image" :class="{ 'attachment-image-failed': images[attachment.index]?.failed }" :disabled="!images[attachment.index]?.url" :aria-label="t('components.markdown.previewAttachment', { name: attachment.name })" @click="previewIndex = attachment.index">
          <img v-if="images[attachment.index]?.url" :src="images[attachment.index]?.url" :alt="attachment.name" @load="emit('attachments-loaded')" />
          <span v-else-if="images[attachment.index]?.failed" class="attachment-image-status">{{ t('components.markdown.imageUnavailable') }}</span>
          <a-spin v-else size="small" />
        </button>
        <span v-if="!attachment.is_image" class="attachment-name" :title="attachment.name">{{ attachment.name }}</span>
        <div v-if="!attachment.is_image || images[attachment.index]?.failed" class="attachment-actions">
          <a-button v-if="images[attachment.index]?.failed" size="small" type="link" @click="loadImage(attachment)">{{ t('common.actions.retry') }}</a-button>
          <a-button v-if="!attachment.is_image" size="small" type="link" @click="revealFile(attachment)">{{ t('components.markdown.revealInFolder') }}</a-button>
        </div>
      </div>
    </div>
    <MarkdownPreview v-if="content.text" :content="content.text" :task-uuid="taskUuid" :session-uuid="sessionUuid" :file-roots="fileRoots" image-thumbnails @attachments-loaded="emit('attachments-loaded')" />
    <a-modal :open="Boolean(previewImage)" :title="previewImage?.name" :footer="null" width="min(90vw, 1000px)" centered destroy-on-close @update:open="previewIndex = -1">
      <img v-if="previewImage" class="full-image" :src="previewImage.url" :alt="previewImage.name" />
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import apiClient from '@/api/client'
import MarkdownPreview from '@/components/MarkdownPreview.vue'
import { revealTaskOutputFile } from '@/composables/useDesktop'
import { useAppI18n } from '@/i18n'
import type { TaskProgress } from '@/types/pipeline'

type CodexContent = NonNullable<TaskProgress['codex_message']>
type Attachment = CodexContent['attachments'][number]
const props = withDefaults(defineProps<{ content: CodexContent; taskUuid: string; progressUuid: string; sessionUuid?: string; fileRoots?: string[] }>(), { sessionUuid: '', fileRoots: () => [] })
const emit = defineEmits<{ 'attachments-loaded': [] }>()
const { t } = useAppI18n()
const images = ref<Record<number, { url: string; failed: boolean }>>({})
const previewIndex = ref(-1)
const previewImage = computed(() => {
  const attachment = props.content.attachments.find(item => item.index === previewIndex.value)
  const url = images.value[previewIndex.value]?.url
  return attachment && url ? { name: attachment.name, url } : undefined
})
let controller: AbortController | undefined

async function loadImage(attachment: Attachment, signal = controller?.signal) {
  images.value[attachment.index] = { url: '', failed: false }
  try {
    const blob = await apiClient.getBlob(`/tasks/${encodeURIComponent(props.taskUuid)}/progress/${encodeURIComponent(props.progressUuid)}/attachments/${attachment.index}`, undefined, signal)
    if (signal?.aborted) return
    if (!['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(blob.type)) throw new Error('Invalid image')
    images.value[attachment.index] = { url: URL.createObjectURL(blob), failed: false }
  } catch {
    if (!signal?.aborted) images.value[attachment.index] = { url: '', failed: true }
  }
  if (!signal?.aborted) emit('attachments-loaded')
}

function clearImages() {
  controller?.abort()
  for (const image of Object.values(images.value)) if (image.url) URL.revokeObjectURL(image.url)
  images.value = {}
  previewIndex.value = -1
}

watch(() => [props.taskUuid, props.progressUuid, JSON.stringify(props.content.attachments)] as const, () => {
  clearImages()
  controller = new AbortController()
  const signal = controller.signal
  const attachments = props.content.attachments.filter(item => item.is_image)
  let next = 0
  void Promise.all(Array.from({ length: Math.min(4, attachments.length) }, async () => {
    while (next < attachments.length && !signal.aborted) {
      const attachment = attachments[next++]
      if (attachment) await loadImage(attachment, signal)
    }
  }))
}, { immediate: true })

async function revealFile(attachment: Attachment) {
  try {
    const result = await revealTaskOutputFile(props.taskUuid, attachment.relative_path, props.sessionUuid)
    if (result === 'copied') message.info(t('components.markdown.revealUseDesktop'))
  } catch (error) {
    message.error(error instanceof Error && error.message ? error.message : t('components.markdown.revealFailed'))
  }
}
onBeforeUnmount(clearImages)
</script>

<style scoped>
.codex-message { white-space: normal; }
.message-attachments { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 12px; }
.attachment-card { width: 180px; max-width: 100%; overflow: hidden; border: 1px solid #d9d9d9; border-radius: 12px; background: #fff; }
.attachment-card-image { position: relative; box-sizing: border-box; flex: 0 0 160px; width: 160px; height: 120px; border-radius: 8px; background: #f5f6f8; }
.attachment-image { display: flex; align-items: center; justify-content: center; width: 100%; height: 100%; padding: 0; border: 0; background: #f5f6f8; cursor: zoom-in; }
.attachment-image-failed { padding-bottom: 28px; }
.attachment-image:disabled { cursor: default; }
.attachment-image img { display: block; width: 100%; height: 100%; object-fit: contain; }
.attachment-image-status { padding: 8px; color: #595959; font-size: 12px; }
.attachment-image:focus-visible { outline: 2px solid #3157e2; outline-offset: -2px; }
.attachment-name { display: block; overflow: hidden; padding: 8px 12px 0; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
.attachment-actions { display: flex; flex-wrap: wrap; padding: 4px; }
.attachment-card-image .attachment-actions { position: absolute; inset: auto 0 4px; justify-content: center; padding: 0; }
.full-image { display: block; max-width: 100%; max-height: 80vh; margin: auto; object-fit: contain; }
</style>
