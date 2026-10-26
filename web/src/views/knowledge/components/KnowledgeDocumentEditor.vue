<template>
  <div class="knowledge-document-editor" :style="editorStyle">
    <textarea
      v-show="mode === 'source' && markdown"
      ref="sourceRef"
      class="source-editor"
      :value="modelValue"
      :aria-label="t('knowledge.sourceMode')"
      spellcheck="false"
      :disabled="disabled"
      @input="updateSource"
    />
    <TiptapMarkdownEditor
      v-show="mode !== 'source' || !markdown"
      ref="editorRef"
      :model-value="modelValue"
      :title-placeholder="markdown ? t('knowledge.titlePlaceholder') : ''"
      :custom-colors="true"
      :disabled="disabled"
      :default-text-color="textColor"
      @update:model-value="emit('update:modelValue', $event)"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import TiptapMarkdownEditor from '@/components/TiptapMarkdownEditor.vue'
import { useAppI18n } from '@/i18n'

const props = defineProps<{
  modelValue: string
  disabled: boolean
  markdown: boolean
  mode: 'visual' | 'source'
  fontSize: number
  lineHeight: number
  textColor: string
}>()
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const { t } = useAppI18n()
const editorRef = ref<InstanceType<typeof TiptapMarkdownEditor>>()
const sourceRef = ref<HTMLTextAreaElement>()
const editorStyle = computed(() => ({
  '--knowledge-font-size': `${props.fontSize}px`,
  '--knowledge-line-height': String(props.lineHeight),
  '--knowledge-text-color': props.textColor,
}))

function updateSource(event: Event) {
  const target = event.target
  if (target instanceof HTMLTextAreaElement) emit('update:modelValue', target.value)
}

function scrollToHeading(index: number) {
  if (props.mode !== 'source') {
    editorRef.value?.scrollToHeading(index)
    return
  }
  const match = Array.from(props.modelValue.matchAll(/^#{1,6}\s+.+$/gm))[index]
  const source = sourceRef.value
  if (!match || !source) return
  source.focus()
  source.setSelectionRange(match.index, match.index + match[0].length)
  const line = props.modelValue.slice(0, match.index).split('\n').length - 1
  source.scrollTop = line * props.fontSize * props.lineHeight
}

defineExpose({ scrollToHeading })
</script>

<style scoped>
.knowledge-document-editor { display: flex; flex: 1; flex-direction: column; height: 100%; min-height: 0; overflow: hidden; }
.source-editor {
  display: block;
  width: 100%;
  height: 100%;
  resize: none;
  padding: 24px 48px 64px;
  border: 0;
  outline: none;
  background: #fff;
  color: var(--knowledge-text-color);
  font: var(--knowledge-font-size) / var(--knowledge-line-height) SFMono-Regular, Consolas, 'Liberation Mono', monospace;
  tab-size: 2;
}
.knowledge-document-editor :deep(.ProseMirror) {
  color: var(--knowledge-text-color);
  font-size: var(--knowledge-font-size);
  line-height: var(--knowledge-line-height);
}
.knowledge-document-editor :deep(.ProseMirror :is(p, ul, ol, blockquote)) {
  padding-top: 6px;
  font-size: inherit;
  line-height: inherit;
}
.knowledge-document-editor :deep(.ProseMirror :is(h1, h2, h3, h4, h5, h6)) {
  color: inherit;
  line-height: 1.5;
  margin: 16px 0 4px;
  letter-spacing: normal;
}
.knowledge-document-editor :deep(.ProseMirror h1) { font-size: 1.7em; }
.knowledge-document-editor :deep(.ProseMirror h2) { font-size: 1.45em; }
.knowledge-document-editor :deep(.ProseMirror h3) { font-size: 1.25em; }
.knowledge-document-editor :deep(.ProseMirror :is(h4, h5, h6)) { font-size: 1.1em; }
@media (max-width: 700px) {
  .source-editor { padding: 16px 16px 48px; }
}
</style>
