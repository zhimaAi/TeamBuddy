<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import MarkdownIt from 'markdown-it'
import apiClient from '@/api/client'
import { getApiToken } from '@/api/token'
import CliSelectModal from '@/components/CliSelectModal.vue'
import {
  formatPastedImageContent,
  useTaskImageAttachments,
} from '@/composables/useTaskImageAttachments'
import { cliDisplayName, sanitizeUserMessageText } from '@/components/task-progress/utils'
import { revealTaskOutputFile } from '@/composables/useDesktop'
import { t } from '@/i18n'
import { allowLocalFileLinks, LOCAL_FILE_LINK_ATTRIBUTE, LOCAL_FILE_SESSION_ATTRIBUTE, localFileTarget, renderInlineFileCode } from '@/utils/localFileLink'
import StopExecutionConfirmModal from '@/components/task-progress/StopExecutionConfirmModal.vue'
import { isStopConfirmSuppressed, suppressStopConfirm } from '@/utils/stopConfirm'

interface Step {
  uuid: string
  step_key: string
  name: string
  sort_order: number
  cli_type: string
  status: string
  execution_status: string
  prompt_snapshot: string
}

interface Session {
  uuid: string
  task_uuid: string
  step_uuid: string
  step_key: string
  step_name: string
  conversation_uuid: string
  parent_session_uuid: string
  run_no: number
  status: string
  cli_type: string
  external_session_id: string
  work_dir: string
  prompt_snapshot: string
  display_prompt: string
  input_tokens: number
  output_tokens: number
  total_tokens: number
  started_at: number
  finished_at: number
  duration_ms: number
  created_at: number
  updated_at: number
  error_message?: string
}

interface ConversationGroup {
  uuid: string
  sessions: Session[]
  firstSession: Session
  latestSession: Session
}

interface PermissionRequest {
  tool_name: string
  tool_use_id: string
  tool_input?: string
}

interface ExecutorEvent {
  type: string
  content?: string
  error?: string
  permission_requests?: PermissionRequest[]
  timestamp?: number
  input_tokens?: number
  output_tokens?: number
}

interface TimelineEvent extends ExecutorEvent {
  sequence: number
}

const props = defineProps<{
  open: boolean
  taskUuid: string
  steps: Step[]
  focusStepKey?: string
  focusSessionUuid?: string
}>()

const emit = defineEmits<{
  'update:open': [value: boolean]
  refresh: []
}>()

const markdown = new MarkdownIt({ breaks: true, linkify: true })
allowLocalFileLinks(markdown)
const originalLinkOpenRenderer = markdown.renderer.rules.link_open
markdown.renderer.rules.link_open = (tokens, index, options, env, self) => {
  const token = tokens[index]
  const href = token.attrGet('href') || ''
  if (env.taskUuid && localFileTarget(href, env.fileRoots || [])) {
    token.attrSet('href', '#')
    token.attrSet(LOCAL_FILE_LINK_ATTRIBUTE, href)
    if (env.sessionUuid) token.attrSet(LOCAL_FILE_SESSION_ATTRIBUTE, env.sessionUuid)
    token.attrSet('title', t('components.markdown.revealInFolder'))
  }
  return originalLinkOpenRenderer
    ? originalLinkOpenRenderer(tokens, index, options, env, self)
    : self.renderToken(tokens, index, options)
}
const sessions = ref<Session[]>([])
const sessionsLoading = ref(false)
const selectedStepKey = ref('')
const selectedSessionUuid = ref('')
const eventsBySession = ref<Record<string, TimelineEvent[]>>({})
const question = ref('')
const sending = ref(false)
const {
  clear: clearImageAttachments,
  isSaving: isSavingPastedImages,
  addImages,
  removeAttachmentMarkers,
  resolveAttachmentMarkdown,
  uploadAttachmentsToTask,
} = useTaskImageAttachments()
const permissionActionSequence = ref<number>()
const dismissedPermissionEvents = ref<Record<string, boolean>>({})
const stopping = ref(false)
const stopConfirmOpen = ref(false)
const stopConfirmSessionUuid = ref('')
const startingStep = ref('')
const cliModalOpen = ref(false)
const socketState = ref<'connecting' | 'connected' | 'disconnected'>('disconnected')
const messagePanel = ref<HTMLElement>()
const autoScroll = ref(true)
let socket: WebSocket | undefined
let reconnectTimer: ReturnType<typeof setTimeout> | undefined
let closingSocket = false

const activeStatuses = ['created', 'running', 'waiting_input', 'stop_requested']
const terminalStatuses = ['success', 'failed', 'stopped', 'interrupted']
const sessionsByStep = computed(() => {
  const grouped: Record<string, Session[]> = {}
  for (const step of props.steps) grouped[step.step_key] = []
  for (const session of sessions.value) {
    if (!grouped[session.step_key]) grouped[session.step_key] = []
    grouped[session.step_key].push(session)
  }
  return grouped
})

const conversationsByStep = computed(() => {
  const grouped: Record<string, ConversationGroup[]> = {}
  for (const step of props.steps) grouped[step.step_key] = []

  for (const [stepKey, stepSessions] of Object.entries(sessionsByStep.value)) {
    const conversations = new Map<string, Session[]>()
    for (const session of stepSessions) {
      const conversationUuid = session.conversation_uuid || session.uuid
      const items = conversations.get(conversationUuid) || []
      items.push(session)
      conversations.set(conversationUuid, items)
    }

    grouped[stepKey] = [...conversations.entries()]
      .map(([uuid, items]) => {
        const ordered = [...items].sort(
          (a, b) => a.run_no - b.run_no || a.created_at - b.created_at,
        )
        return {
          uuid,
          sessions: ordered,
          firstSession: ordered[0],
          latestSession: ordered[ordered.length - 1],
        }
      })
      .sort(
        (a, b) =>
          a.firstSession.created_at - b.firstSession.created_at ||
          a.firstSession.run_no - b.firstSession.run_no,
      )
  }
  return grouped
})

