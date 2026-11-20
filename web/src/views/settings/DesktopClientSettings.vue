<script setup lang="ts">
import { computed, h, onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'ant-design-vue'
import {
  getDesktopCloseBehavior,
  restartDesktopApp,
  selectDirectory,
  setDesktopCloseBehavior,
  type DesktopCloseBehavior,
} from '@/composables/useDesktop'
import { apiClient, ApiError } from '@/api/client'
import { useAppI18n } from '@/i18n'
import CodexReportingSettings from './CodexReportingSettings.vue'
import { useUpdateStore } from '@/stores/update'

const { t } = useAppI18n()
const updateStore = useUpdateStore()
const currentVersion = computed(() => updateStore.state?.currentVersion
  ? `v${updateStore.state.currentVersion}` : '—')

const closeBehavior = ref<DesktopCloseBehavior>('hide')
const savedCloseBehavior = ref<DesktopCloseBehavior>('hide')
const loading = ref(true)
const saving = ref(false)
const errorMessage = ref('')

interface WorkspaceInfo {
  current_root: string
  default_root: string
  relocated: boolean
  is_desktop: boolean
  os: string
  dir_name: string
  error?: string
}

const workspace = reactive<WorkspaceInfo>({
  current_root: '',
  default_root: '',
  relocated: false,
  is_desktop: true,
  os: '',
  dir_name: '.teambuddy',
})
const workspaceLoading = ref(true)
const workspaceError = ref('')
const relocating = ref(false)
const restarting = ref(false)
const pendingRoot = ref('')
const restartPostponed = ref(false)

// 当前工作空间的父目录，作为目录选择器的默认打开位置。
const workspaceParent = computed(() => {
  const root = workspace.current_root
  if (!root) return undefined
  const idx = Math.max(root.lastIndexOf('/'), root.lastIndexOf('\\'))
  return idx > 0 ? root.slice(0, idx) : undefined
})

async function loadCloseBehavior() {
  loading.value = true
  errorMessage.value = ''
  try {
    const loadedCloseBehavior = await getDesktopCloseBehavior()
    closeBehavior.value = loadedCloseBehavior
    savedCloseBehavior.value = loadedCloseBehavior
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : t('settings.readDesktopFailed')
  } finally {
    loading.value = false
  }
}

async function saveCloseBehavior() {
  const nextBehavior = closeBehavior.value
  saving.value = true
  errorMessage.value = ''
  try {
    const savedBehavior = await setDesktopCloseBehavior(nextBehavior)
    closeBehavior.value = savedBehavior
    savedCloseBehavior.value = savedBehavior
  } catch (error) {
    closeBehavior.value = savedCloseBehavior.value
    errorMessage.value = error instanceof Error ? error.message : t('settings.saveDesktopFailed')
  } finally {
    saving.value = false
  }
}

async function loadWorkspace() {
  workspaceLoading.value = true
  workspaceError.value = ''
  try {
    const data = await apiClient.get<WorkspaceInfo>('/config/workspace')
    Object.assign(workspace, data)
    if (data.error) workspaceError.value = data.error
  } catch (error) {
    // 搬迁已经落盘时，刷新失败只说明当前进程还没切到新目录，不能当成迁移失败。
    if (pendingRoot.value) return
    workspaceError.value =
      error instanceof ApiError ? error.message : t('settings.workspaceLoadFailed')
  } finally {
    workspaceLoading.value = false
  }
}

function confirmAndRelocate(parentDir: string) {
  const targetRoot =
    workspace.os === 'windows'
      ? parentDir.replace(/\//g, '\\') + '\\' + workspace.dir_name
      : parentDir.replace(/\/+$/, '') + '/' + workspace.dir_name

  Modal.confirm({
    title: t('settings.workspaceConfirmTitle'),
    content: () =>
      h('div', { class: 'workspace-confirm' }, [
        h(
          'p',
          t('settings.workspaceConfirmMove', { from: workspace.current_root, to: targetRoot }),
        ),
        h('p', { class: 'workspace-confirm__warn' }, t('settings.workspaceConfirmWarn')),
        h('p', { class: 'workspace-confirm__hint' }, t('settings.workspaceConfirmRestart')),
      ]),
    okText: t('settings.workspaceConfirmOk'),
    cancelText: t('common.actions.cancel'),
    okButtonProps: { danger: true },
    async onOk() {
      await doRelocate(parentDir)
    },
  })
}

async function restartNow() {
  restarting.value = true
  workspaceError.value = ''
  try {
    await restartDesktopApp()
  } catch (error) {
    restarting.value = false
    workspaceError.value =
      error instanceof Error ? error.message : t('settings.workspaceRestartFailed')
  }
}

function restartLater() {
  restartPostponed.value = true
}

async function doRelocate(parentDir: string) {
  relocating.value = true
  workspaceError.value = ''
  restartPostponed.value = false
  try {
    const moved = await apiClient.put<{ current_root?: string }>('/config/workspace', {
      parent_dir: parentDir,
      confirm: true,
    })
    const nextRoot = moved.current_root || ''
    pendingRoot.value = nextRoot
    if (nextRoot) {
      workspace.current_root = nextRoot
      workspace.relocated = true
    }
    // 指针和目录在接口返回前已经写完。刷新只是更新展示，失败也不回滚搬迁。
    await loadWorkspace()
    Modal.confirm({
      title: t('settings.workspaceDoneTitle'),
      content: t('settings.workspaceDoneContent', { path: nextRoot || workspace.current_root }),
      okText: t('settings.workspaceRestartNow'),
      cancelText: t('settings.workspaceRestartLater'),
      async onOk() {
        await restartNow()
      },
      onCancel() {
        restartLater()
      },
    })
  } catch (error) {
    workspaceError.value =
      error instanceof ApiError ? error.message : t('settings.workspaceMoveFailed')
  } finally {
    relocating.value = false
  }
}

async function chooseWorkspaceDir() {
  workspaceError.value = ''
  let selected: string | undefined
  try {
    selected = await selectDirectory(workspaceParent.value)
  } catch (error) {
    workspaceError.value =
      error instanceof Error ? error.message : t('settings.workspacePickFailed')
    return
  }
  if (!selected) return
  confirmAndRelocate(selected)
}

onMounted(() => {
  void loadCloseBehavior()
  void loadWorkspace()
  void updateStore.initialize().catch(error => {
    message.error(error instanceof Error ? error.message : t('settings.updateFailed'))
  })
})

async function manualCheck() {
  try {
    const result = await updateStore.check(true)
    if (result?.status === 'current') message.success(t('settings.alreadyLatest', { version: `v${result.currentVersion}` }))
    else if (result?.status === 'error') message.error(result.error || t('settings.updateFailed'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('settings.updateFailed'))
  }
}
</script>

<template>
  <div class="desktop-client-settings">
    <a-card :title="t('settings.desktopClient')" :bordered="true">
      <a-alert
        v-if="errorMessage"
        type="error"
        show-icon
        :message="errorMessage"
        class="settings-card__alert"
      >
        <template #action>
          <a-button type="link" size="small" @click="loadCloseBehavior">{{ t('common.actions.retry') }}</a-button>
        </template>
      </a-alert>
      <a-spin :spinning="loading">
        <a-form layout="vertical">
          <a-form-item :label="t('settings.closeWindow')" :extra="t('settings.closeHint')">
            <a-radio-group v-model:value="closeBehavior" :disabled="loading || saving" @change="saveCloseBehavior">
              <a-radio value="hide">{{ t('settings.hideToTray') }}</a-radio>
              <a-radio value="quit">{{ t('settings.quit') }}</a-radio>
            </a-radio-group>
          </a-form-item>
        </a-form>
      </a-spin>
    </a-card>

    <CodexReportingSettings class="settings-card" />
    <a-card :title="t('settings.workspaceTitle')" :bordered="true" class="settings-card">
      <a-alert
        v-if="workspaceError"
        type="error"
        show-icon
        :message="workspaceError"
        class="settings-card__alert"
      >
        <template #action>
          <a-button
            type="link"
            size="small"
            @click="loadWorkspace"
            >{{ t('common.actions.retry') }}</a-button
          >
        </template>
      </a-alert>
      <a-spin :spinning="workspaceLoading">
        <a-form layout="vertical">
          <a-form-item :label="t('settings.workspaceCurrentLabel')" :extra="t('settings.workspaceHint')">
            <div class="workspace-directory-row">
              <a-input
                :value="pendingRoot || workspace.current_root"
                readonly
                :placeholder="t('settings.workspaceDirectoryPlaceholder')"
                :title="pendingRoot || workspace.current_root"
              />
              <a-button
                :loading="relocating"
                :disabled="workspaceLoading || relocating || restarting || !!pendingRoot || !!workspace.error"
                @click="chooseWorkspaceDir"
              >
                {{ t('settings.workspaceChangeButton') }}
              </a-button>
            </div>
            <div v-if="pendingRoot || workspace.relocated" class="workspace-status">
              <a-tag v-if="pendingRoot" color="orange">{{ t('settings.workspacePendingTag') }}</a-tag>
              <a-tag v-else color="blue">{{ t('settings.workspaceRelocatedTag') }}</a-tag>
            </div>
            <div
              v-if="pendingRoot"
              class="settings-card__restart"
            >
              <a-alert
                type="info"
                show-icon
                :message="
                  restartPostponed
                    ? t('settings.workspaceRestartLaterHint', { path: pendingRoot })
                    : t('settings.workspaceRestartHint', { path: pendingRoot })
                "
              />
              <div v-if="!restartPostponed" class="restart-actions">
                <a-button
                  type="primary"
                  :loading="restarting"
                  @click="restartNow"
                >
                  {{ t('settings.workspaceRestartNow') }}
                </a-button>
                <a-button
                  :disabled="restarting"
                  @click="restartLater"
                >
                  {{ t('settings.workspaceRestartLater') }}
                </a-button>
              </div>
              <div v-else class="restart-actions">
                <a-button
                  type="primary"
                  :loading="restarting"
                  @click="restartNow"
                >
                  {{ t('settings.workspaceRestartNow') }}
                </a-button>
              </div>
            </div>
          </a-form-item>
        </a-form>
      </a-spin>
    </a-card>

    <a-card :title="t('settings.versionUpdate')" :bordered="true" class="version-card">
      <div class="version-row">
        <div>
          <strong>{{ t('settings.currentVersion') }} {{ currentVersion }}</strong>
          <p>{{ t('settings.autoCheckHint') }}<a class="version-github-link" href="https://github.com/zhimaAi/TeamBuddy/releases/latest/" target="_blank" rel="noopener noreferrer">{{ t('settings.goToGitHub') }}</a></p>
        </div>
        <a-button :loading="updateStore.checking" @click="manualCheck">{{ t('settings.checkUpdate') }}</a-button>
      </div>
    </a-card>
  </div>
</template>

<style scoped>
.desktop-client-settings {
  max-width: 800px;
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.version-row { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.version-row strong { color: #262626; }
.version-row p { margin: 8px 0 0; color: #8c8c8c; }
.version-github-link { margin-left: 4px; color: #3157e2; }

.settings-card__alert {
  margin-bottom: 16px;
}

.settings-card__restart {
  margin-top: 12px;
}

.restart-actions {
  display: flex;
  gap: 8px;
  margin-top: 12px;
}

.workspace-directory-row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.workspace-directory-row :deep(.ant-input) {
  flex: 1;
  min-width: 0;
}

.workspace-status {
  margin-top: 8px;
}

:global(.workspace-confirm p) {
  margin: 0 0 8px;
}

:global(.workspace-confirm__warn) {
  color: #d46b08;
}

:global(.workspace-confirm__hint) {
  color: rgba(0, 0, 0, 0.45);
  font-size: 12px;
}
</style>
