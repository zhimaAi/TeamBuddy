<template>
  <a-modal
    :open="open"
    :footer="null"
    :closable="false"
    :body-style="{ padding: 0 }"
    :width="922"
    :z-index="1100"
    centered
    wrap-class-name="team-requirement-edit-modal"
    @cancel="emit('update:open', false)"
  >
    <div v-if="open" class="requirement-edit-body">
      <div class="edit-title-heading">
        <a-input
          v-model:value="draft.title"
          :bordered="false"
          :aria-label="t('teamwork.configure.taskTitle')"
          :placeholder="t('workflows.task.create.titleRequired')"
          class="title-input"
        />
        <a-button
          type="text"
          :aria-label="t('workflows.task.detail.closeEdit')"
          @click="emit('update:open', false)"
        >
          <template #icon><CloseOutlined /></template>
        </a-button>
      </div>
      <div class="description-box">
        <TaskDescriptionEditor
          ref="descriptionEditor"
          v-model="draft.description"
          :placeholder="t('workflows.task.create.description')"
          :on-upload-images="onUploadImages"
        />
      </div>
    </div>
    <footer class="modal-footer">
      <a-button @click="emit('update:open', false)">{{ t('common.actions.cancel') }}</a-button>
      <a-button type="primary" @click="save">{{ t('common.actions.save') }}</a-button>
    </footer>
  </a-modal>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { CloseOutlined } from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import TaskDescriptionEditor from '@/components/TaskDescriptionEditor.vue'
import { useAppI18n } from '@/i18n'

const props = defineProps<{
  open: boolean
  title: string
  description: string
  onUploadImages: (files: File[]) => Promise<string[]>
}>()
const emit = defineEmits<{
  'update:open': [value: boolean]
  saved: [value: { title: string; description: string }]
}>()
const { t } = useAppI18n()
const draft = reactive({ title: '', description: '' })
const descriptionEditor = ref<{ getValue: () => string }>()

watch(() => props.open, (open) => {
  if (open) Object.assign(draft, { title: props.title, description: props.description })
}, { immediate: true })

function save() {
  if (!draft.title.trim()) return message.warning(t('workflows.task.create.titleRequired'))
  emit('saved', {
    title: draft.title.trim(),
    description: descriptionEditor.value?.getValue() ?? draft.description,
  })
  emit('update:open', false)
}
</script>

<style scoped>
.requirement-edit-body {
  display: flex;
  height: min(548px, calc(100vh - 160px));
  min-height: 0;
  box-sizing: border-box;
  flex-direction: column;
  overflow: hidden;
  padding: 24px;
}

.edit-title-heading {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 12px;
  padding-bottom: 8px;
  border-bottom: 1px solid #3157e2;
}

.title-input {
  min-width: 0;
  height: 32px;
  flex: 1;
  padding: 0;
  border-radius: 0;
  color: #262626;
  font-size: 22px;
  font-weight: 600;
  line-height: 32px;
}

.description-box {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  margin-top: 16px;
}

.modal-footer {
  display: flex;
  height: 52px;
  box-sizing: border-box;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  padding: 0 24px;
  border-top: 1px solid #d9d9d9;
}

:global(.team-requirement-edit-modal .ant-modal) {
  max-width: calc(100vw - 32px);
  padding-bottom: 0;
}

:global(.team-requirement-edit-modal .ant-modal-content) {
  overflow: hidden;
  padding: 0;
  border-radius: 16px;
}
</style>
