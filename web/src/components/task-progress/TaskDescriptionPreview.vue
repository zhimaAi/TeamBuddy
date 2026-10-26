<template>
  <section v-if="task.description && !task.created_from_conversation" class="description-preview">
    <div class="description-heading">
      <FileTextOutlined /><strong>{{ t('workflows.task.workspace.taskDetails') }}</strong>
      <button type="button" :aria-label="t('workflows.task.detail.edit')" @click="emit('edit')"><EditOutlined /></button>
      <button v-if="overflowing" type="button" :aria-expanded="expanded" :aria-label="t(expanded ? 'workflows.task.detail.collapse' : 'workflows.task.detail.expand')" @click="expanded = !expanded"><UpOutlined v-if="expanded" /><DownOutlined v-else /></button>
      <button
        type="button"
        class="description-documents"
        :title="t('workflows.task.progress.documents')"
        :aria-label="t('workflows.task.progress.documents')"
        @click="emit('documents')"
      >
        {{ t('workflows.task.progress.documents') }}
      </button>
    </div>
    <div ref="contentRef" class="description-body" :class="{ expanded }" @click="previewImage">
      <div v-if="usesHtml" v-html="renderedDescription" />
      <MarkdownPreview v-else :content="task.description" :task-uuid="task.uuid" @attachments-loaded="measure" />
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { DownOutlined, EditOutlined, FileTextOutlined, UpOutlined } from '@ant-design/icons-vue'
import MarkdownIt from 'markdown-it'
import MarkdownPreview from '@/components/MarkdownPreview.vue'
import { useAppI18n } from '@/i18n'
import type { TaskWithDetails } from '@/types/task-detail'

const props = defineProps<{ task: TaskWithDetails }>()
const emit = defineEmits<{ edit: []; documents: []; 'preview-image': [url: string] }>()
const { t } = useAppI18n()
const expanded = ref(false)
const overflowing = ref(false)
const contentRef = ref<HTMLElement>()
const md = new MarkdownIt({ breaks: true, linkify: true })
const usesHtml = computed(() => /<[a-z][\s\S]*>/i.test(props.task.description || ''))
const renderedDescription = computed(() => usesHtml.value ? props.task.description || '' : md.render(props.task.description || ''))
let observer: ResizeObserver | undefined
async function measure() {
  await nextTick()
  const content = contentRef.value
  if (content) overflowing.value = content.scrollHeight > 66 + 1
}
function previewImage(event: MouseEvent) {
  if (event.target instanceof HTMLImageElement) emit('preview-image', event.target.src)
}
watch(() => props.task.uuid, () => { expanded.value = false })
watch(() => props.task.description, measure)
onMounted(() => {
  observer = new ResizeObserver(() => { void measure() })
  if (contentRef.value) observer.observe(contentRef.value)
  void measure()
})
onBeforeUnmount(() => observer?.disconnect())
</script>

<style scoped>
.description-preview { margin: 12px 0; }
.description-heading { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; color: #8c8c8c; font-size: 12px; line-height: 20px; }
.description-heading strong { font-weight: 400; }
.description-heading button { display: inline-flex; width: 24px; height: 24px; align-items: center; justify-content: center; padding: 0; border: 0; border-radius: 6px; color: #8c8c8c; background: transparent; cursor: pointer; }
.description-heading button:hover { color: #262626; background: #f0f0f0; }
.description-heading button:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
/* 产出文档入口：与编辑按钮并列的轻量文字按钮（Vibe Coding 等没有输入框的形态也需要入口） */
.description-heading .description-documents { width: auto; height: 22px; padding: 0 8px; border: 1px solid #e5e7eb; border-radius: 6px; color: #595959; background: #fff; font-size: 12px; line-height: 20px; white-space: nowrap; }
.description-heading .description-documents:hover { color: #3157e2; border-color: #c7d2fe; background: #f5f7ff; }
.description-body { display: -webkit-box; overflow: hidden; max-height: 66px; color: #262626; font-size: 14px; line-height: 22px; -webkit-box-orient: vertical; -webkit-line-clamp: 3; overflow-wrap: anywhere; }
.description-body.expanded { display: block; max-height: none; -webkit-line-clamp: unset; }
.description-body :deep(.markdown-preview-shell) { min-height: 0; padding: 0; background: transparent; }
.description-body :deep(.markdown-preview), .description-body :deep(.markdown-preview-content), .description-body :deep(p), .description-body :deep(li) { margin-top: 0; margin-bottom: 0; font-size: inherit; line-height: inherit; }
.description-body :deep(ol), .description-body :deep(ul) { margin: 0; padding-left: 24px; line-height: inherit; }
.description-body :deep(img) { max-width: 100px; max-height: 60px; border: 1px solid #d9d9d9; border-radius: 3px; object-fit: cover; cursor: pointer; }
</style>