const selectedStep = computed(() =>
  props.steps.find((step) => step.step_key === selectedStepKey.value),
)

const selectedSession = computed(() =>
  sessions.value.find((session) => session.uuid === selectedSessionUuid.value),
)

const selectedConversationUuid = computed(() => {
  const session = selectedSession.value
  return session ? session.conversation_uuid || session.uuid : ''
})

const selectedConversationSessions = computed(() => {
  const session = selectedSession.value
  if (!session) return []
  const conversationUuid = session.conversation_uuid || session.uuid
  return sessions.value
    .filter((item) => (item.conversation_uuid || item.uuid) === conversationUuid)
    .sort((a, b) => a.run_no - b.run_no || a.created_at - b.created_at)
})

const activeSession = computed(() =>
  sessions.value.find((session) => activeStatuses.includes(session.status)),
)

const canContinue = computed(() => {
  const session = selectedSession.value
  return Boolean(session && terminalStatuses.includes(session.status) && !activeSession.value)
})

const continueHint = computed(() => {
  if (!selectedSession.value) return '请选择一轮历史对话'
  if (activeSession.value) return '当前执行结束或终止后，可以继续发送问题'
  return '输入问题，继续当前 CLI 对话'
})

function statusText(status: string) {
  return (
    {
      idle: '未执行',
      created: '准备中',
      running: '执行中',
      waiting_input: '等待输入',
      stop_requested: '正在终止',
      success: '已完成',
      failed: '失败',
      stopped: '已终止',
      interrupted: '已中断',
    }[status] || status
  )
}

function statusColor(status: string) {
  return (
    {
      idle: 'default',
      created: 'processing',
      running: 'processing',
      waiting_input: 'warning',
      stop_requested: 'warning',
      success: 'success',
      failed: 'error',
      stopped: 'warning',
      interrupted: 'default',
    }[status] || 'default'
  )
}

function displaySessionPrompt(session: Session) {
  return sanitizeUserMessageText(
    session.display_prompt ||
      session.prompt_snapshot ||
      selectedStep.value?.prompt_snapshot ||
      '开始执行步骤',
  )
}

function sessionEvents(sessionUuid: string) {
  return eventsBySession.value[sessionUuid] || []
}

function sessionHasPendingPermission(sessionUuid: string) {
  const permissionEvent = (eventsBySession.value[sessionUuid] || []).find(
    (event) => event.type === 'permission_request',
  )
  return Boolean(permissionEvent && !isPermissionEventDismissed(permissionEvent, sessionUuid))
}

function sessionStatusText(session: Session) {
  return sessionHasPendingPermission(session.uuid) ? '待授权' : statusText(session.status)
}

function sessionStatusColor(session: Session) {
  return sessionHasPendingPermission(session.uuid) ? 'warning' : statusColor(session.status)
}

function sessionHasErrorEvent(sessionUuid: string) {
  return (eventsBySession.value[sessionUuid] || []).some((event) => event.type === 'error')
}

function sessionErrorHint(session?: Session) {
  return session?.status === 'failed' ? session.error_message || '' : ''
}

function formatTime(timestamp: number) {
  if (!timestamp) return '--:--:--'
  return new Date(timestamp).toLocaleTimeString('zh-CN', { hour12: false })
}

function formatDuration(duration: number) {
  if (!duration) return ''
  if (duration < 1000) return `${duration}ms`
  if (duration < 60_000) return `${(duration / 1000).toFixed(1)}s`
  return `${Math.floor(duration / 60_000)}m ${Math.round((duration % 60_000) / 1000)}s`
}

const originalCodeRenderer = markdown.renderer.rules.code_inline
markdown.renderer.rules.code_inline = (tokens, index, options, env, self) => {
  const html = renderInlineFileCode(
    markdown,
    tokens[index].content,
    env.fileRoots || [],
    t('components.markdown.revealInFolder'),
    env.sessionUuid || '',
  )
  if (env.taskUuid && html) return html
  return originalCodeRenderer
    ? originalCodeRenderer(tokens, index, options, env, self)
    : self.renderToken(tokens, index, options)
}

function renderMarkdown(content = '', sessionUuid = '') {
  return markdown.render(content, { taskUuid: props.taskUuid, sessionUuid, fileRoots: [] })
}

async function handleFileLinkClick(event: MouseEvent) {
  const target = event.target
  if (!(target instanceof Element)) return
  const anchor = target.closest<HTMLAnchorElement>(`a[${LOCAL_FILE_LINK_ATTRIBUTE}]`)
  const fileRef = anchor?.getAttribute(LOCAL_FILE_LINK_ATTRIBUTE)
  const sessionUuid = anchor?.getAttribute(LOCAL_FILE_SESSION_ATTRIBUTE) || ''
  if (!anchor || !fileRef || !props.taskUuid) return
  event.preventDefault()
  try {
    const result = await revealTaskOutputFile(props.taskUuid, fileRef, sessionUuid)
    if (result === 'copied') message.info(t('components.markdown.revealUseDesktop'))
  } catch (error) {
    message.error(error instanceof Error && error.message ? error.message : t('components.markdown.revealFailed'))
  }
}

