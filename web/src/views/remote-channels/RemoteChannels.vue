<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Modal, message } from 'ant-design-vue'
import { SettingOutlined } from '@ant-design/icons-vue'
import apiClient from '@/api/client'
import { useLocalWS } from '@/composables/useLocalWebSocket'
import { useAppI18n } from '@/i18n'
import DirectoryPicker, { type DirectoryPickerCandidate } from '@/components/DirectoryPicker.vue'
import { useConversationStore, getLastDirSegment } from '@/stores/conversation'
import {
  getChannelDocsUrl,
  getChannelPlatformUrl,
  openExternalLink,
} from '@/utils/channelLinks'

type ChannelStatus = 'disconnected' | 'connecting' | 'connected' | 'error' | 'disabled'
type LoginStatus = 'waiting' | 'scanned' | 'verifying' | 'need_verifycode' | 'expired' | 'error' | 'connected'

// 通用通道状态接口（用于 wecom/feishu/dingtalk）
interface GenericChannelStatus {
  status: ChannelStatus
  connected_at: number
  bot_id?: string
  last_error?: string
}

// IN-001: WeChatStatus 不再返回 CLI 默认配置，CLI 配置走独立接口并行加载
interface WeChatStatus {
  status: ChannelStatus
  connected_at: number
  login_id: string
  last_error: string
  reply_message_id_missing: boolean
  has_credential: boolean
}

interface LoginAttempt {
  id: string
  status: LoginStatus
  qr_image: string
  error?: string
}

interface UncertainRun {
  message_id: string
  task_uuid: string
  task_title: string
  received_at: number
}

// S-UI001, IN-002: CLI 发现项接口
interface CLIDiscoveryItem {
  type: string
  name: string
  installed: boolean
  exec_path: string
  version: string
  models: string[]
}

// S-UI001, IN-002: CLI 配置接口
interface CLIConfig {
  cli_type: string
  cli_model: string
  workspace: string
  enabled: boolean
}

const { t, d } = useAppI18n()
const conversationStore = useConversationStore()
const status = ref<ChannelStatus>('disconnected')
const lastError = ref('')
const replyMessageIDMissing = ref(false)
const hasCredential = ref(false)
const loading = ref(true)
const loadError = ref(false)
const loginOpen = ref(false)
const login = ref<LoginAttempt | null>(null)
const loginBusy = ref(false)
const verifyBusy = ref(false)
const verifyCode = ref('')
const uncertainRuns = ref<UncertainRun[]>([])
const uncertainLoading = ref(false)
const uncertainError = ref(false)
const acknowledgeBusy = ref<string | null>(null)

// S-UI001, IN-001, IN-002, IN-003, IN-004, IN-005: CLI 配置相关状态
const cliConfigOpen = ref(false)
const cliDiscoveryList = ref<CLIDiscoveryItem[]>([])
const cliDiscoveryLoading = ref(false)
const selectedCLItype = ref('')
const selectedCLImodel = ref('')
const selectedWorkspace = ref('')
const cliConfigEnabled = ref(true)
const cliModels = ref<string[]>([])
// 下拉选项右侧展示的模型个数：/tasks/cli-discovery 不下发模型列表，需要按 CLI 逐个拉取，
// 与「创建任务」的 CLI 下拉（useCliModelOptions.prefetchModelCounts）使用同一数据来源。
const cliModelCounts = ref<Record<string, number>>({})
const cliModelCountFlights = new Map<string, Promise<string[]>>()
const cliConfigSaving = ref(false)
const cliConfigToggling = ref(false)

let loginTimer: ReturnType<typeof setInterval> | undefined
let statusTimer: ReturnType<typeof setInterval> | undefined
let uncertainRequestVersion = 0
let statusRequestVersion = 0
// IN-001: CLI 配置独立加载，独立的请求版本号用于丢弃过期响应
let cliConfigRequestVersion = 0
// 备份当前已保存的 CLI 配置，关闭弹窗时丢弃临时编辑并还原
let cliTypeBackup = ''
let cliModelBackup = ''
let workspaceBackup = ''
let cliEnabledBackup = true
// UI-008: 首次点「连接」（数据库尚无连接授权数据，即未保存过凭据）时先引导配置 CLI 兜底；
// 该标记表示当前 CLI 弹窗属于引导流程，保存或跳过后需要继续弹出二维码扫码弹窗。
const wechatCLIGatePending = ref(false)

// 企业微信通道状态
const wecomStatus = ref<ChannelStatus>('disconnected')
const wecomConfigOpen = ref(false)
const wecomConfigBusy = ref(false)
const wecomVerifyBusy = ref(false)
const wecomDisableBusy = ref(false)
const wecomAppID = ref('')
const wecomAppSecret = ref('')
const wecomLastError = ref('')
// 已保存的 AppID 快照（来自后端状态表），用于连接弹窗反向渲染：已配置时 AppID 预填、Secret 留空。
const wecomSavedAppID = ref('')
let wecomStatusRequestVersion = 0
let wecomStatusTimer: ReturnType<typeof setInterval> | undefined

// 企业微信通道 CLI 兜底配置：与微信一致，整块面板在卡片上，弹窗与凭据弹窗分离
const wecomCLIEnabled = ref(false)
const wecomSelectedCLItype = ref('')
const wecomSelectedCLImodel = ref('')
const wecomSelectedWorkspace = ref('')
const wecomCLISaving = ref(false)
const wecomCLIToggling = ref(false)
const wecomCLIConfigOpen = ref(false)
const wecomCLIName = computed(() => {
  const item = cliDiscoveryList.value.find((cli) => cli.type === wecomSelectedCLItype.value)
  return item?.name || wecomSelectedCLItype.value
})
const wecomCLIConfigured = computed(() => wecomSelectedCLItype.value !== '' && wecomSelectedCLImodel.value !== '')
let wecomCLIConfigRequestVersion = 0
let wecomCLITypeBackup = ''
let wecomCLIModelBackup = ''
let wecomCLIWorkspaceBackup = ''
let wecomCLIEnabledBackup = true
// UI-008: 首次点「连接」（尚未保存过凭据）时先引导配置 CLI 兜底；该标记表示当前 CLI 弹窗属于引导流程，
// 保存或跳过后需要继续弹出凭据配置弹窗。
const wecomCLIGatePending = ref(false)

// 飞书通道状态
const feishuStatus = ref<ChannelStatus>('disconnected')
const feishuConfigOpen = ref(false)
const feishuConfigBusy = ref(false)
const feishuVerifyBusy = ref(false)
const feishuDisableBusy = ref(false)
const feishuAppID = ref('')
const feishuAppSecret = ref('')
const feishuLastError = ref('')
// 已保存的 AppID 快照（来自后端状态表），用于连接弹窗反向渲染：已配置时 AppID 预填、Secret 留空。
const feishuSavedAppID = ref('')
let feishuStatusRequestVersion = 0
let feishuStatusTimer: ReturnType<typeof setInterval> | undefined

// 飞书通道 CLI 兜底配置
const feishuCLIEnabled = ref(false)
const feishuSelectedCLItype = ref('')
const feishuSelectedCLImodel = ref('')
const feishuSelectedWorkspace = ref('')
const feishuCLISaving = ref(false)
const feishuCLIToggling = ref(false)
const feishuCLIConfigOpen = ref(false)
const feishuCLIName = computed(() => {
  const item = cliDiscoveryList.value.find((cli) => cli.type === feishuSelectedCLItype.value)
  return item?.name || feishuSelectedCLItype.value
})
const feishuCLIConfigured = computed(() => feishuSelectedCLItype.value !== '' && feishuSelectedCLImodel.value !== '')
let feishuCLIConfigRequestVersion = 0
let feishuCLITypeBackup = ''
let feishuCLIModelBackup = ''
let feishuCLIWorkspaceBackup = ''
let feishuCLIEnabledBackup = true
// UI-008: 同企业微信，首次点「连接」时先引导配置 CLI 兜底，再进入凭据配置弹窗
const feishuCLIGatePending = ref(false)

// 钉钉通道状态
const dingtalkStatus = ref<ChannelStatus>('disconnected')
const dingtalkConfigOpen = ref(false)
const dingtalkConfigBusy = ref(false)
const dingtalkVerifyBusy = ref(false)
const dingtalkDisableBusy = ref(false)
const dingtalkClientID = ref('')
const dingtalkClientSecret = ref('')
// 钉钉 robotCode：sessionWebhook 失效时通过 oToMessages/batchSend 主动推送的兜底凭据。
// 未填写时后端自动取 ClientID 作为默认值（robotCode 通常与 ClientID 相同）。
const dingtalkRobotCode = ref('')
const dingtalkLastError = ref('')
// 已保存的 ClientID 快照（来自后端状态表），用于连接弹窗反向渲染：已配置时 ClientID 预填、Secret 留空。
const dingtalkSavedClientID = ref('')
// 已保存的 robotCode 快照，弹窗打开时预填；用户留空时后端默认取 ClientID。
const dingtalkSavedRobotCode = ref('')
let dingtalkStatusRequestVersion = 0
let dingtalkStatusTimer: ReturnType<typeof setInterval> | undefined

// 钉钉通道 CLI 兜底配置
const dingtalkCLIEnabled = ref(false)
const dingtalkSelectedCLItype = ref('')
const dingtalkSelectedCLImodel = ref('')
const dingtalkSelectedWorkspace = ref('')
const dingtalkCLISaving = ref(false)
const dingtalkCLIToggling = ref(false)
const dingtalkCLIConfigOpen = ref(false)
const dingtalkCLIName = computed(() => {
  const item = cliDiscoveryList.value.find((cli) => cli.type === dingtalkSelectedCLItype.value)
  return item?.name || dingtalkSelectedCLItype.value
})
const dingtalkCLIConfigured = computed(() => dingtalkSelectedCLItype.value !== '' && dingtalkSelectedCLImodel.value !== '')
let dingtalkCLIConfigRequestVersion = 0
let dingtalkCLITypeBackup = ''
let dingtalkCLIModelBackup = ''
let dingtalkCLIWorkspaceBackup = ''
let dingtalkCLIEnabledBackup = true
// UI-008: 同企业微信，首次点「连接」时先引导配置 CLI 兜底，再进入凭据配置弹窗
const dingtalkCLIGatePending = ref(false)

const cards = computed(() => [
  { key: 'wechat', name: t('remoteChannels.wechat'), description: t('remoteChannels.wechatDescription'), mark: 'wechat', glyph: '' },
  { key: 'wecom', name: t('remoteChannels.wecom'), description: t('remoteChannels.wecomDescription'), mark: 'wecom', glyph: '企' },
  { key: 'feishu', name: t('remoteChannels.feishu'), description: t('remoteChannels.feishuDescription'), mark: 'feishu', glyph: '飞' },
  { key: 'dingtalk', name: t('remoteChannels.dingtalk'), description: t('remoteChannels.dingtalkDescription'), mark: 'dingtalk', glyph: '钉' },
  { key: 'app', name: t('remoteChannels.app'), description: t('remoteChannels.appDescription'), mark: 'app', glyph: '▣' },
])

// S-UI001, IN-002: CLI 下拉选项。展示全部发现项（含未安装），已安装的排在前面可选，
// 未安装的置后并禁用，便于用户知道还有哪些 CLI 可装；与 AgentEditorModal 的选择策略一致。
// 展示形态与「创建任务」的 CLI 下拉一致：左列名称、右列模型个数（未安装显示「未安装」），不再展示版本号。
const cliSelectOptions = computed(() => [...cliDiscoveryList.value]
  .sort((a, b) => Number(b.installed) - Number(a.installed))
  .map((cli) => ({
    value: cli.type,
    label: cli.name,
    installed: cli.installed,
    modelCount: cliModelCounts.value[cli.type],
    disabled: !cli.installed,
  })))

// S-UI001: 当前已配置 CLI 的展示名（用于卡片状态展示）
const currentCLIName = computed(() => {
  const item = cliDiscoveryList.value.find((cli) => cli.type === selectedCLItype.value)
  return item?.name || selectedCLItype.value
})

// UI-007: CLI 工作目录候选项。读取 conversationStore 的历史目录和项目本地目录作为候选，
// 不调用 recordWorkingDir，避免把兜底任务目录写进对话工作目录历史。
const workspaceCandidates = computed<DirectoryPickerCandidate[]>(() => {
  const normalize = (p: string) => (p || '').replace(/\\/g, '/').replace(/\/+$/, '').toLowerCase()
  const knownDirs = conversationStore.allKnownWorkDirs
  const projects = conversationStore.projects
  return knownDirs
    .map((dir) => {
      const matched = projects.find((p) => p.local_dir && normalize(p.local_dir) === normalize(dir))
      return {
        path: dir,
        projectName: matched ? matched.name : undefined,
      }
    })
    .sort((a, b) => {
      const aName = a.projectName || getLastDirSegment(a.path)
      const bName = b.projectName || getLastDirSegment(b.path)
      return aName.localeCompare(bName)
    })
})

