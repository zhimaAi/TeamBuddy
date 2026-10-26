<template>
  <TiptapMarkdownEditor
    ref="editor"
    :model-value="editorValue"
    :placeholder="placeholder"
    :disabled="disabled"
    :on-upload-images="onUploadImages"
    variant="compact"
    @update:model-value="handleChange"
    @ready="handleReady"
  />
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import TiptapMarkdownEditor from '@/components/TiptapMarkdownEditor.vue'
import { isLegacyRichTextHtml } from '@/utils/richText'

const props = withDefaults(defineProps<{
  modelValue?: string
  placeholder?: string
  disabled?: boolean
  onUploadImages?: (files: File[]) => Promise<string[]>
}>(), {
  modelValue: '',
  placeholder: '',
  disabled: false,
})
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const editor = ref<{
  getValue: () => string
  htmlToMarkdown: (value: string) => string
}>()
const editorValue = ref(props.modelValue)
let ready = false
let touched = false

function loadValue(value: string) {
  editorValue.value = ready && isLegacyRichTextHtml(value)
    ? editor.value?.htmlToMarkdown(value) ?? value
    : value
}

function handleReady() {
  ready = true
  loadValue(props.modelValue)
}

function handleChange(value: string) {
  touched = true
  editorValue.value = value
  emit('update:modelValue', value)
}

watch(() => props.modelValue, (value) => {
  if (value === editorValue.value) return
  touched = false
  loadValue(value)
})

defineExpose({
  getValue() {
    // HTML 仅转换为编辑草稿，未编辑正文时保存原文，避免格式被自动重写。
    return touched ? editor.value?.getValue() ?? editorValue.value : props.modelValue
  },
})
</script>
