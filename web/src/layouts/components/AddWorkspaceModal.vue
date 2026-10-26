<template>
  <a-modal :open="open" :title="t('layout.sidebar.addWorkspace')" :confirm-loading="saving" :closable="!saving" :mask-closable="!saving" :keyboard="!saving" :cancel-button-props="{ disabled: saving }" @ok="save" @cancel="emit('update:open', false)">
    <a-form layout="vertical" @submit.prevent="save">
      <a-form-item :label="t('layout.sidebar.workspaceDirectory')" :validate-status="error ? 'error' : undefined" :help="error">
        <a-input v-model:value="directory" :disabled="saving" @press-enter="save" />
      </a-form-item>
      <a-button v-if="isDesktopRuntime()" :loading="picking" :disabled="saving" @click="pick">{{ t('layout.sidebar.chooseDirectory') }}</a-button>
    </a-form>
  </a-modal>
</template>
<script setup lang="ts">
import { ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { useAppI18n } from '@/i18n'
import { useConversationStore } from '@/stores/conversation'
import { isDesktopRuntime, selectDirectory } from '@/composables/useDesktop'
const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()
const { t } = useAppI18n()
const store = useConversationStore()
const directory = ref('')
const error = ref('')
const saving = ref(false)
const picking = ref(false)
let dialogVersion = 0
watch(() => props.open, open => { dialogVersion++; if (open) { directory.value = ''; error.value = '' } })
async function pick() {
  if (picking.value) return
  picking.value = true
  const version = dialogVersion
  try { const path = await selectDirectory(directory.value || undefined); if (path && props.open && version === dialogVersion) directory.value = path }
  catch (err) { if (version === dialogVersion) error.value = err instanceof Error ? err.message : t('layout.sidebar.workspaceActionFailed') }
  finally { picking.value = false }
}
async function save() {
  if (saving.value) return
  const path = directory.value.trim()
  if (!path) { error.value = t('layout.sidebar.workspaceDirectoryRequired'); return }
  saving.value = true; error.value = ''
  try {
    await store.updateWorkspace(path, { display_name: '', create_only: true })
    store.recordWorkingDir(path)
    emit('update:open', false)
    message.success(t('layout.sidebar.workspaceSaved'))
  } catch (err) { error.value = err instanceof Error ? err.message : t('layout.sidebar.workspaceActionFailed') }
  finally { saving.value = false }
}
</script>