const loginStatusText = computed(() => {
  switch (login.value?.status) {
    case 'scanned': return t('remoteChannels.scanned')
    case 'verifying': return t('remoteChannels.verifying')
    case 'need_verifycode': return t('remoteChannels.needVerify')
    case 'expired': return t('remoteChannels.expired')
    case 'error': return login.value.error || t('remoteChannels.connectFailed')
    default: return t('remoteChannels.waiting')
  }
})

const statusErrorText = computed(() => lastError.value === 'wechat_session_expired'
  ? t('remoteChannels.connectionLost')
  : t('remoteChannels.statusError'))

// S-UI001: CLI 是否已配置
const cliConfigured = computed(() => selectedCLItype.value !== '' && selectedCLImodel.value !== '')

function stopLoginPolling() {
  if (loginTimer) clearInterval(loginTimer)
  loginTimer = undefined
}

async function loadStatus(silent = false) {
  const requestVersion = ++statusRequestVersion
  if (!silent) loading.value = true
  try {
    const response = await apiClient.get<WeChatStatus>('/remote-channels/wechat')
    if (!response || !['disconnected', 'connecting', 'connected', 'disabled', 'error'].includes(response.status)) {
      throw new Error('Invalid channel status')
    }
    if (requestVersion !== statusRequestVersion) return
    status.value = response.status
    lastError.value = response.last_error
    replyMessageIDMissing.value = response.reply_message_id_missing === true
    // hasCredential：后端返回是否有保存的凭据，用于判断是否可以跳过扫码直接重新连接
    hasCredential.value = response.has_credential === true
    // IN-001: CLI 默认配置改为独立接口并行加载，不与 status 接口耦合
    loadError.value = false
  } catch {
    if (requestVersion === statusRequestVersion) loadError.value = true
  } finally {
    if (requestVersion === statusRequestVersion) loading.value = false
  }
}

useLocalWS('remote.wechat.status', () => { void loadStatus(true) })

// IN-001: 独立加载 CLI 配置，不依赖 status 接口；status 接口慢或失败时仍可显示已有配置
async function loadCLIConfig() {
  const requestVersion = ++cliConfigRequestVersion
  try {
    const response = await apiClient.get<CLIConfig>('/remote-channels/wechat/cli-config')
    if (!response || typeof response.cli_type !== 'string' || typeof response.cli_model !== 'string') {
      throw new Error('Invalid CLI config')
    }
    if (requestVersion !== cliConfigRequestVersion) return
    // 与 wecom/feishu/dingtalk 一致：加载时同步刷新备份快照，保证关闭弹窗还原的是「已保存值」，
    // 且默认工作区（后端未配置时回填的默认路径）在首次打开弹窗前就落到 selectedWorkspace。
    cliTypeBackup = response.cli_type
    cliModelBackup = response.cli_model
    workspaceBackup = response.workspace || ''
    cliEnabledBackup = response.enabled !== false
    selectedCLItype.value = response.cli_type
    selectedCLImodel.value = response.cli_model
    selectedWorkspace.value = response.workspace || ''
    cliConfigEnabled.value = response.enabled !== false
  } catch {
    // 静默失败：CLI 配置拉取失败不影响主状态展示，保留已有选择或默认空值
  }
}

async function loadUncertain() {
  const requestVersion = ++uncertainRequestVersion
  uncertainLoading.value = true
  uncertainError.value = false
  try {
    const response = await apiClient.get<{ items: UncertainRun[] }>('/remote-channels/wechat/uncertain')
    if (!response || !Array.isArray(response.items)) throw new Error('Invalid uncertain runs')
    if (requestVersion === uncertainRequestVersion) uncertainRuns.value = response.items
  } catch {
    if (requestVersion === uncertainRequestVersion) uncertainError.value = true
  } finally {
    if (requestVersion === uncertainRequestVersion) uncertainLoading.value = false
  }
}

function acknowledgeUncertain(run: UncertainRun) {
  if (acknowledgeBusy.value) return
  Modal.confirm({
    title: t('remoteChannels.uncertainConfirmTitle'),
    content: t('remoteChannels.uncertainConfirmDescription', { task: run.task_title || run.task_uuid || t('remoteChannels.uncertainTaskUnknown') }),
    okText: t('remoteChannels.uncertainAcknowledge'),
    okType: 'danger',
    onOk: async () => {
      acknowledgeBusy.value = run.message_id
      try {
        await apiClient.post('/remote-channels/wechat/uncertain/ack', { message_id: run.message_id })
        uncertainRuns.value = uncertainRuns.value.filter((item) => item.message_id !== run.message_id)
        await loadUncertain()
        message.success(t('remoteChannels.uncertainAcknowledged'))
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('remoteChannels.uncertainAckFailed'))
        throw error
      } finally {
        acknowledgeBusy.value = null
      }
    },
  })
}

async function pollLogin() {
  const id = login.value?.id
  if (!id || loginBusy.value) return
  try {
    const response = await apiClient.get<LoginAttempt>(`/remote-channels/wechat/login/${encodeURIComponent(id)}`)
    if (!response || response.id !== id || typeof response.status !== 'string') {
      throw new Error('Invalid login status')
    }
    if (login.value?.id !== id) return
    login.value = response
    if (response.status === 'connected') {
      stopLoginPolling()
      loginOpen.value = false
      status.value = 'connected'
      lastError.value = ''
      await loadStatus()
    } else if (response.status === 'expired' || response.status === 'error') {
      stopLoginPolling()
    }
  } catch {
    stopLoginPolling()
    if (login.value?.id === id) login.value = { ...login.value, status: 'error' }
  }
}

async function startLogin() {
  if (loginBusy.value) return
  loginBusy.value = true
  stopLoginPolling()
  verifyCode.value = ''
  login.value = null
  loginOpen.value = true
  try {
    const response = await apiClient.post<LoginAttempt>('/remote-channels/wechat/login', {})
    if (!response || !response.id || !response.qr_image) throw new Error(t('remoteChannels.connectFailed'))
    login.value = response
    status.value = 'connecting'
    loginTimer = setInterval(() => { void pollLogin() }, 2000)
  } catch (error) {
    login.value = { id: '', status: 'error', qr_image: '', error: error instanceof Error ? error.message : t('remoteChannels.connectFailed') }
  } finally {
    loginBusy.value = false
  }
}

// 微信通道：有凭据时直接重新连接（跳过扫码），用于 disabled 状态下点击"重新连接"按钮。
async function reloadWechat() {
  if (loginBusy.value) return
  loginBusy.value = true
  try {
    await apiClient.post<{ status: string }>('/remote-channels/wechat/reload', {})
    status.value = 'connecting'
    lastError.value = ''
    await loadStatus()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.reloadFailed'))
  } finally {
    loginBusy.value = false
  }
}

async function submitVerification() {
  if (!login.value?.id || !verifyCode.value.trim() || verifyBusy.value) return
  verifyBusy.value = true
  try {
    await apiClient.post(`/remote-channels/wechat/login/${encodeURIComponent(login.value.id)}/verify`, { code: verifyCode.value.trim() })
    verifyCode.value = ''
    login.value = { ...login.value, status: 'verifying' }
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.verifyFailed'))
  } finally {
    verifyBusy.value = false
  }
}

function closeLogin() {
  loginOpen.value = false
  void loadStatus()
}

// 微信通道：连接按钮（根据状态决定行为），与企业微信/飞书/钉钉三通道保持一致。
// connected 走断开，其余状态一律进入连接流程——包括 connecting 与 error。
function wechatAction() {
  if (status.value === 'connected') {
    disconnect()
    return
  }
  // UI-008: 首次连接（数据库无连接授权数据、未保存过凭据）时，先弹 CLI 兜底配置，
  // 配置或「稍后配置」后再弹二维码，方便客户先用兜底跑起来；已有凭据则直接进入扫码弹窗。
  // 想单独调整兜底配置点卡片上的 CLI 配置区即可（openCLIconfig）。
  if (!hasCredential.value) {
    wechatCLIGatePending.value = true
    void openCLIconfig()
    return
  }
  void startLogin()
}

function disconnect() {
  Modal.confirm({
    title: t('remoteChannels.disconnectTitle'),
    content: t('remoteChannels.disconnectDescription'),
    okText: t('remoteChannels.disconnect'),
    okType: 'danger',
    onOk: async () => {
      try {
        const result = await apiClient.delete<{ status: ChannelStatus; wechat_notified: boolean }>('/remote-channels/wechat')
        statusRequestVersion++
        // 后端在用户主动断开时会把统一状态表 status 置为 disabled，
        // 防止客户端启动时自动重连；这里同步反映在 UI 状态上。
        // 保留 hasCredential 为 true，以便用户可以点击"重新连接"跳过扫码。
        status.value = result.status || 'disabled'
        lastError.value = ''
        replyMessageIDMissing.value = false
        // 不清空 login.value，保留二维码信息（如果有的话），方便用户重新扫码
        stopLoginPolling()
        uncertainRequestVersion++
        uncertainLoading.value = false
        // S-UI001: 重新拉取状态，确保 CLI 配置在断开后仍被保留和显示
        await loadStatus()
        await loadUncertain()
        if (result.wechat_notified) {
          message.success(t('remoteChannels.disconnectSuccess'))
        } else {
          message.warning(t('remoteChannels.stopNotifyFailed'))
        }
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('remoteChannels.disconnectFailed'))
        throw error
      }
    },
  })
}

// IN-001: 打开 CLI 配置弹窗，先备份已保存的配置；关闭弹窗时会还原，避免卡片被临时编辑污染
async function openCLIconfig() {
  cliTypeBackup = selectedCLItype.value
  cliModelBackup = selectedCLImodel.value
  workspaceBackup = selectedWorkspace.value
  cliEnabledBackup = cliConfigEnabled.value
  if (!cliConfigured.value) cliConfigEnabled.value = true
  cliConfigOpen.value = true
  await loadCLIDiscovery()
}

// 企业微信通道：加载状态
async function loadWeComStatus() {
  const requestVersion = ++wecomStatusRequestVersion
  try {
    const response = await apiClient.get<GenericChannelStatus>('/remote-channels/wecom')
    if (requestVersion !== wecomStatusRequestVersion) return
    wecomStatus.value = response.status
    wecomLastError.value = response.last_error || ''
    wecomSavedAppID.value = response.bot_id || ''
  } catch {
    // 静默失败
  }
}

// 企业微信通道：打开连接弹窗。已配置过凭据时反向渲染 AppID（保留原 AppID 让用户看到当前生效配置），
// Secret 留空让用户决定是否重新输入；空 Secret 表示「保留原 Secret」由后端走重连路径。
async function openWeComConfig() {
  wecomAppID.value = wecomSavedAppID.value
  wecomAppSecret.value = ''
  wecomConfigOpen.value = true
}

// 企业微信通道：关闭连接弹窗
function closeWeComConfig() {
  wecomConfigOpen.value = false
  wecomAppID.value = ''
  wecomAppSecret.value = ''
}

// 企业微信通道：打开 CLI 兜底配置弹窗，先备份已保存的配置
async function openWecomCLIconfig() {
  wecomCLITypeBackup = wecomSelectedCLItype.value
  wecomCLIModelBackup = wecomSelectedCLImodel.value
  wecomCLIWorkspaceBackup = wecomSelectedWorkspace.value
  wecomCLIEnabledBackup = wecomCLIEnabled.value
  if (!wecomCLIConfigured.value) wecomCLIEnabled.value = true
  wecomCLIConfigOpen.value = true
  await loadCLIDiscovery()
}

// 企业微信通道：关闭 CLI 兜底配置弹窗，还原临时编辑
function closeWecomCLIconfig() {
  wecomCLIConfigOpen.value = false
  wecomSelectedCLItype.value = wecomCLITypeBackup
  wecomSelectedCLImodel.value = wecomCLIModelBackup
  wecomSelectedWorkspace.value = wecomCLIWorkspaceBackup
  wecomCLIEnabled.value = wecomCLIEnabledBackup
  // UI-008: 从「连接」入口进来的引导流程，保存与跳过都要接着弹凭据配置弹窗
  if (wecomCLIGatePending.value) {
    wecomCLIGatePending.value = false
    openWeComConfig()
  }
}

// 企业微信通道：加载 CLI 兜底配置
async function loadWecomCLIConfig() {
  const requestVersion = ++wecomCLIConfigRequestVersion
  try {
    const response = await apiClient.get<CLIConfig>('/remote-channels/wecom/cli-config')
    if (requestVersion !== wecomCLIConfigRequestVersion) return
    wecomCLITypeBackup = response.cli_type || ''
    wecomCLIModelBackup = response.cli_model || ''
    wecomCLIWorkspaceBackup = response.workspace || ''
    wecomCLIEnabledBackup = response.enabled !== false
    wecomSelectedCLItype.value = response.cli_type || ''
    wecomSelectedCLImodel.value = response.cli_model || ''
    wecomSelectedWorkspace.value = response.workspace || ''
    wecomCLIEnabled.value = response.enabled !== false
  } catch {
    // 静默失败：CLI 配置拉取失败不影响主状态展示
  }
}