function permissionEventKey(event: TimelineEvent, sessionUuid = selectedSessionUuid.value) {
  return `${sessionUuid}:${event.sequence}`
}

function isPermissionEventDismissed(event: TimelineEvent, sessionUuid = selectedSessionUuid.value) {
  return Boolean(dismissedPermissionEvents.value[permissionEventKey(event, sessionUuid)])
}

function dismissPermissionEvent(event: TimelineEvent, sessionUuid = selectedSessionUuid.value) {
  dismissedPermissionEvents.value = {
    ...dismissedPermissionEvents.value,
    [permissionEventKey(event, sessionUuid)]: true,
  }
  message.info('已拒绝授权，本轮不会继续执行该操作')
}

function formatPermissionInput(input = '') {
  if (!input) return '未提供参数'
  try {
    return JSON.stringify(JSON.parse(input), null, 2)
  } catch {
    return input
  }
}

async function loadSessions(preferredSessionUuid?: string) {
  sessionsLoading.value = true
  try {
    const result = await apiClient.get<{ items: Session[] }>(`/tasks/${props.taskUuid}/sessions`)
    sessions.value = result.items || []

    if (preferredSessionUuid && sessions.value.some((item) => item.uuid === preferredSessionUuid)) {
      selectSession(preferredSessionUuid)
      return
    }
    if (
      selectedSessionUuid.value &&
      sessions.value.some((item) => item.uuid === selectedSessionUuid.value)
    ) {
      return
    }

    const stepKey = selectedStepKey.value || props.focusStepKey || props.steps[0]?.step_key || ''
    selectStep(stepKey)
  } catch (error) {
    message.error(error instanceof Error ? error.message : '执行历史加载失败')
  } finally {
    sessionsLoading.value = false
  }
}

function selectStep(stepKey: string) {
  clearPastedImagesFromQuestion()
  selectedStepKey.value = stepKey
  const stepSessions = sessionsByStep.value[stepKey] || []
  const latest = stepSessions[stepSessions.length - 1]
  selectedSessionUuid.value = latest?.uuid || ''
  if (latest) subscribeConversation(latest.uuid)
  scrollToBottom(true)
}

function selectSession(sessionUuid: string) {
  const session = sessions.value.find((item) => item.uuid === sessionUuid)
  if (!session) return
  clearPastedImagesFromQuestion()
  selectedStepKey.value = session.step_key
  selectedSessionUuid.value = sessionUuid
  subscribeConversation(sessionUuid)
  scrollToBottom(true)
}

function subscribeConversation(sessionUuid: string) {
  const session = sessions.value.find((item) => item.uuid === sessionUuid)
  if (!session) return
  const conversationUuid = session.conversation_uuid || session.uuid
  const conversationSessions = sessions.value
    .filter((item) => (item.conversation_uuid || item.uuid) === conversationUuid)
    .sort((a, b) => a.run_no - b.run_no || a.created_at - b.created_at)
  for (const item of conversationSessions) subscribe(item.uuid)
}

// 浏览器 WebSocket 握手无法携带自定义 header，鉴权 token 通过 query 传递
async function webSocketUrl() {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const token = await getApiToken()
  const query = token ? `?token=${encodeURIComponent(token)}` : ''
  return `${protocol}//${window.location.host}/api/local/ws${query}`
}

async function connectSocket() {
  if (
    !props.open ||
    socket?.readyState === WebSocket.OPEN ||
    socket?.readyState === WebSocket.CONNECTING
  ) {
    return
  }
  closingSocket = false
  socketState.value = 'connecting'
  socket = new WebSocket(await webSocketUrl())
  socket.onopen = () => {
    socketState.value = 'connected'
    if (selectedSessionUuid.value) subscribeConversation(selectedSessionUuid.value)
  }
  socket.onmessage = handleSocketMessage
  socket.onerror = () => {
    socketState.value = 'disconnected'
  }
  socket.onclose = () => {
    socket = undefined
    socketState.value = 'disconnected'
    if (!closingSocket && props.open) {
      reconnectTimer = setTimeout(connectSocket, 1500)
    }
  }
}

function closeSocket() {
  closingSocket = true
  if (reconnectTimer) clearTimeout(reconnectTimer)
  reconnectTimer = undefined
  socket?.close()
  socket = undefined
  socketState.value = 'disconnected'
}

function subscribe(sessionUuid: string) {
  if (!sessionUuid) return
  if (!socket || socket.readyState !== WebSocket.OPEN) {
    connectSocket()
    return
  }
  const existing = eventsBySession.value[sessionUuid] || []
  const afterSequence = existing.reduce((max, event) => Math.max(max, event.sequence), 0)
  socket.send(
    JSON.stringify({
      type: 'executor.subscribe',
      task_uuid: props.taskUuid,
      session_uuid: sessionUuid,
      after_sequence: afterSequence,
    }),
  )
}

