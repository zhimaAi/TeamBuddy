<template>
  <a-dropdown v-model:open="open" :trigger="['click']" :disabled="disabled || switching">
    <button type="button" class="branch-picker" :disabled="disabled || switching"
      :title="t('workflows.task.progress.chooseBranch')" :aria-label="t('workflows.task.progress.chooseBranch')"
      aria-haspopup="menu" :aria-expanded="open">
      <LoadingOutlined v-if="loading || switching" spin /><img v-else class="branch-icon" :src="branchIcon" alt="" aria-hidden="true" />
      <span>{{ branchLabel }}</span>
      <DownOutlined />
    </button>
    <template #overlay>
      <div class="git-branch-menu" role="menu">
        <button
          v-if="error"
          type="button"
          class="git-branch-item"
          role="menuitem"
          @click="load"
        >
          {{ error }} <span class="branch-retry">{{ t('common.actions.retry') }}</span>
        </button>
        <template v-else>
          <button
            v-for="branch in branches"
            :key="branch"
            type="button"
            class="git-branch-item"
            :class="{ 'is-current': branch === current }"
            :disabled="switching || branch === current"
            role="menuitem"
            :aria-current="branch === current ? 'true' : undefined"
            @click="selectBranch(branch)"
          >{{ branch }}</button>
          <div v-if="!branches.length" class="git-branch-item is-empty">{{ branchLabel }}</div>
        </template>
      </div>
    </template>
  </a-dropdown>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { DownOutlined, LoadingOutlined } from '@ant-design/icons-vue'
import branchIcon from '@/assets/icons/task-composer-git-branch.svg'
import apiClient from '@/api/client'
import { useAppI18n } from '@/i18n'

interface BranchesResponse { current: string; branches: string[]; available: boolean; detached: boolean }
const { t } = useAppI18n()
const props = defineProps<{ taskUuid: string; disabled?: boolean }>()
const emit = defineEmits<{ busy: [value: boolean] }>()
const open = ref(false)
const loading = ref(false)
const switching = ref(false)
const current = ref('')
const branches = ref<string[]>([])
const available = ref(false)
const detached = ref(false)
const error = ref('')
let requestVersion = 0
let disposed = false
const branchLabel = computed(() => current.value || (loading.value ? t('workflows.task.progress.loadingBranches')
  : error.value ? t('workflows.task.progress.branchesLoadFailed')
  : detached.value ? t('workflows.task.progress.detachedHead') : available.value
    ? t('workflows.task.progress.noBranches') : t('workflows.task.progress.notGitRepository')))

async function load() {
  if (!props.taskUuid || switching.value) return
  const version = ++requestVersion
  loading.value = true
  error.value = ''
  try {
    const result = await apiClient.get<BranchesResponse>(`/tasks/${encodeURIComponent(props.taskUuid)}/git/branches`)
    if (version !== requestVersion) return
    current.value = result.current || ''
    branches.value = result.branches || []
    available.value = result.available
    detached.value = result.detached
  } catch (cause) {
    if (version === requestVersion) error.value = cause instanceof Error ? cause.message : t('workflows.task.progress.branchesLoadFailed')
  } finally {
    if (version === requestVersion) loading.value = false
  }
}

async function selectBranch(branch: string) {
  if (props.disabled || switching.value || branch === current.value || !branches.value.includes(branch)) return
  const taskUuid = props.taskUuid
  let switched = false
  switching.value = true
  emit('busy', true)
  try {
    await apiClient.post(`/tasks/${encodeURIComponent(taskUuid)}/git/checkout`, { branch })
    if (disposed || taskUuid !== props.taskUuid) return
    current.value = branch
    detached.value = false
    switched = true
    message.success(t('workflows.task.progress.branchSwitched', { branch }))
    open.value = false
  } catch (cause) {
    if (!disposed && taskUuid === props.taskUuid) {
      message.error(cause instanceof Error ? cause.message : t('workflows.task.progress.branchSwitchFailed'))
    }
  } finally {
    switching.value = false
    if (!disposed) {
      emit('busy', false)
      if (switched || taskUuid !== props.taskUuid) void load()
    }
  }
}

watch(() => props.taskUuid, () => {
  requestVersion++
  current.value = ''
  branches.value = []
  available.value = false
  detached.value = false
  error.value = ''
  loading.value = false
  open.value = false
  void load()
}, { immediate: true })
watch(open, (value) => { if (value) void load() })
onBeforeUnmount(() => {
  disposed = true
  requestVersion++
  emit('busy', false)
})
</script>

<style scoped>
.branch-picker {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 180px;
  min-height: 24px;
  border: 0;
  border-radius: 6px;
  padding: 1px 6px;
  color: #595959;
  background: transparent;
  cursor: pointer;
  font-size: 12px;
  line-height: 20px;
}
.branch-icon { width: 14px; height: 14px; flex: 0 0 auto; }
.branch-picker:not(:disabled):hover { background: #f2f4f7; color: #262626; }
.branch-picker:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.branch-picker:disabled { opacity: 0.5; cursor: not-allowed; }
.branch-picker > span:not(.anticon) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.git-branch-menu {
  min-width: 160px;
  max-width: 420px;
  max-height: 300px;
  overflow: auto;
  padding: 4px;
  background: #fff;
  border-radius: 8px;
  box-shadow: 0 6px 16px rgba(0, 0, 0, 0.08);
}
.git-branch-item {
  display: block;
  width: 100%;
  overflow: hidden;
  border: 0;
  border-radius: 6px;
  padding: 5px 12px;
  color: #262626;
  background: transparent;
  text-align: start;
  text-overflow: ellipsis;
  white-space: nowrap;
  cursor: pointer;
  font-size: 14px;
  line-height: 22px;
}
.git-branch-item:hover:not(:disabled) { background: #f5f5f5; }
.git-branch-item.is-current { color: #3157e2; font-weight: 500; }
.git-branch-item:disabled { cursor: default; }
.git-branch-item.is-empty { color: #8c8c8c; cursor: default; }
.git-branch-item:focus-visible { outline: 2px solid #3157e2; outline-offset: -2px; }
.branch-retry { margin-inline-start: 8px; color: #3157e2; }
</style>