// 企业微信通道：保存 CLI 兜底配置
async function saveWecomCLIconfig() {
  if (!wecomSelectedCLItype.value) {
    message.warning(t('remoteChannels.selectCliType'))
    return
  }
  if (!wecomSelectedCLImodel.value) {
    message.warning(t('remoteChannels.selectCliModel'))
    return
  }
  wecomCLISaving.value = true
  try {
    await apiClient.put('/remote-channels/wecom/cli-config', {
      cli_type: wecomSelectedCLItype.value,
      cli_model: wecomSelectedCLImodel.value,
      workspace: wecomSelectedWorkspace.value.trim(),
      enabled: wecomCLIEnabled.value,
    })
    wecomCLITypeBackup = wecomSelectedCLItype.value
    wecomCLIModelBackup = wecomSelectedCLImodel.value
    wecomCLIWorkspaceBackup = wecomSelectedWorkspace.value
    wecomCLIEnabledBackup = wecomCLIEnabled.value
    message.success(t('remoteChannels.cliConfigSaved'))
    closeWecomCLIconfig()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.cliConfigFailed'))
  } finally {
    wecomCLISaving.value = false
  }
}

// 企业微信通道：卡片上直接切换启用开关
async function toggleWecomCLIEnabled(next: boolean) {
  if (wecomCLIToggling.value) return
  const previous = wecomCLIEnabled.value
  wecomCLIEnabled.value = next
  wecomCLIToggling.value = true
  try {
    await apiClient.put('/remote-channels/wecom/cli-config', {
      cli_type: wecomSelectedCLItype.value,
      cli_model: wecomSelectedCLImodel.value,
      enabled: next,
    })
    wecomCLIEnabledBackup = next
  } catch (error) {
    wecomCLIEnabled.value = previous
    message.error(error instanceof Error ? error.message : t('remoteChannels.cliConfigFailed'))
  } finally {
    wecomCLIToggling.value = false
  }
}

// 企业微信通道：弹窗内 CLI 类型切换，清空已选 model 并加载新模型列表
function onWecomCLItypeChange(cliType: string) {
  wecomSelectedCLImodel.value = ''
  void loadCLIModels(cliType)
}