function handleSocketMessage(raw: MessageEvent<string>) {
  let payload: {
    type?: string
    task_uuid?: string
    session_uuid?: string
    status?: string
    sequence?: number
    event?: string | ExecutorEvent
  }
  try {
    payload = JSON.parse(raw.data)
  } catch {
    return
  }

  if (payload.type === 'executor.event') {
    const sessionUuid = String(payload.session_uuid || '')
    const event = typeof payload.event === 'string' ? JSON.parse(payload.event) : payload.event
    const sequence = Number(payload.sequence || 0)
    if (!sessionUuid || !event || !sequence) return

    const current = eventsBySession.value[sessionUuid] || []
    if (!current.some((item) => item.sequence === sequence)) {
      eventsBySession.value = {
        ...eventsBySession.value,
        [sessionUuid]: [...current, { ...event, sequence }].sort((a, b) => a.sequence - b.sequence),
      }
      if (sessionUuid === selectedSessionUuid.value) scrollToBottom()
      if (event.type === 'complete' || event.type === 'error') {
        setTimeout(() => {
          void loadSessions()
          emit('refresh')
        }, 300)
      }
    }
    return
  }

  if (payload.type === 'executor.state_changed' && payload.task_uuid === props.taskUuid) {
    const target = sessions.value.find((item) => item.uuid === payload.session_uuid)
    if (target) target.status = String(payload.status)
    if (payload.status !== 'stop_requested') {
      void loadSessions()
    }
    emit('refresh')
  }
}

async function startSelectedStep() {
  if (!selectedStep.value || activeSession.value) return
  cliModalOpen.value = true
}

async function onCliConfirm(payload: { cli_type: string; model: string }) {
  if (!selectedStep.value || activeSession.value) return
  startingStep.value = selectedStep.value.step_key
  try {
    const result = await apiClient.post<{ session_uuid: string }>(
      `/tasks/${props.taskUuid}/steps/${encodeURIComponent(selectedStep.value.step_key)}/runs`,
      { request_id: crypto.randomUUID(), cli_type: payload.cli_type, model: payload.model },
    )
    await loadSessions(result.session_uuid)
    emit('refresh')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '步骤启动失败')
  } finally {
    startingStep.value = ''
  }
}

async function sendQuestion() {
  const session = selectedSession.value
  const taskUuid = props.taskUuid
  if (!question.value.trim() || !session || !canContinue.value || isSavingPastedImages.value) return
  sending.value = true
  try {
    if (taskUuid !== props.taskUuid) return
    const content = question.value.trim()
    if (!content) return
    const result = await continueSession(session.uuid, content, content)
    question.value = ''
    clearImageAttachments()
    await loadSessions(result.session_uuid)
    emit('refresh')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '问题发送失败')
  } finally {
    sending.value = false
  }
}

async function handlePaste(event: ClipboardEvent) {
  if (!event.clipboardData) return
  const files = [...event.clipboardData.items]
    .filter((item) => item.type.startsWith('image/'))
    .map((item) => item.getAsFile())
    .filter((file): file is File => Boolean(file))
  if (!files.length) return
  if (isSavingPastedImages.value) {
    event.preventDefault()
    message.warning('上一批图片正在保存，请稍后再粘贴')
    return
  }

  const textarea = event.target as HTMLTextAreaElement
  const start = textarea.selectionStart ?? question.value.length
  const end = textarea.selectionEnd ?? start
  const pastedText = event.clipboardData.getData('text/plain')
  const pastedHtml = event.clipboardData.getData('text/html')
  event.preventDefault()
  let insertedImageMarkers = false
  try {
    const attachments = addImages(files)
    insertPastedQuestion(
      pastedText,
      pastedHtml,
      attachments.map((attachment) => attachment.marker),
      textarea,
      start,
      end,
    )
    insertedImageMarkers = true
    await uploadAttachmentsToTask(props.taskUuid)
    const resolved = resolveAttachmentMarkdown(question.value, textarea.selectionStart)
    question.value = resolved.content
    clearImageAttachments()
    await nextTick()
    textarea.focus()
    textarea.setSelectionRange(resolved.caret, resolved.caret)
  } catch (error) {
    if (insertedImageMarkers) question.value = removeAttachmentMarkers(question.value)
    clearImageAttachments()
    message.error(error instanceof Error ? error.message : '图片粘贴失败')
  }
}

function insertPastedQuestion(
  text: string,
  html: string,
  imageMarkers: string[],
  textarea: HTMLTextAreaElement,
  start: number,
  end: number,
) {
  const insertStart = Math.min(start, question.value.length)
  const insertEnd = Math.min(Math.max(end, insertStart), question.value.length)
  const before = question.value.slice(0, insertStart)
  const after = question.value.slice(insertEnd)
  const inserted = formatPastedImageContent(text, html, imageMarkers, before, after)
  question.value = `${before}${inserted}${after}`
  const caret = insertStart + inserted.length
  nextTick(() => {
    textarea.focus()
    textarea.setSelectionRange(caret, caret)
  })
}

function clearPastedImagesFromQuestion() {
  question.value = removeAttachmentMarkers(question.value)
  clearImageAttachments()
}

async function approvePermission(event: TimelineEvent, sourceSessionUuid: string) {
  const session = selectedSession.value
  if (!session || !canContinue.value) {
    message.warning('请等待本轮 CLI 结束并取得可继续的会话 ID')
    return
  }

  const toolNames = [
    ...new Set((event.permission_requests || []).map((item) => item.tool_name).filter(Boolean)),
  ]
  const toolDescription = toolNames.length ? toolNames.join('、') : '上述工具'
  const content = `用户已明确批准使用 ${toolDescription}。请继续执行刚才因权限不足而未完成的操作，不要再次等待授权。`

  permissionActionSequence.value = event.sequence
  try {
    const result = await continueSession(session.uuid, content)
    dismissedPermissionEvents.value = {
      ...dismissedPermissionEvents.value,
      [permissionEventKey(event, sourceSessionUuid)]: true,
    }
    await loadSessions(result.session_uuid)
    emit('refresh')
    message.success('已授权，CLI 将继续执行')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '权限授权失败')
  } finally {
    permissionActionSequence.value = undefined
  }
}

