<template>
  <a-button
    v-if="cliType"
    type="link"
    size="small"
    :loading="loading"
    :disabled="disabled"
    @click.stop="readModels"
  >
    {{ t(reason === 'login_required' ? 'components.cliSelect.readModelsAgain' : 'components.cliSelect.readModels') }}
  </a-button>
  <small role="status">{{ notice }}</small>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useAppI18n } from '@/i18n'
import { useCLIModelCatalogStore } from '@/stores/cli-models'

const props = defineProps<{ cliType: string; taskUuid?: string; channelType?: string; disabled?: boolean }>()
const { t } = useAppI18n()
const directories = useCLIModelCatalogStore()
const scope = computed(() => ({ taskUuid: props.taskUuid, channelType: props.channelType }))
const loading = computed(() => directories.isLoading(props.cliType, scope.value))
const catalog = computed(() => directories.catalog(props.cliType, scope.value))
const reason = computed(() => catalog.value?.catalog_meta?.reason)
const notice = computed(() => {
  if (reason.value) return t(`components.cliSelect.readIssue.${reason.value}`)
  return directories.error(props.cliType, scope.value)
    || (catalog.value && !catalog.value.models.length ? t('components.cliSelect.noReadableModels') : '')
})

async function readModels() {
  try {
    await directories.read(props.cliType, scope.value)
  } catch {
    // Store 的错误在当前字段旁展示。
  }
}
</script>