// 企业微信通道：验证配置
async function verifyWeComConfig() {
  if (!wecomAppID.value.trim() || !wecomAppSecret.value.trim()) {
    message.warning(t('remoteChannels.wecomFillAllFields'))
    return
  }
  wecomVerifyBusy.value = true
  try {
    await apiClient.post('/remote-channels/wecom/verify', {
      app_id: wecomAppID.value.trim(),
      app_secret: wecomAppSecret.value.trim(),
    })
    message.success(t('remoteChannels.wecomVerifySuccess'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.wecomVerifyFailed'))
  } finally {
    wecomVerifyBusy.value = false
  }
}

// 企业微信通道：连接。后端 AppSecret 留空表示"保留原 Secret"，此处不再强制必填；
// verify 仍要求完整凭据用于在线校验。上线通知目标由后端自动从 inbox 最近一条 sender_id 推断，
// 前端不再收集管理员 OpenID。
async function connectWeCom() {
  if (!wecomAppID.value.trim()) {
    message.warning(t('remoteChannels.wecomFillAllFields'))
    return
  }
  wecomConfigBusy.value = true
  // 连接结果通常在数秒内出现，窗口内 1s 快刷让用户立刻看到 connecting → connected/error
  startConnectFastPoll('wecom', loadWeComStatus)
  try {
    const response = await apiClient.post<{ status?: string }>('/remote-channels/wecom/connect', {
      app_id: wecomAppID.value.trim(),
      app_secret: wecomAppSecret.value.trim(),
    })
    // 只有后端确认 connected 才提示成功；connecting 表示仍在建连，
    // 失败会由后端返回 4xx 走 catch 分支，不会出现「失败却提示连接成功」。
    if (response?.status === 'connected') {
      message.success(t('remoteChannels.wecomConnectSuccess'))
      wecomConfigOpen.value = false
    } else {
      message.info(t('remoteChannels.connecting'))
    }
    await loadWeComStatus()
  } catch (error) {
    // 失败时保留弹窗，便于用户直接修正配置（常见原因：凭据带空格或填错）
    message.error(error instanceof Error ? error.message : t('remoteChannels.wecomConnectFailed'))
  } finally {
    wecomConfigBusy.value = false
  }
}

// 企业微信通道：停用
async function disableWeCom() {
  if (wecomDisableBusy.value) return
  Modal.confirm({
    title: t('remoteChannels.wecomDisableTitle'),
    content: t('remoteChannels.wecomDisableContent'),
    okText: t('remoteChannels.wecomDisable'),
    okType: 'danger',
    onOk: async () => {
      wecomDisableBusy.value = true
      try {
        await apiClient.post('/remote-channels/wecom/disable', {})
        message.success(t('remoteChannels.wecomDisabled'))
        await loadWeComStatus()
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('remoteChannels.wecomDisableFailed'))
        throw error
      } finally {
        wecomDisableBusy.value = false
      }
    },
  })
}

// 企业微信通道：连接按钮（根据状态决定行为）
// 除 connected（点击=停用）外，一律打开配置弹窗——包括 connecting 与 error。
// 旧实现只处理 disabled/disconnected，连接失败后状态停在 error 时点击不命中任何分支，
// 表现为「按钮点不动、弹窗弹不出来」，只能重启程序才能重新配置。
function wecomAction() {
  if (wecomStatus.value === 'connected') {
    disableWeCom()
    return
  }
  // UI-008: 没有保存过凭据时，第一次点「连接」先弹 CLI 兜底配置，再弹凭据配置，方便客户先用兜底跑起来；
  // 已有凭据则直接弹凭据配置。想单独调整兜底配置点卡片上的 CLI 配置区即可（openWecomCLIconfig）。
  if (!wecomSavedAppID.value) {
    wecomCLIGatePending.value = true
    void openWecomCLIconfig()
    return
  }
  openWeComConfig()
}

// 企业微信通道：在系统默认浏览器打开企业微信开放平台与配置步骤文档链接。
// 桌面端由 Electron 的 setWindowOpenHandler 自动改走 shell.openExternal。
function openChannelLink(url: string) {
  try {
    openExternalLink(url)
  } catch {
    message.error(t('remoteChannels.wecomOpenLinkFailed'))
  }
}

function openWecomPlatform() {
  openChannelLink(getChannelPlatformUrl('wecom'))
}

function openWecomGuide() {
  openChannelLink(getChannelDocsUrl('wecom'))
}

function openFeishuPlatform() {
  openChannelLink(getChannelPlatformUrl('feishu'))
}

function openFeishuGuide() {
  openChannelLink(getChannelDocsUrl('feishu'))
}

function openDingtalkPlatform() {
  openChannelLink(getChannelPlatformUrl('dingtalk'))
}

function openDingtalkGuide() {
  openChannelLink(getChannelDocsUrl('dingtalk'))
}

useLocalWS('remote.wecom.status', () => { void loadWeComStatus() })

// 飞书通道：加载状态
async function loadFeishuStatus() {
  const requestVersion = ++feishuStatusRequestVersion
  try {
    const response = await apiClient.get<GenericChannelStatus>('/remote-channels/feishu')
    if (requestVersion !== feishuStatusRequestVersion) return
    feishuStatus.value = response.status
    feishuLastError.value = response.last_error || ''
    feishuSavedAppID.value = response.bot_id || ''
  } catch {
    // 静默失败
  }
}

// 飞书通道：打开连接弹窗。已配置过凭据时反向渲染 AppID（保留原 AppID 让用户看到当前生效配置），
// Secret 留空让用户决定是否重新输入；空 Secret 表示「保留原 Secret」由后端走重连路径。
function openFeishuConfig() {
  feishuAppID.value = feishuSavedAppID.value
  feishuAppSecret.value = ''
  feishuConfigOpen.value = true
}

// 飞书通道：关闭连接弹窗
function closeFeishuConfig() {
  feishuConfigOpen.value = false
  feishuAppID.value = ''
  feishuAppSecret.value = ''
}

// 飞书通道：验证配置
async function verifyFeishuConfig() {
  if (!feishuAppID.value.trim() || !feishuAppSecret.value.trim()) {
    message.warning(t('remoteChannels.feishuFillAllFields'))
    return
  }
  feishuVerifyBusy.value = true
  try {
    await apiClient.post('/remote-channels/feishu/verify', {
      app_id: feishuAppID.value.trim(),
      app_secret: feishuAppSecret.value.trim(),
    })
    message.success(t('remoteChannels.feishuVerifySuccess'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.feishuVerifyFailed'))
  } finally {
    feishuVerifyBusy.value = false
  }
}

// 飞书通道：连接。后端 AppSecret 留空表示"保留原 Secret"，此处不再强制必填。
// 上线通知目标由后端自动从 inbox 最近一条 sender_id 推断，前端不再收集管理员 OpenID。
async function connectFeishu() {
  if (!feishuAppID.value.trim()) {
    message.warning(t('remoteChannels.feishuFillAllFields'))
    return
  }
  feishuConfigBusy.value = true
  // 连接结果通常在数秒内出现，窗口内 1s 快刷让用户立刻看到 connecting → connected/error
  startConnectFastPoll('feishu', loadFeishuStatus)
  try {
    const response = await apiClient.post<{ status?: string }>('/remote-channels/feishu/connect', {
      app_id: feishuAppID.value.trim(),
      app_secret: feishuAppSecret.value.trim(),
    })
    // 只有后端确认 connected 才提示成功；connecting 表示仍在建连，
    // 失败会由后端返回 4xx 走 catch 分支，不会出现「失败却提示连接成功」。
    if (response?.status === 'connected') {
      message.success(t('remoteChannels.feishuConnectSuccess'))
      feishuConfigOpen.value = false
    } else {
      message.info(t('remoteChannels.connecting'))
    }
    await loadFeishuStatus()
  } catch (error) {
    // 失败时保留弹窗，便于用户直接修正配置（常见原因：凭据带空格或填错）
    message.error(error instanceof Error ? error.message : t('remoteChannels.feishuConnectFailed'))
  } finally {
    feishuConfigBusy.value = false
  }
}

// 飞书通道：停用
function disableFeishu() {
  if (feishuDisableBusy.value) return
  Modal.confirm({
    title: t('remoteChannels.feishuDisableTitle'),
    content: t('remoteChannels.feishuDisableContent'),
    okText: t('remoteChannels.wecomDisable'),
    okType: 'danger',
    onOk: async () => {
      feishuDisableBusy.value = true
      try {
        await apiClient.post('/remote-channels/feishu/disable', {})
        message.success(t('remoteChannels.feishuDisableSuccess'))
        await loadFeishuStatus()
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('remoteChannels.feishuDisableFailed'))
        throw error
      } finally {
        feishuDisableBusy.value = false
      }
    },
  })
}

function feishuAction() {
  // 同 wecomAction：connected 才走停用，其余状态（含 connecting/error）都打开配置弹窗，
  // 否则连接失败后按钮会完全无响应。
  if (feishuStatus.value === 'connected') {
    disableFeishu()
    return
  }
  // UI-008: 同企业微信，首次连接先引导配置 CLI 兜底
  if (!feishuSavedAppID.value) {
    feishuCLIGatePending.value = true
    void openFeishuCLIconfig()
    return
  }
  openFeishuConfig()
}

useLocalWS('remote.feishu.status', () => { void loadFeishuStatus() })

// 飞书通道：加载 CLI 兜底配置
async function loadFeishuCLIConfig() {
  const requestVersion = ++feishuCLIConfigRequestVersion
  try {
    const response = await apiClient.get<CLIConfig>('/remote-channels/feishu/cli-config')
    if (requestVersion !== feishuCLIConfigRequestVersion) return
    feishuCLITypeBackup = response.cli_type || ''
    feishuCLIModelBackup = response.cli_model || ''
    feishuCLIWorkspaceBackup = response.workspace || ''
    feishuCLIEnabledBackup = response.enabled !== false
    feishuSelectedCLItype.value = response.cli_type || ''
    feishuSelectedCLImodel.value = response.cli_model || ''
    feishuSelectedWorkspace.value = response.workspace || ''
    feishuCLIEnabled.value = response.enabled !== false
  } catch {
    // 静默失败：CLI 配置拉取失败不影响主状态展示
  }
}

// 飞书通道：打开 CLI 兜底配置弹窗，先备份
async function openFeishuCLIconfig() {
  feishuCLITypeBackup = feishuSelectedCLItype.value
  feishuCLIModelBackup = feishuSelectedCLImodel.value
  feishuCLIWorkspaceBackup = feishuSelectedWorkspace.value
  feishuCLIEnabledBackup = feishuCLIEnabled.value
  if (!feishuCLIConfigured.value) feishuCLIEnabled.value = true
  feishuCLIConfigOpen.value = true
  await loadCLIDiscovery()
}

// 飞书通道：关闭 CLI 兜底配置弹窗，还原临时编辑
function closeFeishuCLIconfig() {
  feishuCLIConfigOpen.value = false
  feishuSelectedCLItype.value = feishuCLITypeBackup
  feishuSelectedCLImodel.value = feishuCLIModelBackup
  feishuSelectedWorkspace.value = feishuCLIWorkspaceBackup
  feishuCLIEnabled.value = feishuCLIEnabledBackup
  // UI-008: 从「连接」入口进来的引导流程，保存与跳过都要接着弹凭据配置弹窗
  if (feishuCLIGatePending.value) {
    feishuCLIGatePending.value = false
    openFeishuConfig()
  }
}

// 飞书通道：保存 CLI 兜底配置
async function saveFeishuCLIconfig() {
  if (!feishuSelectedCLItype.value) {
    message.warning(t('remoteChannels.selectCliType'))
    return
  }
  if (!feishuSelectedCLImodel.value) {
    message.warning(t('remoteChannels.selectCliModel'))
    return
  }
  feishuCLISaving.value = true
  try {
    await apiClient.put('/remote-channels/feishu/cli-config', {
      cli_type: feishuSelectedCLItype.value,
      cli_model: feishuSelectedCLImodel.value,
      workspace: feishuSelectedWorkspace.value.trim(),
      enabled: feishuCLIEnabled.value,
    })
    feishuCLITypeBackup = feishuSelectedCLItype.value
    feishuCLIModelBackup = feishuSelectedCLImodel.value
    feishuCLIWorkspaceBackup = feishuSelectedWorkspace.value
    feishuCLIEnabledBackup = feishuCLIEnabled.value
    message.success(t('remoteChannels.cliConfigSaved'))
    closeFeishuCLIconfig()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.cliConfigFailed'))
  } finally {
    feishuCLISaving.value = false
  }
}

// 飞书通道：卡片上直接切换启用开关
async function toggleFeishuCLIEnabled(next: boolean) {
  if (feishuCLIToggling.value) return
  const previous = feishuCLIEnabled.value
  feishuCLIEnabled.value = next
  feishuCLIToggling.value = true
  try {
    await apiClient.put('/remote-channels/feishu/cli-config', {
      cli_type: feishuSelectedCLItype.value,
      cli_model: feishuSelectedCLImodel.value,
      enabled: next,
    })
    feishuCLIEnabledBackup = next
  } catch (error) {
    feishuCLIEnabled.value = previous
    message.error(error instanceof Error ? error.message : t('remoteChannels.cliConfigFailed'))
  } finally {
    feishuCLIToggling.value = false
  }
}

// 飞书通道：弹窗内 CLI 类型切换，清空已选 model 并加载新模型列表
function onFeishuCLItypeChange(cliType: string) {
  feishuSelectedCLImodel.value = ''
  void loadCLIModels(cliType)
}

// 钉钉通道：加载状态
async function loadDingtalkStatus() {
  const requestVersion = ++dingtalkStatusRequestVersion
  try {
    const response = await apiClient.get<GenericChannelStatus & { robot_code?: string }>('/remote-channels/dingtalk')
    if (requestVersion !== dingtalkStatusRequestVersion) return
    dingtalkStatus.value = response.status
    dingtalkLastError.value = response.last_error || ''
    dingtalkSavedClientID.value = response.bot_id || ''
    dingtalkSavedRobotCode.value = response.robot_code || ''
  } catch {
    // 静默失败
  }
}

// 钉钉通道：打开连接弹窗。已配置过凭据时反向渲染 ClientID（保留原 ClientID 让用户看到当前生效配置），
// Secret 留空让用户决定是否重新输入；空 Secret 表示「保留原 Secret」由后端走重连路径。
// robotCode 同样反向渲染：上次配置过的值会预填，留空时后端默认取 ClientID。
function openDingtalkConfig() {
  dingtalkClientID.value = dingtalkSavedClientID.value
  dingtalkClientSecret.value = ''
  dingtalkRobotCode.value = dingtalkSavedRobotCode.value
  dingtalkConfigOpen.value = true
}

// 钉钉通道：关闭连接弹窗
function closeDingtalkConfig() {
  dingtalkConfigOpen.value = false
  dingtalkClientID.value = ''
  dingtalkClientSecret.value = ''
  dingtalkRobotCode.value = ''
}

// 钉钉通道：验证配置
async function verifyDingtalkConfig() {
  if (!dingtalkClientID.value.trim() || !dingtalkClientSecret.value.trim()) {
    message.warning(t('remoteChannels.dingtalkFillAllFields'))
    return
  }
  dingtalkVerifyBusy.value = true
  try {
    await apiClient.post('/remote-channels/dingtalk/verify', {
      client_id: dingtalkClientID.value.trim(),
      client_secret: dingtalkClientSecret.value.trim(),
    })
    message.success(t('remoteChannels.dingtalkVerifySuccess'))
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.dingtalkVerifyFailed'))
  } finally {
    dingtalkVerifyBusy.value = false
  }
}

// 钉钉通道：连接。后端 ClientSecret 留空表示"保留原 Secret"，此处不再强制必填。
// 上线通知目标由后端自动从 inbox 最近一条 sender_id 推断，前端不再收集管理员 StaffID。
// robotCode 未填写时后端默认取 ClientID（两者通常相同），用户也可自行填写不同的值。
async function connectDingtalk() {
  if (!dingtalkClientID.value.trim()) {
    message.warning(t('remoteChannels.dingtalkFillAllFields'))
    return
  }
  dingtalkConfigBusy.value = true
  // 连接结果通常在数秒内出现，窗口内 1s 快刷让用户立刻看到 connecting → connected/error
  startConnectFastPoll('dingtalk', loadDingtalkStatus)
  try {
    const response = await apiClient.post<{ status?: string }>('/remote-channels/dingtalk/connect', {
      client_id: dingtalkClientID.value.trim(),
      client_secret: dingtalkClientSecret.value.trim(),
      robot_code: dingtalkRobotCode.value.trim(),
    })
    // 只有后端确认 connected 才提示成功；connecting 表示仍在建连，
    // 失败会由后端返回 4xx 走 catch 分支，不会出现「失败却提示连接成功」。
    if (response?.status === 'connected') {
      message.success(t('remoteChannels.dingtalkConnectSuccess'))
      dingtalkConfigOpen.value = false
    } else {
      message.info(t('remoteChannels.connecting'))
    }
    await loadDingtalkStatus()
  } catch (error) {
    // 失败时保留弹窗，便于用户直接修正配置（常见原因：凭据带空格或填错）
    message.error(error instanceof Error ? error.message : t('remoteChannels.dingtalkConnectFailed'))
  } finally {
    dingtalkConfigBusy.value = false
  }
}

// 钉钉通道：停用
function disableDingtalk() {
  if (dingtalkDisableBusy.value) return
  Modal.confirm({
    title: t('remoteChannels.dingtalkDisableTitle'),
    content: t('remoteChannels.dingtalkDisableContent'),
    okText: t('remoteChannels.wecomDisable'),
    okType: 'danger',
    onOk: async () => {
      dingtalkDisableBusy.value = true
      try {
        await apiClient.post('/remote-channels/dingtalk/disable', {})
        message.success(t('remoteChannels.dingtalkDisableSuccess'))
        await loadDingtalkStatus()
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('remoteChannels.dingtalkDisableFailed'))
        throw error
      } finally {
        dingtalkDisableBusy.value = false
      }
    },
  })
}

function dingtalkAction() {
  // 同 wecomAction：connected 才走停用，其余状态（含 connecting/error）都打开配置弹窗。
  if (dingtalkStatus.value === 'connected') {
    disableDingtalk()
    return
  }
  // UI-008: 同企业微信，首次连接先引导配置 CLI 兜底
  if (!dingtalkSavedClientID.value) {
    dingtalkCLIGatePending.value = true
    void openDingtalkCLIconfig()
    return
  }
  openDingtalkConfig()
}

useLocalWS('remote.dingtalk.status', () => { void loadDingtalkStatus() })

// 钉钉通道：加载 CLI 兜底配置
async function loadDingtalkCLIConfig() {
  const requestVersion = ++dingtalkCLIConfigRequestVersion
  try {
    const response = await apiClient.get<CLIConfig>('/remote-channels/dingtalk/cli-config')
    if (requestVersion !== dingtalkCLIConfigRequestVersion) return
    dingtalkCLITypeBackup = response.cli_type || ''
    dingtalkCLIModelBackup = response.cli_model || ''
    dingtalkCLIWorkspaceBackup = response.workspace || ''
    dingtalkCLIEnabledBackup = response.enabled !== false
    dingtalkSelectedCLItype.value = response.cli_type || ''
    dingtalkSelectedCLImodel.value = response.cli_model || ''
    dingtalkSelectedWorkspace.value = response.workspace || ''
    dingtalkCLIEnabled.value = response.enabled !== false
  } catch {
    // 静默失败：CLI 配置拉取失败不影响主状态展示
  }
}

// 钉钉通道：打开 CLI 兜底配置弹窗，先备份
async function openDingtalkCLIconfig() {
  dingtalkCLITypeBackup = dingtalkSelectedCLItype.value
  dingtalkCLIModelBackup = dingtalkSelectedCLImodel.value
  dingtalkCLIWorkspaceBackup = dingtalkSelectedWorkspace.value
  dingtalkCLIEnabledBackup = dingtalkCLIEnabled.value
  if (!dingtalkCLIConfigured.value) dingtalkCLIEnabled.value = true
  dingtalkCLIConfigOpen.value = true
  await loadCLIDiscovery()
}

// 钉钉通道：关闭 CLI 兜底配置弹窗，还原临时编辑
function closeDingtalkCLIconfig() {
  dingtalkCLIConfigOpen.value = false
  dingtalkSelectedCLItype.value = dingtalkCLITypeBackup
  dingtalkSelectedCLImodel.value = dingtalkCLIModelBackup
  dingtalkSelectedWorkspace.value = dingtalkCLIWorkspaceBackup
  dingtalkCLIEnabled.value = dingtalkCLIEnabledBackup
  // UI-008: 从「连接」入口进来的引导流程，保存与跳过都要接着弹凭据配置弹窗
  if (dingtalkCLIGatePending.value) {
    dingtalkCLIGatePending.value = false
    openDingtalkConfig()
  }
}

// 钉钉通道：保存 CLI 兜底配置
async function saveDingtalkCLIconfig() {
  if (!dingtalkSelectedCLItype.value) {
    message.warning(t('remoteChannels.selectCliType'))
    return
  }
  if (!dingtalkSelectedCLImodel.value) {
    message.warning(t('remoteChannels.selectCliModel'))
    return
  }
  dingtalkCLISaving.value = true
  try {
    await apiClient.put('/remote-channels/dingtalk/cli-config', {
      cli_type: dingtalkSelectedCLItype.value,
      cli_model: dingtalkSelectedCLImodel.value,
      workspace: dingtalkSelectedWorkspace.value.trim(),
      enabled: dingtalkCLIEnabled.value,
    })
    dingtalkCLITypeBackup = dingtalkSelectedCLItype.value
    dingtalkCLIModelBackup = dingtalkSelectedCLImodel.value
    dingtalkCLIWorkspaceBackup = dingtalkSelectedWorkspace.value
    dingtalkCLIEnabledBackup = dingtalkCLIEnabled.value
    message.success(t('remoteChannels.cliConfigSaved'))
    closeDingtalkCLIconfig()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.cliConfigFailed'))
  } finally {
    dingtalkCLISaving.value = false
  }
}

// 钉钉通道：卡片上直接切换启用开关
async function toggleDingtalkCLIEnabled(next: boolean) {
  if (dingtalkCLIToggling.value) return
  const previous = dingtalkCLIEnabled.value
  dingtalkCLIEnabled.value = next
  dingtalkCLIToggling.value = true
  try {
    await apiClient.put('/remote-channels/dingtalk/cli-config', {
      cli_type: dingtalkSelectedCLItype.value,
      cli_model: dingtalkSelectedCLImodel.value,
      enabled: next,
    })
    dingtalkCLIEnabledBackup = next
  } catch (error) {
    dingtalkCLIEnabled.value = previous
    message.error(error instanceof Error ? error.message : t('remoteChannels.cliConfigFailed'))
  } finally {
    dingtalkCLIToggling.value = false
  }
}

// 钉钉通道：弹窗内 CLI 类型切换，清空已选 model 并加载新模型列表
function onDingtalkCLItypeChange(cliType: string) {
  dingtalkSelectedCLImodel.value = ''
  void loadCLIModels(cliType)
}

// IN-001: 关闭 CLI 配置弹窗时还原备份（丢弃临时编辑），保证卡片上的 CLI 配置始终反映已保存值
function closeCLIconfig() {
  cliConfigOpen.value = false
  selectedCLItype.value = cliTypeBackup
  selectedCLImodel.value = cliModelBackup
  selectedWorkspace.value = workspaceBackup
  cliConfigEnabled.value = cliEnabledBackup
  cliModels.value = []
  // UI-008: 从「连接」入口进来的引导流程，保存与跳过都要接着弹二维码扫码弹窗
  if (wechatCLIGatePending.value) {
    wechatCLIGatePending.value = false
    void startLogin()
  }
}

// S-UI001, IN-002: 加载 CLI 发现列表
async function loadCLIDiscovery() {
  cliDiscoveryLoading.value = true
  try {
    const response = await apiClient.get<{ items: CLIDiscoveryItem[] }>('/tasks/cli-discovery')
    if (response && Array.isArray(response.items)) {
      cliDiscoveryList.value = response.items
      // 已安装的 CLI 才需要模型个数；不阻塞发现列表的渲染
      prefetchCLIModelCounts(response.items.filter((item) => item.installed).map((item) => item.type))
    }
  } catch (error) {
    message.error(t('remoteChannels.cliDiscoveryFailed'))
  } finally {
    cliDiscoveryLoading.value = false
  }
}

// S-UI001: 逐 CLI 拉取模型列表并记录个数，供下拉选项右侧展示。
// 探测最长 45s，因此在途请求按 cliType 去重；失败静默，选项右侧回退为占位符「…」。
function prefetchCLIModelCounts(cliTypes: string[]) {
  for (const cliType of new Set(cliTypes.filter(Boolean))) {
    if (cliModelCountFlights.has(cliType)) continue
    const request = apiClient
      .get<{ models: string[] }>(`/tasks/cli-models?cli_type=${encodeURIComponent(cliType)}`)
      .then((response) => {
        const models = Array.isArray(response?.models) ? response.models : []
        if (models.length) cliModelCounts.value = { ...cliModelCounts.value, [cliType]: models.length }
        return models
      })
      .catch(() => [] as string[])
      .finally(() => {
        cliModelCountFlights.delete(cliType)
      })
    cliModelCountFlights.set(cliType, request)
  }
}

// S-UI001, IN-003: 仅加载 CLI 模型列表，不修改 selectedCLImodel。
// 用于程序化触发（如弹窗打开时重新拉模型、loadCLIConfig 完成后预热下拉）。
async function loadCLIModels(cliType: string) {
  if (!cliType) {
    cliModels.value = []
    return
  }
  try {
    const response = await apiClient.get<{ models: string[] }>(`/tasks/cli-models?cli_type=${encodeURIComponent(cliType)}`)
    if (response && Array.isArray(response.models)) {
      cliModels.value = response.models
    }
  } catch (error) {
    message.error(t('remoteChannels.cliModelsLoadFailed'))
  }
}

// S-UI001, IN-003: 用户在弹窗内切换 CLI 类型时调用，清空已选 model 并加载新模型列表。
// 仅由 a-select 的 @change 触发，避免任何程序化赋值（包括 loadCLIConfig / backup 还原）误清空 selectedCLImodel。
async function onCLItypeChange(cliType: string) {
  selectedCLImodel.value = ''
  await loadCLIModels(cliType)
}

// S-UI001, IN-004: 保存 CLI 配置
// S-UI001-fix: CLI 配置与连接状态无关，作为通道指令消息的兜底处理选项，可随时保存。
async function saveCLIconfig() {
  if (!selectedCLItype.value) {
    message.warning(t('remoteChannels.selectCliType'))
    return
  }
  if (!selectedCLImodel.value) {
    message.warning(t('remoteChannels.selectCliModel'))
    return
  }
  cliConfigSaving.value = true
  try {
    await apiClient.put('/remote-channels/wechat/cli-config', {
      cli_type: selectedCLItype.value,
      cli_model: selectedCLImodel.value,
      workspace: selectedWorkspace.value.trim(),
      enabled: cliConfigEnabled.value,
    })
    // IN-001: 保存成功时把当前值同步为备份，closeCLIconfig 还原时保留新值，卡片不会被覆盖回旧值
    cliTypeBackup = selectedCLItype.value
    cliModelBackup = selectedCLImodel.value
    workspaceBackup = selectedWorkspace.value
    cliEnabledBackup = cliConfigEnabled.value
    message.success(t('remoteChannels.cliConfigSaved'))
    closeCLIconfig()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('remoteChannels.cliConfigFailed'))
  } finally {
    cliConfigSaving.value = false
  }
}