function continueSession(sessionUuid: string, content: string, displayContent = content) {
  return apiClient.post<{ session_uuid: string }>(`/tasks/sessions/${sessionUuid}/messages`, {
    content,
    display_content: displayContent,
    request_id: crypto.randomUUID(),
  })
}

function stopActiveSession() {
  const session = activeSession.value
  if (!session) return
  if (isStopConfirmSuppressed()) {
    void executeStop(session.uuid)
    return
  }
  stopConfirmSessionUuid.value = session.uuid
  stopConfirmOpen.value = true
}

async function executeStop(sessionUuid: string) {
  stopping.value = true
  try {
    await apiClient.post(`/tasks/sessions/${sessionUuid}/stop`)
    await loadSessions(sessionUuid)
    emit('refresh')
    message.success('执行已终止')
  } catch (error) {
    message.error(error instanceof Error ? error.message : '终止失败')
  } finally {
    stopping.value = false
    closeStopConfirm()
  }
}

function closeStopConfirm() {
  stopConfirmOpen.value = false
  stopConfirmSessionUuid.value = ''
}

function confirmStopActiveSession(suppressFutureConfirm: boolean) {
  const sessionUuid = stopConfirmSessionUuid.value
  if (suppressFutureConfirm) suppressStopConfirm()
  if (sessionUuid) void executeStop(sessionUuid)
}

function scrollToBottom(force = false) {
  void nextTick(() => {
    const el = messagePanel.value
    if (!el) return
    if (force) autoScroll.value = true
    if (autoScroll.value) el.scrollTop = el.scrollHeight
  })
}

function onMessageScroll() {
  const el = messagePanel.value
  if (!el) return
  const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
  autoScroll.value = distanceFromBottom < 40
}

watch(
  () => props.open,
  (open) => {
    if (open) {
      selectedStepKey.value =
        props.focusStepKey || selectedStepKey.value || props.steps[0]?.step_key || ''
      selectedSessionUuid.value = props.focusSessionUuid || ''
      void loadSessions(props.focusSessionUuid)
      connectSocket()
    } else {
      closeSocket()
    }
  },
  { immediate: true },
)

watch(
  () => props.focusSessionUuid,
  (sessionUuid) => {
    if (props.open && sessionUuid) void loadSessions(sessionUuid)
  },
)

watch(
  () => props.taskUuid,
  () => {
    question.value = ''
    clearImageAttachments()
  },
)

onBeforeUnmount(closeSocket)
</script>

