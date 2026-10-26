<template>
  <div v-if="images.length" class="composer-images" :aria-label="t('workflows.task.progress.attachedFiles')">
    <div v-for="image in images" :key="image.id" class="image-card">
      <button type="button" class="image-preview" :title="image.name" :aria-label="image.name" @click="previewId = image.id">
        <img v-if="!failedImages[image.id]" :src="image.previewUrl" :alt="image.name" @error="failedImages[image.id] = true" />
        <span v-else class="image-fallback"><PictureOutlined /><span>{{ image.name }}</span></span>
      </button>
      <button
        type="button"
        class="image-remove"
        :disabled="disabled"
        :title="t('workflows.task.progress.removeAttachment', { name: image.name })"
        :aria-label="t('workflows.task.progress.removeAttachment', { name: image.name })"
        @click="emit('remove', image.id)"
      ><CloseOutlined aria-hidden="true" /></button>
    </div>
  </div>
  <a-modal :open="Boolean(previewImage)" :footer="null" width="min(90vw, 900px)" centered destroy-on-close @update:open="previewId = ''">
    <img v-if="previewImage" class="full-image" :src="previewImage.previewUrl" :alt="previewImage.name" />
  </a-modal>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { CloseOutlined, PictureOutlined } from '@ant-design/icons-vue'
import type { TaskAttachmentItem } from '@/composables/useTaskImageAttachments'
import { useAppI18n } from '@/i18n'

const props = defineProps<{ items: TaskAttachmentItem[]; disabled?: boolean }>()
const emit = defineEmits<{ remove: [id: string] }>()
const { t } = useAppI18n()
const images = computed(() => props.items.filter((item) => item.isImage && item.previewUrl))
const previewId = ref('')
const previewImage = computed(() => images.value.find((image) => image.id === previewId.value))
const failedImages = ref<Record<string, boolean>>({})
watch(images, (items) => {
  if (!items.some((item) => item.id === previewId.value)) previewId.value = ''
  failedImages.value = Object.fromEntries(items.filter((item) => failedImages.value[item.id]).map((item) => [item.id, true]))
})
</script>

<style scoped>
.composer-images {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  min-width: 0;
  max-height: 320px;
  overflow-y: auto;
  padding: 4px 4px 8px;
}
.image-card {
  position: relative;
  flex: 0 0 144px;
  width: 144px;
  height: 144px;
  max-width: 100%;
}
.image-preview {
  position: relative;
  display: block;
  width: 100%;
  height: 100%;
  overflow: hidden;
  padding: 0;
  border: 1px solid #d9d9d9;
  border-radius: 12px;
  background: #f5f6f8;
  cursor: pointer;
}
.image-preview img {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
}
.image-preview::after {
  position: absolute;
  inset: 0;
  border-radius: inherit;
  background: rgba(0, 0, 0, 0.08);
  opacity: 0;
  pointer-events: none;
  content: '';
}
.image-card:hover .image-preview::after { opacity: 1; }
.image-remove {
  position: absolute;
  top: -4px;
  right: -4px;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  padding: 0;
  border: 2px solid #fff;
  border-radius: 50%;
  color: #fff;
  background: #262626;
  font-size: 12px;
  cursor: pointer;
  opacity: 0;
  pointer-events: none;
}
.image-card:hover .image-remove { opacity: 1; pointer-events: auto; }
.image-remove:hover:not(:disabled) { background: #595959; }
.image-remove:disabled { background: #8c8c8c; cursor: not-allowed; }
.image-preview:focus-visible,
.image-remove:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.image-fallback {
  display: flex;
  height: 100%;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 8px;
  padding: 12px;
  color: #595959;
  font-size: 12px;
  overflow-wrap: anywhere;
}
.image-fallback > span:last-child { max-width: 100%; }
.full-image { display: block; max-width: 100%; max-height: 80vh; margin: auto; object-fit: contain; }
@media (max-width: 640px) {
  .image-card { flex-basis: 112px; width: 112px; height: 112px; }
  .composer-images { max-height: 256px; }
}
</style>