// IN-006: 卡片上的开关切换。直接落库后回写本地状态，不打开弹窗。
async function toggleCLIEnabled(next: boolean) {
  if (cliConfigToggling.value) return
  const previous = cliConfigEnabled.value
  cliConfigEnabled.value = next
  cliConfigToggling.value = true
  try {
    await apiClient.put('/remote-channels/wechat/cli-config', {
      cli_type: selectedCLItype.value,
      cli_model: selectedCLImodel.value,
      workspace: selectedWorkspace.value.trim(),
      enabled: next,
    })
    cliEnabledBackup = next
  } catch (error) {
    cliConfigEnabled.value = previous
    message.error(error instanceof Error ? error.message : t('remoteChannels.cliConfigFailed'))
  } finally {
    cliConfigToggling.value = false
  }
}

// S-UI001, IN-005: 暂不配置
function skipCLIconfig() {
  closeCLIconfig()
}

// IN-001: 弹窗打开时预热模型列表。已选 model 必须保留（用 loadCLIModels 而非 onCLItypeChange，
// 否则 backup 还原 selectedCLItype 时会把 selectedCLImodel 又清掉一次）。
watch(cliConfigOpen, async (isOpen) => {
  if (isOpen && selectedCLItype.value) {
    await loadCLIModels(selectedCLItype.value)
  }
})
// 企微 CLI 弹窗打开时预热模型列表
watch(wecomCLIConfigOpen, async (isOpen) => {
  if (isOpen && wecomSelectedCLItype.value) {
    await loadCLIModels(wecomSelectedCLItype.value)
  }
})
// 飞书 CLI 弹窗打开时预热模型列表
watch(feishuCLIConfigOpen, async (isOpen) => {
  if (isOpen && feishuSelectedCLItype.value) {
    await loadCLIModels(feishuSelectedCLItype.value)
  }
})
// 钉钉 CLI 弹窗打开时预热模型列表
watch(dingtalkCLIConfigOpen, async (isOpen) => {
  if (isOpen && dingtalkSelectedCLItype.value) {
    await loadCLIModels(dingtalkSelectedCLItype.value)
  }
})

// ────────────────────────────────────────────────────
// 连接结果快刷：点击「确认连接」后 20 秒内每秒拉一次状态
// ────────────────────────────────────────────────────
// 后端 connect 接口会短暂等待首轮建连结果，之后状态还可能再花几秒才稳定
// （connecting → connected / error）。状态变化虽已由后端 WS 广播即时推送
//（remote.wecom.status 等），但广播依赖长连接，断线重连期间事件会丢，
// 所以这里加一层兜底：窗口内 1s 一次，窗口结束自动停止，之后回到既有的 10s 轮询。
const CONNECT_FAST_POLL_WINDOW_MS = 20_000
const CONNECT_FAST_POLL_INTERVAL_MS = 1_000
const connectFastPollTimers = new Map<string, { timer: ReturnType<typeof setInterval>; expire: ReturnType<typeof setTimeout> }>()

function stopConnectFastPoll(key: string) {
  const entry = connectFastPollTimers.get(key)
  if (!entry) return
  clearInterval(entry.timer)
  clearTimeout(entry.expire)
  connectFastPollTimers.delete(key)
}

// startConnectFastPoll 立即刷新一次并启动窗口内的 1s 快刷；
// 同一通道重复调用会重置窗口（例如用户连续点几次「确认连接」）。
function startConnectFastPoll(key: string, refresh: () => Promise<void>) {
  stopConnectFastPoll(key)
  void refresh()
  const timer = setInterval(() => { void refresh() }, CONNECT_FAST_POLL_INTERVAL_MS)
  const expire = setTimeout(() => { stopConnectFastPoll(key) }, CONNECT_FAST_POLL_WINDOW_MS)
  connectFastPollTimers.set(key, { timer, expire })
}

function stopAllConnectFastPolls() {
  for (const key of [...connectFastPollTimers.keys()]) stopConnectFastPoll(key)
}

onMounted(() => {
  void loadStatus()
  void loadUncertain()
  // IN-001: 各通道 CLI 配置并行独立加载，状态接口慢或失败时不影响 CLI 配置展示
  void loadCLIConfig()
  void loadWeComStatus()
  void loadWecomCLIConfig()
  void loadFeishuStatus()
  void loadFeishuCLIConfig()
  void loadDingtalkStatus()
  void loadDingtalkCLIConfig()
  statusTimer = setInterval(() => { void loadStatus(true) }, 10_000)
  wecomStatusTimer = setInterval(() => { void loadWeComStatus() }, 10_000)
  feishuStatusTimer = setInterval(() => { void loadFeishuStatus() }, 10_000)
  dingtalkStatusTimer = setInterval(() => { void loadDingtalkStatus() }, 10_000)
})
onUnmounted(() => {
  stopLoginPolling()
  if (statusTimer) clearInterval(statusTimer)
  if (wecomStatusTimer) clearInterval(wecomStatusTimer)
  if (feishuStatusTimer) clearInterval(feishuStatusTimer)
  if (dingtalkStatusTimer) clearInterval(dingtalkStatusTimer)
  statusRequestVersion++
  uncertainRequestVersion++
  cliConfigRequestVersion++
  wecomStatusRequestVersion++
  wecomCLIConfigRequestVersion++
  feishuStatusRequestVersion++
  feishuCLIConfigRequestVersion++
  dingtalkStatusRequestVersion++
  dingtalkCLIConfigRequestVersion++
  stopAllConnectFastPolls()
})
</script>