<template>
  <a-modal
    :open="open"
    width="min(1380px, calc(100vw - 48px))"
    :footer="null"
    :body-style="{ padding: 0 }"
    wrap-class-name="task-execution-modal"
    @cancel="emit('update:open', false)"
  >
    <StopExecutionConfirmModal
      :open="stopConfirmOpen"
      :loading="stopping"
      title="终止当前执行？"
      description="CLI 进程及其子进程将被终止，已经产生的对话和日志会保留。"
      confirm-text="终止执行"
      @close="closeStopConfirm"
      @confirm="confirmStopActiveSession"
    />

    <template #title>
      <div class="modal-title">
        <span>任务执行窗口</span>
        <a-tag
          :color="
            socketState === 'connected'
              ? 'success'
              : socketState === 'connecting'
                ? 'processing'
                : 'default'
          "
        >
          {{
            socketState === 'connected'
              ? '实时连接'
              : socketState === 'connecting'
                ? '正在连接'
                : '连接已断开'
          }}
        </a-tag>
        <span class="local-note">AI 对话会同步云端，工具明细仅保存在本机</span>
      </div>
    </template>

    <div class="console-shell">
      <aside class="step-sidebar">
        <div class="sidebar-heading">工作流步骤</div>
        <a-spin :spinning="sessionsLoading">
          <div
            v-for="step in steps"
            :key="step.step_key"
            class="step-group"
            :class="{ selected: selectedStepKey === step.step_key }"
          >
            <button
              class="step-button"
              type="button"
              @click="selectStep(step.step_key)"
            >
              <span class="step-number">{{ step.sort_order }}</span>
              <span class="step-title">{{ step.name }}</span>
              <a-badge :status="statusColor(step.execution_status) as any" />
            </button>

            <div
              v-if="selectedStepKey === step.step_key"
              class="session-list"
            >
              <button
                v-for="(conversation, conversationIndex) in conversationsByStep[step.step_key]"
                :key="conversation.uuid"
                type="button"
                class="session-button"
                :class="{ active: selectedConversationUuid === conversation.uuid }"
                :title="sessionErrorHint(conversation.latestSession) || undefined"
                @click="selectSession(conversation.latestSession.uuid)"
              >
                <span class="session-button__main">
                  <span class="session-button__title">
                    对话 {{ conversationIndex + 1 }}
                    <small>
                      {{ conversation.sessions.length }} 轮 ·
                      {{
                        formatTime(
                          conversation.firstSession.started_at ||
                            conversation.firstSession.created_at,
                        )
                      }}
                    </small>
                  </span>
                  <small
                    v-if="
                      conversation.latestSession.status === 'failed' &&
                      conversation.latestSession.error_message
                    "
                    class="session-error-text"
                  >
                    {{ conversation.latestSession.error_message }}
                  </small>
                </span>
                <a-tag
                  :color="sessionStatusColor(conversation.latestSession)"
                  class="session-status"
                >
                  {{ sessionStatusText(conversation.latestSession) }}
                </a-tag>
              </button>
              <div
                v-if="!conversationsByStep[step.step_key]?.length"
                class="no-session"
              >
                暂无执行记录
              </div>
            </div>
          </div>
        </a-spin>
      </aside>

      <section class="conversation-pane">
        <header class="console-header">
          <div>
            <div class="eyebrow">当前步骤</div>
            <h3>
              {{ selectedStep ? `${selectedStep.sort_order}. ${selectedStep.name}` : '请选择步骤' }}
            </h3>
            <div
              v-if="selectedSession"
              class="session-meta"
            >
              {{ selectedSession.cli_type.toUpperCase() }} · 当前对话共
              {{ selectedConversationSessions.length }} 轮
              <template v-if="selectedSession.duration_ms">
                · {{ formatDuration(selectedSession.duration_ms) }}</template
              >
              <template v-if="selectedSession.total_tokens">
                · {{ selectedSession.total_tokens }} Tokens</template
              >
            </div>
          </div>
          <a-space>
            <a-button
              v-if="activeSession"
              danger
              :loading="stopping"
              @click="stopActiveSession"
            >
              终止当前执行
            </a-button>
            <a-button
              type="primary"
              :loading="startingStep === selectedStepKey"
              :disabled="!selectedStep || Boolean(activeSession)"
              @click="startSelectedStep"
            >
              {{ sessionsByStep[selectedStepKey]?.length ? '新建对话' : '执行步骤' }}
            </a-button>
          </a-space>
        </header>

        <div
          ref="messagePanel"
          class="message-panel"
          @click="handleFileLinkClick"
          @scroll="onMessageScroll"
        >
          <a-empty
            v-if="!selectedSession"
            :description="selectedStep ? '这个步骤还没有执行记录' : '请从左侧选择一个步骤'"
          />
          <template v-else>
            <template
              v-for="conversationSession in selectedConversationSessions"
              :key="conversationSession.uuid"
            >
              <div
                v-if="selectedConversationSessions.length > 1"
                class="round-divider"
              >
                <span>第 {{ conversationSession.run_no }} 轮</span>
                <em v-if="conversationSession.parent_session_uuid">续聊</em>
              </div>

              <div class="message-row user-message">
                <div class="avatar user-avatar">你</div>
                <div class="message-content">
                  <div class="message-label">
                    发送给 CLI
                    <span>{{
                      formatTime(conversationSession.started_at || conversationSession.created_at)
                    }}</span>
                  </div>
                  <div class="bubble user-bubble">
                    {{ displaySessionPrompt(conversationSession) }}
                  </div>
                </div>
              </div>

              <template
                v-for="event in sessionEvents(conversationSession.uuid)"
                :key="event.sequence"
              >
                <div
                  v-if="event.type === 'message'"
                  class="message-row assistant-message"
                >
                  <div class="avatar cli-avatar">CLI</div>
                  <div class="message-content">
                    <div class="message-label">
                      {{ cliDisplayName(conversationSession.cli_type) }}
                      <span>{{ formatTime(event.timestamp || 0) }}</span>
                    </div>
                    <div
                      class="bubble assistant-bubble markdown-body"
                      v-html="renderMarkdown(event.content, conversationSession.uuid)"
                    />
                  </div>
                </div>

                <details
                  v-else-if="event.type === 'tool_call' || event.type === 'tool_result'"
                  class="tool-event"
                >
                  <summary>
                    <span class="tool-kind">{{
                      event.type === 'tool_call' ? '调用工具' : '工具返回'
                    }}</span>
                    <span class="tool-preview">{{ event.content || '无输出' }}</span>
                    <time>{{ formatTime(event.timestamp || 0) }}</time>
                  </summary>
                  <pre>{{ event.content || '无输出' }}</pre>
                </details>

                <div
                  v-else-if="
                    event.type === 'permission_request' &&
                    !isPermissionEventDismissed(event, conversationSession.uuid)
                  "
                  class="permission-event"
                >
                  <div class="permission-title">CLI 权限申请</div>
                  <p>{{ event.content || 'CLI 请求执行需要授权的操作' }}</p>
                  <details
                    v-for="request in event.permission_requests || []"
                    :key="request.tool_use_id || request.tool_name"
                    class="permission-detail"
                  >
                    <summary>{{ request.tool_name || '未知工具' }}</summary>
                    <pre>{{ formatPermissionInput(request.tool_input) }}</pre>
                  </details>
                  <div class="permission-actions">
                    <a-button
                      size="small"
                      @click="dismissPermissionEvent(event, conversationSession.uuid)"
                    >
                      拒绝
                    </a-button>
                    <a-button
                      type="primary"
                      size="small"
                      :loading="permissionActionSequence === event.sequence"
                      :disabled="!canContinue"
                      @click="approvePermission(event, conversationSession.uuid)"
                    >
                      允许并继续
                    </a-button>
                  </div>
                  <small v-if="!canContinue">本轮结束并取得 CLI 会话 ID 后即可授权继续</small>
                </div>

                <a-alert
                  v-else-if="event.type === 'error'"
                  type="error"
                  show-icon
                  :message="event.error || event.content || 'CLI 执行失败'"
                  class="system-event"
                />

                <div
                  v-else-if="event.type === 'start'"
                  class="system-line"
                >
                  <span>CLI 已启动</span>
                </div>
                <div
                  v-else-if="event.type === 'complete'"
                  class="system-line"
                >
                  <span>本轮执行结束</span>
                </div>
              </template>

              <a-alert
                v-if="
                  conversationSession.error_message &&
                  !sessionHasErrorEvent(conversationSession.uuid)
                "
                type="error"
                show-icon
                :message="conversationSession.error_message"
                class="system-event"
              />

              <div
                v-if="activeStatuses.includes(conversationSession.status)"
                class="typing-row"
              >
                <span /><span /><span />
                <em>CLI 正在执行，消息会实时显示在这里</em>
              </div>
            </template>
          </template>
        </div>

        <footer class="composer">
          <a-textarea
            v-model:value="question"
            :disabled="!canContinue"
            :placeholder="continueHint"
            :auto-size="{ minRows: 2, maxRows: 5 }"
            @paste="handlePaste"
            @keydown.ctrl.enter.prevent="sendQuestion"
            @keydown.meta.enter.prevent="sendQuestion"
          />
          <div class="composer-actions">
            <span>Ctrl / ⌘ + Enter 发送</span>
            <a-button
              type="primary"
              :loading="sending"
              :disabled="!canContinue || isSavingPastedImages || !question.trim()"
              @click="sendQuestion"
            >
              发送问题
            </a-button>
          </div>
        </footer>
      </section>
    </div>
  </a-modal>

  <CliSelectModal
    v-model:open="cliModalOpen"
    @confirm="onCliConfirm"
  />