<template>
  <section class="remote-page">
    <header class="page-header"><h1>{{ t('remoteChannels.title') }}</h1></header>
    <div class="remote-content">
      <div class="intro-banner" role="note">
        <span class="info-symbol" aria-hidden="true"><svg width="12" height="12" viewBox="0 0 12 12" fill="none"><path d="M6 5v3.5M6 3.5h.01" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></span>
        <div><strong>{{ t('remoteChannels.intro') }}</strong><p>{{ t('remoteChannels.hint') }}</p></div>
      </div>

      <div v-if="loading" class="state-message">{{ t('remoteChannels.loading') }}</div>
      <div v-if="loadError" class="state-message">
        {{ t('remoteChannels.loadFailed') }}
        <a-button size="small" @click="loadStatus()">{{ t('remoteChannels.retry') }}</a-button>
      </div>
      <a-alert v-if="lastError" class="status-alert" type="warning" show-icon :message="statusErrorText" />
      <div class="channel-grid">
          <article v-for="card in cards" :key="card.key" class="channel-card">
            <div class="card-heading">
              <span class="channel-icon" :class="card.mark" aria-hidden="true">
                <svg v-if="card.mark === 'wechat'" viewBox="0 0 36 36" fill="none">
                  <ellipse cx="15" cy="15" rx="12" ry="9.5" fill="white" />
                  <path d="m8 23-1 5 5-3" fill="white" />
                  <ellipse cx="23.5" cy="21" rx="9.5" ry="7.5" fill="#d9f6e6" />
                  <path d="m28 27 1 4-4-3" fill="#d9f6e6" />
                  <circle cx="11" cy="14" r="1.1" fill="#16a36a" /><circle cx="19" cy="14" r="1.1" fill="#16a36a" />
                </svg>
                <span v-else>{{ card.glyph }}</span>
              </span>
              <h2>{{ card.name }}</h2>
              <span v-if="card.key === 'wechat'" class="wechat-action-wrap">
                <!-- disabled 状态且有凭据时显示"重新连接"，点击跳过扫码直接重连 -->
                <a-button v-if="status === 'disabled' && hasCredential" class="wechat-action" size="small" :loading="loginBusy" :disabled="loading || loadError" @click="reloadWechat">
                  {{ t('remoteChannels.reconnect') }}
                </a-button>
                <!-- 其他状态使用原有逻辑：connected 时断开；其余状态走 wechatAction，
                     首次连接（无凭据）先引导 CLI 兜底配置，再进入扫码弹窗 -->
                <a-button v-else class="wechat-action" :class="{ 'wechat-action-stop': status === 'connected', 'wechat-action-with-warning': status === 'connected' && replyMessageIDMissing }" size="small" :loading="loginBusy" :disabled="loading || loadError" :danger="status === 'connected'" @click="wechatAction">
                  {{ status === 'connected' ? t('remoteChannels.disconnect') : status === 'connecting' ? t('remoteChannels.connecting') : t('remoteChannels.connect') }}
                </a-button>
                <a-popover v-if="status === 'connected' && replyMessageIDMissing" :trigger="['hover', 'click']" placement="topRight">
                  <template #content><span class="wechat-reply-warning-text">{{ t('remoteChannels.replyMessageIDMissing') }}</span></template>
                  <button type="button" class="wechat-reply-warning" :aria-label="t('remoteChannels.replyMessageIDMissing')" />
                </a-popover>
              </span>
              <a-button v-if="card.key === 'wecom'" class="channel-action" size="small" :loading="wecomConfigBusy" :danger="wecomStatus === 'connected'" @click="wecomAction">
                {{ wecomStatus === 'connected' ? t('remoteChannels.wecomDisable') : wecomStatus === 'connecting' ? t('remoteChannels.connecting') : t('remoteChannels.connect') }}
              </a-button>
              <a-button v-if="card.key === 'feishu'" class="channel-action" size="small" :loading="feishuConfigBusy" :danger="feishuStatus === 'connected'" @click="feishuAction">
                {{ feishuStatus === 'connected' ? t('remoteChannels.feishuDisable') : feishuStatus === 'connecting' ? t('remoteChannels.connecting') : t('remoteChannels.connect') }}
              </a-button>
              <a-button v-if="card.key === 'dingtalk'" class="channel-action" size="small" :loading="dingtalkConfigBusy" :danger="dingtalkStatus === 'connected'" @click="dingtalkAction">
                {{ dingtalkStatus === 'connected' ? t('remoteChannels.dingtalkDisable') : dingtalkStatus === 'connecting' ? t('remoteChannels.connecting') : t('remoteChannels.connect') }}
              </a-button>
              <a-button v-if="card.key === 'app'" class="channel-action" size="small" disabled>{{ t('remoteChannels.appDeveloping') }}</a-button>
            </div>
            <!-- 卡片介绍：CLI 兜底配置区统一排在介绍下方 -->
            <p class="channel-description" :title="card.description">{{ card.description }}</p>
            <!-- IN-006, IN-007: 微信 CLI 配置区。整块可点击打开弹窗；左侧设置图标、右侧启用开关独立交互。 -->
            <div
              v-if="card.key === 'wechat'"
              class="wechat-cli-panel"
              role="button"
              tabindex="0"
              :aria-label="t('remoteChannels.configureCLI')"
              @click="openCLIconfig"
              @keydown.enter.prevent="openCLIconfig"
              @keydown.space.prevent="openCLIconfig"
            >
              <span class="cli-settings-icon" aria-hidden="true">
                <SettingOutlined />
              </span>
              <span class="wechat-cli-status">
                <span class="cli-label">{{ t('remoteChannels.cliStatusLabel') }}</span>
                <template v-if="cliConfigured">
                  <span class="cli-name">{{ currentCLIName }}</span>
                  <span class="cli-sep" aria-hidden="true">·</span>
                  <span class="cli-model">{{ selectedCLImodel }}</span>
                </template>
                <span v-else class="cli-unset">{{ t('remoteChannels.cliStatusUnset') }}</span>
              </span>
              <a-switch
                class="cli-enabled-switch"
                :checked="cliConfigEnabled"
                :loading="cliConfigToggling"
                :disabled="!cliConfigured"
                :aria-label="t('remoteChannels.enableCLIConfig')"
                @click.stop
                @keydown.stop
                @change="(v: boolean) => toggleCLIEnabled(v)"
              />
            </div>
            <!-- 企业微信 CLI 兜底配置区，与微信卡片同款：整块可点击打开弹窗，启用开关独立交互 -->
            <div
              v-if="card.key === 'wecom'"
              class="wechat-cli-panel"
              role="button"
              tabindex="0"
              :aria-label="t('remoteChannels.configureCLI')"
              @click="openWecomCLIconfig"
              @keydown.enter.prevent="openWecomCLIconfig"
              @keydown.space.prevent="openWecomCLIconfig"
            >
              <span class="cli-settings-icon" aria-hidden="true">
                <SettingOutlined />
              </span>
              <span class="wechat-cli-status">
                <span class="cli-label">{{ t('remoteChannels.cliStatusLabel') }}</span>
                <template v-if="wecomCLIConfigured">
                  <span class="cli-name">{{ wecomCLIName }}</span>
                  <span class="cli-sep" aria-hidden="true">·</span>
                  <span class="cli-model">{{ wecomSelectedCLImodel }}</span>
                </template>
                <span v-else class="cli-unset">{{ t('remoteChannels.cliStatusUnset') }}</span>
              </span>
              <a-switch
                class="cli-enabled-switch"
                :checked="wecomCLIEnabled && wecomCLIConfigured"
                :disabled="!wecomCLIConfigured"
                :loading="wecomCLIToggling"
                :aria-label="t('remoteChannels.enableCLIConfig')"
                @click.stop
                @keydown.stop
                @change="(v: boolean) => toggleWecomCLIEnabled(v)"
              />
            </div>
            <!-- 飞书 CLI 兜底配置区 -->
            <div
              v-if="card.key === 'feishu'"
              class="wechat-cli-panel"
              role="button"
              tabindex="0"
              :aria-label="t('remoteChannels.configureCLI')"
              @click="openFeishuCLIconfig"
              @keydown.enter.prevent="openFeishuCLIconfig"
              @keydown.space.prevent="openFeishuCLIconfig"
            >
              <span class="cli-settings-icon" aria-hidden="true">
                <SettingOutlined />
              </span>
              <span class="wechat-cli-status">
                <span class="cli-label">{{ t('remoteChannels.cliStatusLabel') }}</span>
                <template v-if="feishuCLIConfigured">
                  <span class="cli-name">{{ feishuCLIName }}</span>
                  <span class="cli-sep" aria-hidden="true">·</span>
                  <span class="cli-model">{{ feishuSelectedCLImodel }}</span>
                </template>
                <span v-else class="cli-unset">{{ t('remoteChannels.cliStatusUnset') }}</span>
              </span>
              <a-switch
                class="cli-enabled-switch"
                :checked="feishuCLIEnabled && feishuCLIConfigured"
                :disabled="!feishuCLIConfigured"
                :loading="feishuCLIToggling"
                :aria-label="t('remoteChannels.enableCLIConfig')"
                @click.stop
                @keydown.stop
                @change="(v: boolean) => toggleFeishuCLIEnabled(v)"
              />
            </div>
            <!-- 钉钉 CLI 兜底配置区 -->
            <div
              v-if="card.key === 'dingtalk'"
              class="wechat-cli-panel"
              role="button"
              tabindex="0"
              :aria-label="t('remoteChannels.configureCLI')"
              @click="openDingtalkCLIconfig"
              @keydown.enter.prevent="openDingtalkCLIconfig"
              @keydown.space.prevent="openDingtalkCLIconfig"
            >
              <span class="cli-settings-icon" aria-hidden="true">
                <SettingOutlined />
              </span>
              <span class="wechat-cli-status">
                <span class="cli-label">{{ t('remoteChannels.cliStatusLabel') }}</span>
                <template v-if="dingtalkCLIConfigured">
                  <span class="cli-name">{{ dingtalkCLIName }}</span>
                  <span class="cli-sep" aria-hidden="true">·</span>
                  <span class="cli-model">{{ dingtalkSelectedCLImodel }}</span>
                </template>
                <span v-else class="cli-unset">{{ t('remoteChannels.cliStatusUnset') }}</span>
              </span>
              <a-switch
                class="cli-enabled-switch"
                :checked="dingtalkCLIEnabled && dingtalkCLIConfigured"
                :disabled="!dingtalkCLIConfigured"
                :loading="dingtalkCLIToggling"
                :aria-label="t('remoteChannels.enableCLIConfig')"
                @click.stop
                @keydown.stop
                @change="(v: boolean) => toggleDingtalkCLIEnabled(v)"
              />
            </div>
          </article>
      </div>
      <!-- 待核对的远程指令：页面底部的固定区块，与微信连接状态解耦。
           此前用 status === 'connected' 作为显示条件，微信断开时整块消失（挂载瞬间还会因 loading 一闪），
           待核对记录本身用于解除任务操作保护，必须在未连接时也能看到并核对。 -->
      <section class="uncertain-panel" aria-labelledby="uncertain-title">
        <div class="uncertain-heading">
          <h2 id="uncertain-title">{{ t('remoteChannels.uncertainTitle') }}</h2>
          <a-button size="small" :loading="uncertainLoading" @click="loadUncertain">{{ t('remoteChannels.retry') }}</a-button>
        </div>
        <a-alert v-if="uncertainRuns.length" type="warning" show-icon :message="t('remoteChannels.uncertainDescription')" />
        <p v-if="uncertainError" class="uncertain-error">{{ t('remoteChannels.uncertainLoadFailed') }}</p>
        <p v-if="uncertainLoading && !uncertainRuns.length" class="uncertain-empty">{{ t('remoteChannels.uncertainLoading') }}</p>
        <p v-else-if="!uncertainRuns.length && !uncertainError" class="uncertain-empty">{{ t('remoteChannels.uncertainEmpty') }}</p>
        <div v-for="run in uncertainRuns" :key="run.message_id" class="uncertain-item">
          <div class="uncertain-task-line">
            <span class="uncertain-hash" aria-hidden="true">#</span>
            <div class="uncertain-details">
              <strong>{{ run.task_title || t('remoteChannels.uncertainTaskMissing') }}</strong>
              <span>{{ t('remoteChannels.uncertainMeta', { time: d(new Date(run.received_at), 'long'), id: run.task_uuid || t('remoteChannels.uncertainTaskUnknown') }) }}</span>
            </div>
          </div>
          <div class="uncertain-actions">
            <router-link v-if="run.task_title" :to="{ name: 'workflows-task-detail', params: { taskUuid: run.task_uuid } }">
              {{ t('remoteChannels.openTask') }}
            </router-link>
            <a-button danger size="small" :loading="acknowledgeBusy === run.message_id" :disabled="!!acknowledgeBusy" @click="acknowledgeUncertain(run)">
              {{ t('remoteChannels.uncertainAcknowledge') }}
            </a-button>
          </div>
        </div>
      </section>
    </div>

    <a-modal v-model:open="loginOpen" :title="t('remoteChannels.loginTitle')" :footer="null" :mask-closable="false" width="400px" @cancel="closeLogin">
      <div class="login-body">
        <p class="scan-prompt">{{ t('remoteChannels.scanPrompt') }}</p>
        <div class="qr-frame">
          <img v-if="login?.qr_image" :src="login.qr_image" :alt="t('remoteChannels.scanPrompt')">
          <a-spin v-else :spinning="loginBusy" />
        </div>
        <p class="login-state" :class="{ error: login?.status === 'error' || login?.status === 'expired' }" role="status">{{ loginStatusText }}</p>
        <div v-if="login?.status === 'need_verifycode'" class="verify-row">
          <a-input v-model:value="verifyCode" :placeholder="t('remoteChannels.verifyPlaceholder')" :aria-label="t('remoteChannels.verifyPlaceholder')" @press-enter="submitVerification" />
          <a-button type="primary" :loading="verifyBusy" @click="submitVerification">{{ t('remoteChannels.verify') }}</a-button>
        </div>
        <a-button v-if="login?.status === 'expired' || login?.status === 'error'" @click="startLogin">{{ t('remoteChannels.refreshQR') }}</a-button>
        <a-button class="close-button" @click="closeLogin">{{ t('remoteChannels.close') }}</a-button>
      </div>
    </a-modal>

    <!-- S-UI001, IN-001, IN-002, IN-003, IN-004, IN-005, IN-006, UI-002, UI-004, UI-005, UI-006, UI-007: CLI 配置弹窗 -->
    <a-modal
      v-model:open="cliConfigOpen"
      :title="t('remoteChannels.cliConfigTitle')"
      :footer="null"
      :mask-closable="false"
      width="500px"
      @cancel="closeCLIconfig"
    >
      <div class="cli-config-body">
        <p class="cli-config-hint">{{ t('remoteChannels.cliConfigHint') }}</p>

        <!-- IN-006: 启用此配置的总开关，关闭后 dispatch 不再走 CLI 兜底 -->
        <div class="form-item cli-enabled-row">
          <span class="form-label cli-enabled-label">{{ t('remoteChannels.enableCLIConfig') }}</span>
          <a-switch
            :checked="cliConfigEnabled"
            :aria-label="t('remoteChannels.enableCLIConfig')"
            @change="(v: boolean) => (cliConfigEnabled = v)"
          />
        </div>

        <!-- CLI 类型选择 -->
        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.cliType') }}</label>
          <a-select
            v-model:value="selectedCLItype"
            :placeholder="t('remoteChannels.selectCliType')"
            :loading="cliDiscoveryLoading"
            :options="cliSelectOptions"
            style="width: 100%"
            @change="onCLItypeChange"
          >
            <template #option="{ label, value, installed, modelCount }">
              <span class="cli-select-option">
                <span class="cli-select-option__name">{{ label ?? value }}</span>
                <span class="cli-select-option__meta">{{ installed ? (modelCount ?? '…') : t('remoteChannels.cliNotInstalledTag') }}</span>
              </span>
            </template>
          </a-select>
        </div>

        <!-- 模型选择 -->
        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.cliModel') }}</label>
          <a-select
            v-model:value="selectedCLImodel"
            :placeholder="t('remoteChannels.selectCliModel')"
            :disabled="!selectedCLItype"
            style="width: 100%"
          >
            <a-select-option v-for="model in cliModels" :key="model" :value="model">
              {{ model }}
            </a-select-option>
          </a-select>
        </div>

        <!-- 工作区目录：兜底任务自动创建时使用的工作目录；留空使用默认 ~/goteams/wechat_workspace -->
        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.workspaceDir') }}</label>
          <DirectoryPicker
            v-model:value="selectedWorkspace"
            :candidates="workspaceCandidates"
            :placeholder="t('remoteChannels.workspaceDirPlaceholder')"
            :empty-text="t('remoteChannels.workspaceEmpty')"
            :aria-label="t('remoteChannels.workspaceDir')"
          />
        </div>

        <!-- IN-007: 两个按钮，左侧稍后配置（取消），右侧确认配置（保存），不再有第三个取消按钮 -->
        <div class="cli-config-actions">
          <a-button class="cli-skip-button" @click="skipCLIconfig">{{ t('remoteChannels.skipCLIConfig') }}</a-button>
          <a-button type="primary" :loading="cliConfigSaving" :disabled="!selectedCLItype || !selectedCLImodel" @click="saveCLIconfig">
            {{ t('remoteChannels.confirmCLIConfig') }}
          </a-button>
        </div>
      </div>
    </a-modal>

    <!-- 企业微信通道配置弹窗 -->
    <a-modal
      v-model:open="wecomConfigOpen"
      :title="t('remoteChannels.wecomConfigTitle')"
      :footer="null"
      :mask-closable="false"
      width="500px"
      @cancel="closeWeComConfig"
    >
      <div class="cli-config-body">
        <p class="cli-config-hint channel-guide-hint">
          <i18n-t keypath="remoteChannels.wecomConfigGuide" tag="span">
            <template #platform><a
              class="channel-guide-link"
              role="link"
              tabindex="0"
              :aria-label="t('remoteChannels.wecomPlatformLinkText')"
              @click.prevent="openWecomPlatform"
              @keydown.enter.prevent="openWecomPlatform"
            >{{ t('remoteChannels.wecomPlatformLinkText') }}</a></template>
            <template #guide><a
              class="channel-guide-link"
              role="link"
              tabindex="0"
              :aria-label="t('remoteChannels.wecomGuideLinkText')"
              @click.prevent="openWecomGuide"
              @keydown.enter.prevent="openWecomGuide"
            >{{ t('remoteChannels.wecomGuideLinkText') }}</a></template>
          </i18n-t>
        </p>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.wecomAppId') }}</label>
          <a-input v-model:value="wecomAppID" :placeholder="t('remoteChannels.wecomAppIdPlaceholder')" />
        </div>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.wecomAppSecret') }}</label>
          <a-input-password v-model:value="wecomAppSecret" :placeholder="t('remoteChannels.wecomAppSecretPlaceholder')" />
        </div>

        <div class="wecom-config-actions">
          <a-button @click="closeWeComConfig">{{ t('remoteChannels.cancel') }}</a-button>
          <a-button :loading="wecomVerifyBusy" :disabled="!wecomAppID.trim() || !wecomAppSecret" @click="verifyWeComConfig">{{ t('remoteChannels.wecomVerify') }}</a-button>
          <a-button type="primary" :loading="wecomConfigBusy" :disabled="!wecomAppID.trim()" @click="connectWeCom">{{ t('remoteChannels.confirm') }}</a-button>
        </div>
      </div>
    </a-modal>

    <!-- 飞书通道配置弹窗 -->
    <a-modal
      v-model:open="feishuConfigOpen"
      :title="t('remoteChannels.feishuConfigTitle')"
      :footer="null"
      :mask-closable="false"
      width="500px"
    >
      <div class="cli-config-body">
        <p class="cli-config-hint channel-guide-hint">
          <i18n-t keypath="remoteChannels.feishuConfigGuide" tag="span">
            <template #platform><a
              class="channel-guide-link"
              role="link"
              tabindex="0"
              :aria-label="t('remoteChannels.feishuPlatformLinkText')"
              @click.prevent="openFeishuPlatform"
              @keydown.enter.prevent="openFeishuPlatform"
            >{{ t('remoteChannels.feishuPlatformLinkText') }}</a></template>
            <template #guide><a
              class="channel-guide-link"
              role="link"
              tabindex="0"
              :aria-label="t('remoteChannels.feishuGuideLinkText')"
              @click.prevent="openFeishuGuide"
              @keydown.enter.prevent="openFeishuGuide"
            >{{ t('remoteChannels.feishuGuideLinkText') }}</a></template>
          </i18n-t>
        </p>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.feishuAppId') }}</label>
          <a-input v-model:value="feishuAppID" :placeholder="t('remoteChannels.feishuAppIdPlaceholder')" />
        </div>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.feishuAppSecret') }}</label>
          <a-input-password v-model:value="feishuAppSecret" :placeholder="t('remoteChannels.feishuAppSecretPlaceholder')" />
        </div>

        <div class="wecom-config-actions">
          <a-button @click="closeFeishuConfig">{{ t('remoteChannels.cancel') }}</a-button>
          <a-button :loading="feishuVerifyBusy" :disabled="!feishuAppID.trim() || !feishuAppSecret" @click="verifyFeishuConfig">{{ t('remoteChannels.wecomVerify') }}</a-button>
          <a-button type="primary" :loading="feishuConfigBusy" :disabled="!feishuAppID.trim()" @click="connectFeishu">{{ t('remoteChannels.confirm') }}</a-button>
        </div>
      </div>
    </a-modal>

    <!-- 钉钉通道配置弹窗 -->
    <a-modal
      v-model:open="dingtalkConfigOpen"
      :title="t('remoteChannels.dingtalkConfigTitle')"
      :footer="null"
      :mask-closable="false"
      width="500px"
    >
      <div class="cli-config-body">
        <p class="cli-config-hint channel-guide-hint">
          <i18n-t keypath="remoteChannels.dingtalkConfigGuide" tag="span">
            <template #platform><a
              class="channel-guide-link"
              role="link"
              tabindex="0"
              :aria-label="t('remoteChannels.dingtalkPlatformLinkText')"
              @click.prevent="openDingtalkPlatform"
              @keydown.enter.prevent="openDingtalkPlatform"
            >{{ t('remoteChannels.dingtalkPlatformLinkText') }}</a></template>
            <template #guide><a
              class="channel-guide-link"
              role="link"
              tabindex="0"
              :aria-label="t('remoteChannels.dingtalkGuideLinkText')"
              @click.prevent="openDingtalkGuide"
              @keydown.enter.prevent="openDingtalkGuide"
            >{{ t('remoteChannels.dingtalkGuideLinkText') }}</a></template>
          </i18n-t>
        </p>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.dingtalkClientId') }}</label>
          <a-input v-model:value="dingtalkClientID" :placeholder="t('remoteChannels.dingtalkClientIdPlaceholder')" />
        </div>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.dingtalkClientSecret') }}</label>
          <a-input-password v-model:value="dingtalkClientSecret" :placeholder="t('remoteChannels.dingtalkClientSecretPlaceholder')" />
        </div>

        <div class="form-item">
          <label class="form-label">
            {{ t('remoteChannels.dingtalkRobotCode') }}
            <a-tooltip :title="t('remoteChannels.dingtalkRobotCodeTooltip')">
              <span class="form-label-tip" role="img" :aria-label="t('remoteChannels.dingtalkRobotCodeTooltip')">?</span>
            </a-tooltip>
          </label>
          <a-input v-model:value="dingtalkRobotCode" :placeholder="t('remoteChannels.dingtalkRobotCodePlaceholder')" />
        </div>

        <div class="wecom-config-actions">
          <a-button @click="closeDingtalkConfig">{{ t('remoteChannels.cancel') }}</a-button>
          <a-button :loading="dingtalkVerifyBusy" :disabled="!dingtalkClientID.trim() || !dingtalkClientSecret" @click="verifyDingtalkConfig">{{ t('remoteChannels.wecomVerify') }}</a-button>
          <a-button type="primary" :loading="dingtalkConfigBusy" :disabled="!dingtalkClientID.trim()" @click="connectDingtalk">{{ t('remoteChannels.confirm') }}</a-button>
        </div>
      </div>
    </a-modal>

    <!-- 企业微信通道 CLI 兜底配置弹窗：与微信同款，独立的 CLI 配置入口 -->
    <a-modal
      v-model:open="wecomCLIConfigOpen"
      :title="t('remoteChannels.wecomCLIConfigTitle')"
      :footer="null"
      :mask-closable="false"
      width="500px"
      @cancel="closeWecomCLIconfig"
    >
      <div class="cli-config-body">
        <p class="cli-config-hint">{{ t('remoteChannels.wecomCLIConfigHint') }}</p>

        <div class="form-item cli-enabled-row">
          <span class="form-label cli-enabled-label">{{ t('remoteChannels.enableCLIConfig') }}</span>
          <a-switch
            :checked="wecomCLIEnabled"
            :aria-label="t('remoteChannels.enableCLIConfig')"
            @change="(v: boolean) => (wecomCLIEnabled = v)"
          />
        </div>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.cliType') }}</label>
          <a-select
            v-model:value="wecomSelectedCLItype"
            :placeholder="t('remoteChannels.selectCliType')"
            :loading="cliDiscoveryLoading"
            :options="cliSelectOptions"
            style="width: 100%"
            @change="onWecomCLItypeChange"
          >
            <template #option="{ label, value, installed, modelCount }">
              <span class="cli-select-option">
                <span class="cli-select-option__name">{{ label ?? value }}</span>
                <span class="cli-select-option__meta">{{ installed ? (modelCount ?? '…') : t('remoteChannels.cliNotInstalledTag') }}</span>
              </span>
            </template>
          </a-select>
        </div>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.cliModel') }}</label>
          <a-select
            v-model:value="wecomSelectedCLImodel"
            :placeholder="t('remoteChannels.selectCliModel')"
            :disabled="!wecomSelectedCLItype"
            :loading="cliDiscoveryLoading"
            style="width: 100%"
          >
            <a-select-option v-for="model in cliModels" :key="model" :value="model">
              {{ model }}
            </a-select-option>
          </a-select>
        </div>

        <!-- 工作区目录：兜底任务自动创建时使用的工作目录；留空使用默认 ~/goteams/wecom_workspace -->
        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.workspaceDir') }}</label>
          <DirectoryPicker
            v-model:value="wecomSelectedWorkspace"
            :candidates="workspaceCandidates"
            :placeholder="t('remoteChannels.workspaceDirPlaceholderWecom')"
            :empty-text="t('remoteChannels.workspaceEmpty')"
            :aria-label="t('remoteChannels.workspaceDir')"
          />
        </div>

        <div class="cli-config-actions">
          <a-button class="cli-skip-button" @click="closeWecomCLIconfig">{{ t('remoteChannels.skipCLIConfig') }}</a-button>
          <a-button type="primary" :loading="wecomCLISaving" :disabled="!wecomSelectedCLItype || !wecomSelectedCLImodel" @click="saveWecomCLIconfig">
            {{ t('remoteChannels.confirmCLIConfig') }}
          </a-button>
        </div>
      </div>
    </a-modal>

    <!-- 飞书通道 CLI 兜底配置弹窗 -->
    <a-modal
      v-model:open="feishuCLIConfigOpen"
      :title="t('remoteChannels.feishuCLIConfigTitle')"
      :footer="null"
      :mask-closable="false"
      width="500px"
      @cancel="closeFeishuCLIconfig"
    >
      <div class="cli-config-body">
        <p class="cli-config-hint">{{ t('remoteChannels.feishuCLIConfigHint') }}</p>

        <div class="form-item cli-enabled-row">
          <span class="form-label cli-enabled-label">{{ t('remoteChannels.enableCLIConfig') }}</span>
          <a-switch
            :checked="feishuCLIEnabled"
            :aria-label="t('remoteChannels.enableCLIConfig')"
            @change="(v: boolean) => (feishuCLIEnabled = v)"
          />
        </div>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.cliType') }}</label>
          <a-select
            v-model:value="feishuSelectedCLItype"
            :placeholder="t('remoteChannels.selectCliType')"
            :loading="cliDiscoveryLoading"
            :options="cliSelectOptions"
            style="width: 100%"
            @change="onFeishuCLItypeChange"
          >
            <template #option="{ label, value, installed, modelCount }">
              <span class="cli-select-option">
                <span class="cli-select-option__name">{{ label ?? value }}</span>
                <span class="cli-select-option__meta">{{ installed ? (modelCount ?? '…') : t('remoteChannels.cliNotInstalledTag') }}</span>
              </span>
            </template>
          </a-select>
        </div>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.cliModel') }}</label>
          <a-select
            v-model:value="feishuSelectedCLImodel"
            :placeholder="t('remoteChannels.selectCliModel')"
            :disabled="!feishuSelectedCLItype"
            :loading="cliDiscoveryLoading"
            style="width: 100%"
          >
            <a-select-option v-for="model in cliModels" :key="model" :value="model">
              {{ model }}
            </a-select-option>
          </a-select>
        </div>

        <!-- 工作区目录：兜底任务自动创建时使用的工作目录；留空使用默认 ~/goteams/feishu_workspace -->
        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.workspaceDir') }}</label>
          <DirectoryPicker
            v-model:value="feishuSelectedWorkspace"
            :candidates="workspaceCandidates"
            :placeholder="t('remoteChannels.workspaceDirPlaceholderFeishu')"
            :empty-text="t('remoteChannels.workspaceEmpty')"
            :aria-label="t('remoteChannels.workspaceDir')"
          />
        </div>

        <div class="cli-config-actions">
          <a-button class="cli-skip-button" @click="closeFeishuCLIconfig">{{ t('remoteChannels.skipCLIConfig') }}</a-button>
          <a-button type="primary" :loading="feishuCLISaving" :disabled="!feishuSelectedCLItype || !feishuSelectedCLImodel" @click="saveFeishuCLIconfig">
            {{ t('remoteChannels.confirmCLIConfig') }}
          </a-button>
        </div>
      </div>
    </a-modal>

    <!-- 钉钉通道 CLI 兜底配置弹窗 -->
    <a-modal
      v-model:open="dingtalkCLIConfigOpen"
      :title="t('remoteChannels.dingtalkCLIConfigTitle')"
      :footer="null"
      :mask-closable="false"
      width="500px"
      @cancel="closeDingtalkCLIconfig"
    >
      <div class="cli-config-body">
        <p class="cli-config-hint">{{ t('remoteChannels.dingtalkCLIConfigHint') }}</p>

        <div class="form-item cli-enabled-row">
          <span class="form-label cli-enabled-label">{{ t('remoteChannels.enableCLIConfig') }}</span>
          <a-switch
            :checked="dingtalkCLIEnabled"
            :aria-label="t('remoteChannels.enableCLIConfig')"
            @change="(v: boolean) => (dingtalkCLIEnabled = v)"
          />
        </div>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.cliType') }}</label>
          <a-select
            v-model:value="dingtalkSelectedCLItype"
            :placeholder="t('remoteChannels.selectCliType')"
            :loading="cliDiscoveryLoading"
            :options="cliSelectOptions"
            style="width: 100%"
            @change="onDingtalkCLItypeChange"
          >
            <template #option="{ label, value, installed, modelCount }">
              <span class="cli-select-option">
                <span class="cli-select-option__name">{{ label ?? value }}</span>
                <span class="cli-select-option__meta">{{ installed ? (modelCount ?? '…') : t('remoteChannels.cliNotInstalledTag') }}</span>
              </span>
            </template>
          </a-select>
        </div>

        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.cliModel') }}</label>
          <a-select
            v-model:value="dingtalkSelectedCLImodel"
            :placeholder="t('remoteChannels.selectCliModel')"
            :disabled="!dingtalkSelectedCLItype"
            :loading="cliDiscoveryLoading"
            style="width: 100%"
          >
            <a-select-option v-for="model in cliModels" :key="model" :value="model">
              {{ model }}
            </a-select-option>
          </a-select>
        </div>

        <!-- 工作区目录：兜底任务自动创建时使用的工作目录；留空使用默认 ~/goteams/dingtalk_workspace -->
        <div class="form-item">
          <label class="form-label">{{ t('remoteChannels.workspaceDir') }}</label>
          <DirectoryPicker
            v-model:value="dingtalkSelectedWorkspace"
            :candidates="workspaceCandidates"
            :placeholder="t('remoteChannels.workspaceDirPlaceholderDingtalk')"
            :empty-text="t('remoteChannels.workspaceEmpty')"
            :aria-label="t('remoteChannels.workspaceDir')"
          />
        </div>

        <div class="cli-config-actions">
          <a-button class="cli-skip-button" @click="closeDingtalkCLIconfig">{{ t('remoteChannels.skipCLIConfig') }}</a-button>
          <a-button type="primary" :loading="dingtalkCLISaving" :disabled="!dingtalkSelectedCLItype || !dingtalkSelectedCLImodel" @click="saveDingtalkCLIconfig">
            {{ t('remoteChannels.confirmCLIConfig') }}
          </a-button>
        </div>
      </div>
    </a-modal>
  </section>
</template>

<style scoped>
.remote-page { height: 100%; min-height: 0; overflow: auto; background: #fff; color: #262626; }
.page-header { height: 64px; display: flex; align-items: center; padding: 0 24px; border-bottom: 1px solid #f0f0f0; }
.page-header h1 { margin: 0; font-size: 20px; line-height: 28px; font-weight: 600; }
.remote-content { padding: 24px; }
.intro-banner { display: flex; gap: 12px; align-items: flex-start; padding: 16px; background: #eaf0ff; border-radius: 12px; font-size: 14px; line-height: 22px; }
.intro-banner strong { font-weight: 600; }
.intro-banner p { margin: 4px 0 0; color: #595959; }
.info-symbol { display: inline-grid; place-items: center; width: 18px; height: 18px; flex: 0 0 18px; margin-top: 2px; border-radius: 50%; background: #3157e2; color: #fff; font-size: 12px; font-weight: 700; }
.state-message { padding: 24px 0; display: flex; align-items: center; gap: 12px; color: #595959; }
.status-alert { margin-top: 16px; }
.uncertain-panel { margin-top: 24px; padding: 20px; border: 1px solid #d9d9d9; border-radius: 12px; background: #fff; }
.uncertain-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 12px; }
.uncertain-heading h2 { margin: 0; font-size: 16px; line-height: 24px; font-weight: 600; }
.uncertain-error { margin: 12px 0 0; color: #fb363f; font-size: 14px; line-height: 22px; }
.uncertain-empty { margin: 12px 0 0; color: #8c8c8c; font-size: 14px; line-height: 22px; }
.uncertain-item { display: flex; flex-direction: column; gap: 8px; padding: 12px 0; border-bottom: 1px solid #f0f0f0; }
.uncertain-item:last-child { border-bottom: 0; padding-bottom: 0; }
.uncertain-task-line { display: flex; align-items: flex-start; gap: 8px; }
.uncertain-hash { flex: 0 0 auto; color: #262626; font-size: 14px; font-weight: 600; line-height: 22px; user-select: none; }
.uncertain-details { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.uncertain-details strong { font-size: 14px; line-height: 22px; color: #262626; }
.uncertain-details span { color: #8c8c8c; font-size: 12px; line-height: 20px; overflow-wrap: anywhere; }
.uncertain-actions { display: flex; align-items: center; gap: 12px; padding-left: 24px; }
.channel-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; margin-top: 16px; }
.channel-card { min-width: 0; padding: 20px; border: 1px solid #e5e7eb; border-radius: 14px; background: #fff; }
.card-heading { display: flex; align-items: center; gap: 12px; }
.card-heading h2 { min-width: 0; margin: 0; flex: 1; font-size: 16px; font-weight: 600; line-height: 24px; }
.channel-description { display: -webkit-box; height: 66px; margin: 16px 0 0; overflow: hidden; color: #595959; font-size: 14px; line-height: 22px; overflow-wrap: anywhere; -webkit-box-orient: vertical; -webkit-line-clamp: 3; }
.channel-action { flex: 0 0 auto; }
.wechat-action-wrap { position: relative; display: inline-flex; flex: 0 0 auto; }
.wechat-action-with-warning { padding-right: 28px; }
.wechat-reply-warning { position: absolute; top: 50%; right: 1px; display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; padding: 0; border: 0; background: transparent; cursor: pointer; transform: translateY(-50%); }
.wechat-reply-warning::before { display: inline-flex; align-items: center; justify-content: center; width: 15px; height: 15px; border-radius: 50%; background: #fb363f; color: #fff; content: '!'; font-size: 11px; font-weight: 700; }
.wechat-reply-warning:focus-visible { outline: 2px solid #3157e2; outline-offset: 2px; }
.wechat-reply-warning-text { display: block; max-width: 280px; }
.wechat-action:not(:disabled):not(.wechat-action-stop) { color: #3157e2; border-color: #3157e2; }
.channel-icon { display: inline-grid; place-items: center; width: 40px; height: 40px; flex: 0 0 40px; border-radius: 12px; color: white; font-size: 18px; font-weight: 700; }
.channel-icon svg { width: 28px; height: 28px; }
.wechat { background: #16a36a; }.wecom { background: #35a8ed; }.feishu { background: #4b79f5; }.dingtalk { background: #2783ee; }.app { background: #5b7ce7; }
.login-body { display: flex; align-items: center; flex-direction: column; padding: 12px 0 0; }
.scan-prompt { margin: 8px 0 16px; font-size: 14px; }
.qr-frame { display: grid; place-items: center; width: 240px; height: 240px; border: 1px solid #e5e7eb; border-radius: 12px; }
.qr-frame img { width: 224px; height: 224px; }
.login-state { min-height: 22px; margin: 16px 0; color: #595959; text-align: center; }
.login-state.error { color: #c63d43; }
.verify-row { display: flex; width: 100%; gap: 8px; margin-bottom: 12px; }
.close-button { align-self: flex-end; margin-top: 12px; }

/* S-UI001, IN-001: 旧的 CLI 配置按钮已被整块可点击的 .wechat-cli-panel 取代，保留占位以避免误删 */
.cli-config-button { display: none; }

/* IN-006, IN-007: 微信卡片 CLI 整块渲染区，作为打开配置弹窗的入口 */
.wechat-cli-panel { display: flex; align-items: center; gap: 10px; margin-top: 16px; padding: 10px 12px; border: 1px solid #e5e7eb; border-radius: 10px; background: #fafafa; cursor: pointer; transition: border-color 0.15s ease, background 0.15s ease; }
.wechat-cli-panel:hover { border-color: #722ed1; background: #f6f0ff; }
.wechat-cli-panel:focus-visible { outline: 2px solid #722ed1; outline-offset: 2px; }
.wechat-cli-panel .cli-settings-icon { display: inline-grid; place-items: center; width: 24px; height: 24px; flex: 0 0 24px; border-radius: 6px; background: #fff; color: #722ed1; border: 1px solid #e5e7eb; font-size: 14px; }
.wechat-cli-panel .cli-enabled-switch { flex: 0 0 auto; margin-left: auto; pointer-events: auto; }

/* S-UI001: 微信卡片 CLI 状态展示行（被包裹在 .wechat-cli-panel 内） */
.wechat-cli-status { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; min-width: 0; flex: 1; }
.wechat-cli-status .cli-label { color: #8c8c8c; }
.wechat-cli-status .cli-name { color: #262626; font-weight: 500; }
.wechat-cli-status .cli-model { color: #262626; }
.wechat-cli-status .cli-sep { color: #bfbfbf; }
.wechat-cli-status .cli-unset { color: #bfbfbf; }

/* S-UI001: CLI 配置弹窗样式 */
.cli-config-body { padding: 8px 0; }
.cli-config-hint { margin: 0 0 16px; color: #595959; font-size: 14px; line-height: 22px; }
/* 企微/飞书/钉钉 配置弹窗：开放平台与配置步骤链接样式，与 chatclaw 通道提醒保持一致的视觉 */
.channel-guide-hint { color: #262626; }
.channel-guide-link { color: #1677ff; text-decoration: none; cursor: pointer; }
.channel-guide-link:hover { text-decoration: underline; }
.channel-guide-link:focus-visible { outline: 2px solid #1677ff; outline-offset: 2px; border-radius: 2px; }
.form-item { margin-bottom: 16px; }
.form-label { display: block; margin-bottom: 8px; font-size: 14px; font-weight: 500; color: #262626; }
/* S-UI001: CLI 类型下拉选项：左侧名称、右侧模型个数，与创建任务的 CLI 下拉保持一致 */
.cli-select-option { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-width: 0; }
.cli-select-option__name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cli-select-option__meta { flex: 0 0 auto; color: #7a8699; font-size: 12px; line-height: 20px; font-variant-numeric: tabular-nums; }
.cli-enabled-row { display: flex; align-items: center; justify-content: space-between; padding: 10px 12px; border: 1px solid #e5e7eb; border-radius: 8px; background: #fafafa; }
.cli-enabled-row .form-label { margin: 0; }
.cli-enabled-label { cursor: default; }
/* IN-007: 弹窗按钮区：稍后配置与确认配置并排显示在右侧，组成一组按钮 */
.cli-config-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 24px; }

/* 企业微信 / 飞书 / 钉钉 凭据弹窗底部操作行 */
.wecom-config-actions { display: flex; gap: 8px; justify-content: flex-end; margin-top: 16px; }

@media (max-width: 820px) { .channel-grid { grid-template-columns: 1fr; } .remote-content { padding: 16px; } .uncertain-actions { padding-left: 24px; } }
</style>