</template>

<style scoped>
.modal-title {
  display: flex;
  gap: 10px;
  align-items: center;
}

.local-note {
  color: #8c8c8c;
  font-size: 12px;
  font-weight: 400;
}

.console-shell {
  display: grid;
  grid-template-columns: 310px minmax(0, 1fr);
  height: min(760px, calc(100vh - 150px));
  min-height: 560px;
  overflow: hidden;
  border-top: 1px solid #eef0f4;
}

.step-sidebar {
  overflow-y: auto;
  border-right: 1px solid #e8ebf0;
  background: #f7f8fb;
}

.sidebar-heading {
  position: sticky;
  top: 0;
  z-index: 2;
  padding: 17px 18px 11px;
  color: #596273;
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0.08em;
  background: #f7f8fb;
}

.step-group {
  margin: 4px 10px;
  border-radius: 10px;
}

.step-group.selected {
  padding-bottom: 6px;
  background: #fff;
  box-shadow: 0 2px 12px rgb(30 42 66 / 7%);
}

.step-button,
.session-button {
  width: 100%;
  border: 0;
  text-align: left;
  cursor: pointer;
}

.step-button {
  display: grid;
  grid-template-columns: 28px 1fr 18px;
  gap: 8px;
  align-items: center;
  padding: 11px 12px;
  color: #222b3c;
  background: transparent;
  border-radius: 10px;
}

.step-number {
  display: grid;
  width: 25px;
  height: 25px;
  color: #55708f;
  font-size: 12px;
  place-items: center;
  border: 1px solid #d9e0e9;
  border-radius: 50%;
}

.step-title {
  overflow: hidden;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-list {
  padding: 0 7px 3px 44px;
}

.session-button {
  display: flex;
  gap: 6px;
  align-items: center;
  justify-content: space-between;
  padding: 7px 8px;
  color: #566176;
  font-size: 12px;
  background: transparent;
  border-radius: 7px;
}

.session-button__main {
  display: flex;
  flex: 1;
  min-width: 0;
  flex-direction: column;
  gap: 2px;
}

.session-button__title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-error-text {
  overflow: hidden;
  color: #cf1322;
  font-size: 11px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.session-button:hover,
.session-button.active {
  color: #3157d5;
  background: #edf2ff;
}

.session-button small {
  margin-left: 5px;
  color: #9aa2b1;
}

.session-status {
  margin: 0;
  font-size: 11px;
  line-height: 19px;
}

.no-session {
  padding: 8px;
  color: #a1a8b5;
  font-size: 12px;
}

.conversation-pane {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  background: #fff;
}

.console-header {
  display: flex;
  flex-shrink: 0;
  gap: 16px;
  align-items: center;
  justify-content: space-between;
  padding: 16px 22px;
  border-bottom: 1px solid #edf0f4;
}

.eyebrow {
  margin-bottom: 2px;
  color: #8791a3;
  font-size: 11px;
  letter-spacing: 0.08em;
}

.console-header h3 {
  margin: 0;
  color: #1f2633;
  font-size: 17px;
}

.session-meta {
  margin-top: 4px;
  color: #8791a3;
  font-size: 12px;
}

.message-panel {
  flex: 1 1 auto;
  min-height: 0;
  overflow-y: auto;
  overscroll-behavior: contain;
  padding: 24px clamp(20px, 4vw, 58px);
  background: linear-gradient(180deg, #fbfcfe 0, #fff 130px);
}

.round-divider {
  display: flex;
  gap: 8px;
  align-items: center;
  margin: 8px 0 20px;
  color: #8b94a4;
  font-size: 12px;
}

.round-divider::before,
.round-divider::after {
  height: 1px;
  content: '';
  background: #e7eaf0;
}

.round-divider::before {
  width: 28px;
}

.round-divider::after {
  flex: 1;
}

.round-divider em {
  padding: 1px 7px;
  color: #3157d5;
  font-style: normal;
  background: #edf2ff;
  border-radius: 10px;
}

.message-row {
  display: flex;
  gap: 12px;
  margin-bottom: 24px;
}

.user-message {
  flex-direction: row-reverse;
}

.avatar {
  display: grid;
  flex: 0 0 auto;
  width: 34px;
  height: 34px;
  color: #fff;
  font-size: 11px;
  font-weight: 700;
  place-items: center;
  border-radius: 10px;
}

.user-avatar {
  background: #4568dc;
}

.cli-avatar {
  background: #171b24;
}

.message-content {
  max-width: min(780px, calc(100% - 50px));
}

.user-message .message-content {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
}

.message-label {
  display: flex;
  gap: 9px;
  margin: 0 3px 6px;
  color: #4e586c;
  font-size: 12px;
  font-weight: 600;
}

.message-label span {
  color: #a0a7b4;
  font-weight: 400;
}

.bubble {
  padding: 12px 15px;
  line-height: 1.65;
  white-space: pre-wrap;
  border-radius: 5px 14px 14px;
}

.user-bubble {
  color: #fff;
  background: #4568dc;
  border-radius: 14px 5px 14px 14px;
}

.assistant-bubble {
  color: #273044;
  background: #f1f3f7;
}

.markdown-body :deep(a) {
  color: #3a7a3a;
  cursor: pointer;
  text-decoration: none;
}

.markdown-body :deep(a:hover) {
  text-decoration: underline;
}

.markdown-body :deep(p:first-child) {
  margin-top: 0;
}

.markdown-body :deep(p:last-child) {
  margin-bottom: 0;
}

.markdown-body :deep(pre) {
  overflow-x: auto;
  padding: 10px;
  background: #171b24;
  border-radius: 7px;
}

.markdown-body :deep(code) {
  font-family: Consolas, Monaco, monospace;
}

.tool-event {
  margin: 0 0 14px 46px;
  overflow: hidden;
  border: 1px solid #e3e7ed;
  border-radius: 9px;
  background: #fafbfc;
}

.tool-event summary {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  gap: 10px;
  align-items: center;
  padding: 9px 12px;
  color: #5d687c;
  font-size: 12px;
  cursor: pointer;
}

.tool-kind {
  color: #7857b3;
  font-weight: 600;
}

.tool-preview {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tool-event time {
  color: #a0a7b4;
}

.tool-event pre {
  max-height: 260px;
  margin: 0;
  overflow: auto;
  padding: 12px;
  white-space: pre-wrap;
  border-top: 1px solid #e3e7ed;
  background: #fff;
}

.system-event {
  margin: 0 0 16px 46px;
}

.permission-event {
  margin: 0 0 18px 46px;
  padding: 14px 16px;
  color: #5f4300;
  border: 1px solid #ffd666;
  border-radius: 10px;
  background: #fffbe6;
}

.permission-title {
  margin-bottom: 6px;
  color: #ad6800;
  font-weight: 700;
}

.permission-event p {
  margin: 0 0 10px;
}

.permission-detail {
  margin-top: 8px;
  overflow: hidden;
  border: 1px solid #ffe58f;
  border-radius: 7px;
  background: rgb(255 255 255 / 70%);
}

.permission-detail summary {
  padding: 7px 10px;
  font-size: 12px;
  font-weight: 600;
  cursor: pointer;
}

.permission-detail pre {
  max-height: 200px;
  margin: 0;
  overflow: auto;
  padding: 10px;
  white-space: pre-wrap;
  border-top: 1px solid #ffe58f;
}

.permission-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
  margin-top: 12px;
}

.permission-event small {
  display: block;
  margin-top: 8px;
  color: #8c6d1f;
  text-align: right;
}

.system-line {
  display: flex;
  align-items: center;
  margin: 14px 0 14px 46px;
  color: #9aa2b1;
  font-size: 12px;
}

.system-line::before,
.system-line::after {
  height: 1px;
  content: '';
  background: #eceff3;
}

.system-line::before {
  width: 24px;
  margin-right: 9px;
}

.system-line::after {
  flex: 1;
  margin-left: 9px;
}

.typing-row {
  display: flex;
  gap: 4px;
  align-items: center;
  margin: 12px 0 0 46px;
  color: #8b94a4;
  font-size: 12px;
}

.typing-row span {
  width: 6px;
  height: 6px;
  background: #667085;
  border-radius: 50%;
  animation: pulse 1.2s infinite ease-in-out;
}

.typing-row span:nth-child(2) {
  animation-delay: 0.15s;
}

.typing-row span:nth-child(3) {
  animation-delay: 0.3s;
}

.typing-row em {
  margin-left: 5px;
  font-style: normal;
}

.composer {
  flex-shrink: 0;
  padding: 14px 20px 15px;
  border-top: 1px solid #e8ebf0;
  background: #fff;
}

.composer-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 8px;
}

.composer-actions span {
  color: #a0a7b4;
  font-size: 11px;
}

@keyframes pulse {
  0%,
  70%,
  100% {
    opacity: 0.25;
    transform: translateY(0);
  }
  35% {
    opacity: 1;
    transform: translateY(-2px);
  }
}

@media (max-width: 820px) {
  .console-shell {
    grid-template-columns: 220px minmax(0, 1fr);
  }

  .session-list {
    padding-left: 12px;
  }

  .console-header {
    align-items: flex-start;
  }
}
</style>
