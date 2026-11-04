<template>
  <div class="exec-process">
    <button
      type="button"
      class="exec-summary"
      :aria-expanded="expanded"
      @click="toggleExpanded"
    >
      <ThunderboltOutlined class="exec-summary-glyph" />
      <span class="exec-summary-title">{{ t('workflows.task.progress.executionProcess') }}</span>
      <span
        v-if="summaryChip"
        class="exec-summary-chip"
        :title="props.thinkingLevel ? t('components.cliThinking.hint') : undefined"
      >{{ summaryChip }}</span>
      <span class="exec-summary-gap" />
      <span
        class="exec-summary-state"
        :class="stateClass"
      >
        <LoadingOutlined
          v-if="running"
          spin
        />
        <CheckOutlined v-else-if="succeeded" />
        <MinusCircleOutlined v-else-if="props.status === 'interrupted' || props.status === 'stopped'" />
        <CloseOutlined v-else />
        {{ stateText }}
      </span>
      <DownOutlined
        v-if="expanded"
        class="exec-caret"
      />
      <RightOutlined
        v-else
        class="exec-caret"
      />
    </button>

    <div
      v-if="expanded"
      ref="bodyRef"
      class="exec-body"
    >
      <template
        v-for="row in rows"
        :key="row.key"
      >
        <!-- 思考：图标行 + 缩进正文，无底色 -->
        <div
          v-if="row.kind === 'thinking'"
          class="exec-row"
        >
          <button
            type="button"
            class="exec-row-head"
            :aria-expanded="isRowOpen(row)"
            @click="toggleRow(row)"
          >
            <BulbOutlined class="exec-row-icon icon-thinking" />
            <span class="exec-row-label">{{ t('workflows.task.progress.processThinking') }}</span>
            <span class="exec-row-gap" />
            <span
              v-if="row.durationText"
              class="exec-duration is-done"
            ><i class="exec-dot" />{{ row.durationText }}</span>
            <RowCaret :open="isRowOpen(row)" />
          </button>
          <div
            v-if="isRowOpen(row)"
            class="exec-row-text"
          >
            <TypewriterText
              :text="row.content"
              :enabled="isTypingRow(row)"
              @done="markRevealed(row.key)"
            />
          </div>
        </div>

        <!-- 中间输出：工具调用之间的模型发言；该轮最后一条输出属最终结论，不在此重复 -->
        <div
          v-else-if="row.kind === 'output'"
          class="exec-row"
        >
          <div class="exec-row-head is-static">
            <MessageOutlined class="exec-row-icon" />
            <span class="exec-row-label">{{ t('workflows.task.progress.activityOutput') }}</span>
            <span class="exec-row-gap" />
          </div>
          <div class="exec-row-text">
            <TypewriterText
              :text="row.content"
              :enabled="isTypingRow(row)"
              @done="markRevealed(row.key)"
            />
            <button
              v-if="row.truncNote && row.sequence"
              type="button"
              class="exec-full-link"
              @click.stop="loadFull(row)"
            >{{ row.truncNote }}</button>
          </div>
        </div>

        <!-- 权限请求：非交互模式下 CLI 会停下等授权，必须让用户看到卡点在哪 -->
        <div
          v-else-if="row.kind === 'permission'"
          class="exec-row"
        >
          <div class="exec-row-head is-static">
            <StopOutlined class="exec-row-icon icon-permission" />
            <span class="exec-row-label is-permission">{{ t('workflows.task.progress.processPermission') }}</span>
            <span class="exec-row-gap" />
          </div>
          <div class="exec-row-text is-permission-text">
            {{ row.content }}
          </div>
        </div>

        <!-- 工具调用：完成灰勾 / 进行中蓝色旋转，结果缩进在行下 -->
        <div
          v-else
          class="exec-row"
        >
          <button
            type="button"
            class="exec-row-head"
            :aria-expanded="isRowOpen(row)"
            @click="toggleRow(row)"
          >
            <MinusCircleOutlined
              v-if="row.interrupted"
              class="exec-row-icon is-interrupted"
            />
            <LoadingOutlined
              v-else-if="!row.result && running"
              class="exec-row-icon icon-running"
              spin
            />
            <CheckCircleOutlined
              v-else-if="row.result"
              class="exec-row-icon icon-done"
            />
            <MinusCircleOutlined
              v-else
              class="exec-row-icon"
            />
            <span class="exec-row-tool">{{ row.name }}</span>
            <span
              v-if="row.args"
              class="exec-row-args"
              :title="row.args"
            >{{ row.args }}</span>
            <span class="exec-row-gap" />
            <span
              v-if="row.durationText"
              class="exec-duration"
              :class="row.result ? 'is-done' : 'is-running'"
            ><i class="exec-dot" />{{ row.durationText }}</span>
            <span
              v-if="toolStateText(row)"
              class="exec-row-state"
              :class="row.interrupted ? 'is-interrupted' : (row.result ? 'is-done' : 'is-running')"
            >{{ toolStateText(row) }}</span>
            <RowCaret :open="isRowOpen(row)" />
          </button>
          <div
            v-if="isRowOpen(row) && row.result"
            class="exec-tool-result"
          >
            <p class="exec-result-label">{{ t('workflows.task.progress.processCallResult') }}</p>
            <p class="exec-row-text is-plain">
              {{ row.result }}
            </p>
            <p v-if="row.interrupted" class="exec-interrupt-hint">{{ t('workflows.task.progress.toolInterruptedHint') }}</p>
            <button
              v-if="row.truncNote && row.sequence"
              type="button"
              class="exec-full-link"
              @click.stop="loadFull(row)"
            >{{ row.truncNote }}</button>
          </div>
          <div
            v-else-if="isRowOpen(row) && !row.result && running"
            class="exec-tool-result"
          >
            <p class="exec-running-hint">
              <LoadingOutlined spin />
              {{ t('workflows.task.progress.processRunning') }}
            </p>
          </div>
        </div>
      </template>

      <p
        v-if="!rows.length && historyLoaded"
        class="exec-empty"
      >{{ t('workflows.task.progress.processEmpty') }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, h, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  BulbOutlined,
  CheckCircleOutlined,
  CheckOutlined,
  CloseOutlined,
  DownOutlined,
  LoadingOutlined,
  MessageOutlined,
  MinusCircleOutlined,
  MinusOutlined,
  PlusOutlined,
  RightOutlined,
  StopOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons-vue'
import type { SessionEventItem } from '@/types/pipeline'
import { apiClient } from '@/api/client'
import { onMessage, useLocalWSStatus, wsSend } from '@/composables/useLocalWebSocket'
import { useAppI18n } from '@/i18n'
import { cliDisplayName } from './utils'
import { parseTruncMarker } from '@/utils/truncMarker'
import { useCliNames } from '@/composables/useCliNames'
import TypewriterText from './TypewriterText.vue'

const props = withDefaults(
  defineProps<{
    taskUuid: string
    sessionUuid: string
    status?: string
    cliType?: string
    /** 本次会话使用的模型，来自 gt_task_progress.model_name */
    modelName?: string
    thinkingLevel?: string
    thinkingLevelSent?: string
    thinkingResolution?: string
    /** 本次会话的 token 用量；部分 CLI 不上报，此时为 0 */
    inputTokens?: number
    outputTokens?: number
    totalTokens?: number
    readOnly?: boolean
    initialEvents?: SessionEventItem[]
  }>(),
  {
    readOnly: false,
    initialEvents: () => [],
  },
)

const { t } = useAppI18n()

// CLI 版本来自 cli-discovery 探测结果；模块级缓存，多张卡片只会请求一次
const { cliVersion, ensureLoaded: ensureCliInfo } = useCliNames()
onMounted(() => {
  if (!props.readOnly) void ensureCliInfo()
})

// 行展开控件：设计稿用方形 +/- 按钮。独立函数式组件，避免模板里每个行头重复写两个图标分支
const RowCaret = (caretProps: { open: boolean }) =>
  h('span', { class: 'exec-row-caret' }, [
    h(caretProps.open ? MinusOutlined : PlusOutlined),
  ])

// 单步耗时展示上限：超出视为时间戳不连续，不展示以免误导
const MAX_STEP_DURATION_MS = 60 * 60 * 1000

interface ProcessRow {
  key: string
  kind: 'thinking' | 'output' | 'permission' | 'tool'
  content: string
  name: string
  args: string
  result: string
  durationText: string
  /** 该行来自本次执行的实时流（而非历史回看），只有它才走打字机揭示 */
  live: boolean
  interrupted?: boolean
  truncNote?: string
  sequence?: number
}

interface PermissionRequestPayload {
  tool_name?: string
  tool_input?: string
}

const events = ref<SessionEventItem[]>([...props.initialEvents])
const expanded = ref(false)
const historyLoaded = ref(props.readOnly)
// 行展开状态：默认值由 isRowOpen 决定，用户点击后记住覆盖值
const rowOverrides = ref(new Map<string, boolean>())
// 本次执行实时流入的 sequence：只有它们走打字机。
// 后端按终止边界一次性定稿整段文本，前端无法从内容本身区分「正在生成」与「早就有了」，
// 只能按事件是不是刚推送过来判断，否则打开旧会话会满屏同时打字。
const liveSequences = ref(new Set<number>())
// 已经打完行的 key：折叠后再展开不该重播动画
const revealedKeys = ref(new Set<string>())
// 执行过程的内容容器：只用来观察内容增长，自身已不再滚动
const bodyRef = ref<HTMLElement>()

// 真正承接滚动的外层会话区。执行过程取消了固定高度与内部滚动，
// 跟随必须作用在外层容器上，否则内容增高时用户得手动往下拖。
let scrollContainer: HTMLElement | null = null

// 是否跟随最新内容。只在用户仍停留在底部附近时才自动滚动 —— 用户主动上滑翻看
// 前面的步骤时，被新事件反复拽回底部会让人没法阅读。
let followTail = true
// 「贴底」容差：亚像素布局与滚动惯性会让 scrollTop 与底部差几个像素
const TAIL_TOLERANCE = 24

/** 从执行过程往上找第一个自身可滚动的祖先，作为跟随目标 */
function resolveScrollContainer(): HTMLElement | null {
  let node: HTMLElement | null = bodyRef.value?.parentElement ?? null
  while (node) {
    const overflowY = getComputedStyle(node).overflowY
    if (overflowY === 'auto' || overflowY === 'scroll') return node
    node = node.parentElement
  }
  return null
}

function handleContainerScroll() {
  const el = scrollContainer
  if (el) followTail = el.scrollHeight - el.scrollTop - el.clientHeight <= TAIL_TOLERANCE
}

function bindScrollContainer() {
  scrollContainer?.removeEventListener('scroll', handleContainerScroll)
  scrollContainer = resolveScrollContainer()
  scrollContainer?.addEventListener('scroll', handleContainerScroll, { passive: true })
}

async function scrollToLatest() {
  await nextTick()
  const el = scrollContainer
  if (el) el.scrollTop = el.scrollHeight
}

// 打字机逐字增高内容时滚动容器自身尺寸不变，ResizeObserver 观察不到，
// 只能盯内容树的变更；rAF 节流是必须的，否则逐字更新会让滚动任务每帧排队。
let scrollScheduled = false

function scheduleFollowScroll() {
  if (scrollScheduled) return
  scrollScheduled = true
  requestAnimationFrame(() => {
    scrollScheduled = false
    const el = scrollContainer
    if (el && running.value && followTail) el.scrollTop = el.scrollHeight
  })
}

let bodyObserver: MutationObserver | undefined
let collapseTimer: ReturnType<typeof setTimeout> | undefined

watch(bodyRef, (el) => {
  bodyObserver?.disconnect()
  if (!el) return
  bodyObserver ??= new MutationObserver(scheduleFollowScroll)
  bodyObserver.observe(el, { childList: true, subtree: true, characterData: true })
  bindScrollContainer()
})

onBeforeUnmount(() => {
  unsubscribeEvents()
  clearTimeout(collapseTimer)
  bodyObserver?.disconnect()
  scrollContainer?.removeEventListener('scroll', handleContainerScroll)
})

const running = computed(() => props.status === 'created' || props.status === 'running')
const failed = computed(() => props.status === 'failed')
const succeeded = computed(() => props.status === 'success')
const activityHint = ref('')
const fullBySequence = ref(new Map<number, string>())

async function loadFull(row: ProcessRow) {
  if (!row.sequence || !props.sessionUuid) return
  try {
    const text = await apiClient.get<string>(
      `/tasks/sessions/${encodeURIComponent(props.sessionUuid)}/events/${row.sequence}/full`,
    )
    const next = new Map(fullBySequence.value)
    next.set(row.sequence, text)
    fullBySequence.value = next
  } catch {
    const next = new Map(fullBySequence.value)
    next.set(row.sequence, t('workflows.task.progress.artifactMissing'))
    fullBySequence.value = next
  }
}

const activityText = computed(() => {
  const key: Record<string, string> = {
    reconnecting: 'workflows.task.progress.activityReconnecting',
    retrying: 'workflows.task.progress.activityRetrying',
    quiet: 'workflows.task.progress.activityQuiet',
    long_tool: 'workflows.task.progress.activityLongTool',
    compacting: 'workflows.task.progress.activityCompacting',
    suspect: 'workflows.task.progress.activitySuspect',
    slow_launch: 'workflows.task.progress.activitySlowLaunch',
    calling_model: 'workflows.task.progress.activityCallingModel',
    model_lost: 'workflows.task.progress.activityModelLost',
  }
  const id = key[activityHint.value]
  return id ? t(id) : ''
})

const stateText = computed(() => {
  if (running.value && activityText.value) return activityText.value
  if (running.value) return t('workflows.task.progress.processRunning')
  if (failed.value) return t('workflows.task.progress.processFailed')
  if (props.status === 'success') return t('workflows.task.progress.processDone')
  if (props.status === 'stopped') return t('workflows.task.execution.stopped')
  if (props.status === 'interrupted') return t('workflows.task.execution.interrupted')
  return t('workflows.task.progress.processFailed')
})
const stateClass = computed(() => ({
  'is-running': running.value,
  'is-failed': failed.value,
  'is-done': succeeded.value,
  'is-interrupted': props.status === 'interrupted' || props.status === 'stopped',
}))

const sortedEvents = computed(() => [...events.value].sort((a, b) => a.sequence - b.sequence))

function eventTime(item: SessionEventItem) {
  return Number(item.event?.timestamp || item.created_at || 0)
}

function formatDuration(startAt: number, endAt: number) {
  const diff = endAt - startAt
  if (diff <= 0 || diff > MAX_STEP_DURATION_MS) return ''
  // 不足 0.1s 时不再四舍五入成 "0.0s"：满屏 0.0s 会让人误以为这些步骤没有耗时。
  // 用 "<0.1s" 明确表达「快到测不出」，而不是「耗时为 0」。
  const seconds = diff / 1000
  if (seconds < 0.1) return '<0.1s'
  return `${seconds.toFixed(1)}s`
}

function newRow(
  key: string,
  kind: ProcessRow['kind'],
  content: string,
  live = false,
): ProcessRow {
  return {
    key,
    kind,
    content,
    live,
    name: '',
    args: '',
    result: '',
    durationText: '',
  }
}

// 权限请求内容优先取解码器给的摘要；缺失时回落到请求项里的工具名与入参
function permissionText(event?: SessionEventItem['event']) {
  const requests: PermissionRequestPayload[] = event?.permission_requests || []
  return requests
    .map((request) => [request.tool_name, request.tool_input].filter(Boolean).join(' '))
    .filter(Boolean)
    .join('；')
}

// 该轮最后一条输出就是最终结论，结束后正文区会展示它，过程内不再重复。
// 运行中必须保留：此时正文区只显示「正在执行」占位，隐藏这条会让用户看不到
// 模型正在写的内容。
function lastMessageSequence(list: SessionEventItem[]) {
  let sequence = 0
  for (const item of list) {
    if (item.event?.type === 'message' && (item.event.content || '').trim()) sequence = item.sequence
  }
  return sequence
}

// 思考耗时无法从协议直接取得：用「该思考事件到下一个事件」的间隔近似。
// 思考期间没有其他事件到达，这个间隔就是思考本身的时长。
function nextEventTimeBySequence(list: SessionEventItem[]) {
  const nextTime = new Map<number, number>()
  for (let index = 0; index < list.length - 1; index += 1) {
    nextTime.set(list[index].sequence, eventTime(list[index + 1]))
  }
  return nextTime
}

// 执行过程按事件顺序渲染成一条时间线：思考、中间输出、权限请求、工具调用混排，
// 工具结果归属到其调用行本体（不作为独立行），与设计稿的阅读顺序一致。
// 工具调用与结果按工具调用 ID 配对：并行工具调用的结果并不按调用顺序返回
// （实测 pi 会乱序回传），仅靠事件顺序无法归属，所以优先用 tool_use_id 精确配对；
// 协议不提供 ID 时，只在「同时只有一个调用待回结果」这种唯一候选的情况下才归属。
const rows = computed<ProcessRow[]>(() => {
  const list = sortedEvents.value
  const live = liveSequences.value
  const loaded = fullBySequence.value
  const finalMessageSequence = running.value ? 0 : lastMessageSequence(list)
  const nextTime = nextEventTimeBySequence(list)
  const result: ProcessRow[] = []
  const byId = new Map<string, ProcessRow>()
  // 以工具行对象自身为键记录开始时间，避免额外维护映射
  const startedAt = new Map<ProcessRow, number>()

  for (const item of list) {
    const type = item.event?.type
    const loadedText = loaded.get(item.sequence)
    const parsed = parseTruncMarker(loadedText ?? item.event?.content ?? '')
    const content = parsed.text.trim()
    const truncNote = loadedText ? '' : truncNoteOf(parsed.marker)

    if (type === 'thinking') {
      if (!content) continue
      const row = newRow(
        `thinking-${item.sequence}`,
        'thinking',
        content,
        live.has(item.sequence),
      )
      const nextAt = nextTime.get(item.sequence)
      if (nextAt) row.durationText = formatDuration(eventTime(item), nextAt)
      row.truncNote = truncNote
      row.sequence = truncNote ? item.sequence : undefined
      result.push(row)
      continue
    }

    if (type === 'message') {
      if (!content || item.sequence === finalMessageSequence) continue
      const row = newRow(
        `output-${item.sequence}`,
        'output',
        content,
        live.has(item.sequence),
      )
      row.truncNote = truncNote
      row.sequence = truncNote ? item.sequence : undefined
      result.push(row)
      continue
    }

    if (type === 'permission_request') {
      const text = content || permissionText(item.event)
      if (!text) continue
      result.push(newRow(`permission-${item.sequence}`, 'permission', text))
      continue
    }

    if (type === 'tool_call') {
      const separator = content.indexOf(' ')
      const row = newRow(`tool-${item.sequence}`, 'tool', '')
      row.name = separator > 0 ? content.slice(0, separator) : content
      row.args = separator > 0 ? content.slice(separator + 1).replace(/\s+/g, ' ').trim() : ''
      result.push(row)
      startedAt.set(row, eventTime(item))
      const toolUseId = (item.event?.tool_use_id || '').trim()
      if (toolUseId) byId.set(toolUseId, row)
      continue
    }

    if (type !== 'tool_result') continue
    const interrupted = item.event?.tool_result_interrupted === true
    if (!content && !interrupted) continue

    const toolUseId = (item.event?.tool_use_id || '').trim()
    let target: ProcessRow | undefined
    if (toolUseId) {
      // 1) 有 ID：精确归属
      target = byId.get(toolUseId)
    } else {
      // 2) 无 ID：仅唯一候选时归属
      const candidates = result.filter((row) => row.kind === 'tool' && !row.result)
      if (candidates.length === 1) target = candidates[0]
    }
    if (!target) continue

    // 同一调用可能先有增量输出、再有终态结果，保留最后一次非空内容
    target.result = content
    target.interrupted = interrupted
    target.truncNote = truncNote
    target.sequence = item.sequence
    const started = startedAt.get(target)
    if (started) target.durationText = formatDuration(started, eventTime(item))
  }
  return result
})

const toolCount = computed(() => rows.value.filter((row) => row.kind === 'tool').length)

// 汇总行 chip 与设计稿一致：CLI 名 · 版本 · 模型 · 工具调用次数 · token 用量；没有内容时不显示
const summaryChip = computed(() => {
  const parts: string[] = [cliDisplayName(props.cliType)]
  if (versionText.value) {
    parts.push(versionText.value)
  }
  if (props.modelName) {
    parts.push(props.modelName)
  }
  if (props.thinkingLevel) {
    parts.push(t('components.cliThinking.requested', { level: props.thinkingLevel }))
    if (props.thinkingResolution === 'sent' && props.thinkingLevelSent) {
      parts.push(t('components.cliThinking.sent', { level: props.thinkingLevelSent }))
    }
  }
  if (toolCount.value) {
    parts.push(t('workflows.task.progress.processToolCallCount', { count: toolCount.value }))
  }
  if (usageText.value) {
    parts.push(usageText.value)
  }
  return parts.filter(Boolean).join(' · ')
})

/** token 数做紧凑化，避免长数字撑破汇总行 */
function formatTokens(value: number) {
  if (value >= 1000) return `${(value / 1000).toFixed(1)}k`
  return String(value)
}

// token 用量：只有后端确实采集到才展示。
// copilot / kimi 等 CLI 本身不提供 usage，此时保持不显示，而不是显示无意义的 0。
const usageText = computed(() => {
  const input = props.inputTokens ?? 0
  const output = props.outputTokens ?? 0
  if (input <= 0 && output <= 0) return ''
  return `↑${formatTokens(input)} ↓${formatTokens(output)}`
})

// CLI 版本号来自 cli-discovery 的探测结果。
// 历史执行没有保存当时的 CLI 版本，禁止用当前机器版本冒充历史版本。
// 各 CLI 的 --version 输出常带后缀（如 "2.1.273 (Claude Code)"），只保留版本号本身；取不到就不展示。
const versionText = computed(() =>
  props.readOnly ? '' : cliVersion(props.cliType || '').replace(/\s*\(.*\)\s*$/, ''),
)

// 行默认展开态按「这行是否值得直接读」区分：输出行是过程里的结论性文本，默认展开；
// 思考与工具调用「做了什么」行头已经写清楚，默认收起，要看细节再点开（需求 2202 评论 1）。
/** 工具行右侧的完成态文案：设计稿在行尾直接标出「已执行完成」 */
function truncNoteOf(marker: ReturnType<typeof parseTruncMarker>['marker']) {
  if (!marker) return ''
  if (!marker.file) return t('workflows.task.progress.artifactUnsaved')
  return t('workflows.task.progress.artifactViewFull')
}

function toolStateText(row: ProcessRow) {
  if (row.interrupted) return t('workflows.task.progress.toolInterrupted')
  if (row.result) return t('workflows.task.progress.processToolDone')
  return running.value ? t('workflows.task.progress.processRunning') : ''
}

function isRowOpen(row: ProcessRow) {
  const override = rowOverrides.value.get(row.key)
  if (override !== undefined) return override
  return row.kind === 'output'
}

function toggleRow(row: ProcessRow) {
  const next = new Map(rowOverrides.value)
  next.set(row.key, !isRowOpen(row))
  rowOverrides.value = next
}

// 该行是否仍在流式揭示：执行中、来自实时流、且尚未打完
function isTypingRow(row: ProcessRow) {
  return running.value && row.live && !revealedKeys.value.has(row.key)
}

function markRevealed(key: string) {
  if (revealedKeys.value.has(key)) return
  const next = new Set(revealedKeys.value)
  next.add(key)
  revealedKeys.value = next
}

// 运行中每来一条事件，时间线就会变长：滚到底部，让最新进展始终可见
watch(rows, () => {
  if (!running.value || !expanded.value || !followTail) return
  scheduleFollowScroll()
})

// 展开执行过程时直接落在最新一条上，而不是从头开始读
watch(expanded, (value) => {
  if (!value) return
  followTail = true
  if (running.value) void scrollToLatest()
})

// Only a completed HTTP replay proves that all persisted events up to this
// cursor were received. A higher live event must not skip an unfinished replay.
let historyCursor = 0

async function loadHistory() {
  if (props.readOnly || !props.sessionUuid) return
  const sessionUuid = props.sessionUuid
  try {
    // 后端路由为 /tasks/sessions/:uuid/events，路径中不带 taskUuid；
    // 多带段会命中 404，导致任务完成后回看「执行过程」为空。
    const result = await apiClient.get<{ items: SessionEventItem[]; activity?: { kind?: string } }>(
      `/tasks/sessions/${encodeURIComponent(sessionUuid)}/events`,
      { after_sequence: historyCursor },
    )
    if (sessionUuid !== props.sessionUuid) return
    if (result.activity?.kind) activityHint.value = result.activity.kind
    mergeEvents(result.items || [])
    historyCursor = (result.items || []).reduce((cursor, item) => Math.max(cursor, item.sequence), historyCursor)
    historyLoaded.value = true
  } catch (error) {
    // 历史加载失败不阻塞实时事件展示；保留 historyLoaded=false，
    // 让用户下次展开时重试，避免一次失败后永久显示为空。
    console.warn('[execution-process] 执行过程历史加载失败', props.sessionUuid, String(error))
  }
}

function mergeEvents(incoming: SessionEventItem[], live = false) {
  if (!incoming.length) return
  const known = new Set(events.value.map((item) => item.sequence))
  const merged = [...events.value]
  const fresh: number[] = []
  for (const item of incoming) {
    if (item.sequence && !known.has(item.sequence)) {
      known.add(item.sequence)
      merged.push(item)
      fresh.push(item.sequence)
    }
  }
  // 只有实时推送的事件才算「正在生成」；历史补拉的内容直接整段渲染
  if (live && fresh.length) {
    const next = new Set(liveSequences.value)
    for (const sequence of fresh) next.add(sequence)
    liveSequences.value = next
  }
  events.value = merged
}

function subscribe() {
  if (!props.sessionUuid) return
  wsSend({
    type: 'executor.subscribe',
    task_uuid: props.taskUuid,
    session_uuid: props.sessionUuid,
    after_sequence: historyCursor,
  })
}

watch(() => props.sessionUuid, (sessionUuid, previous) => {
  if (previous && !props.readOnly) wsSend({ type: 'executor.unsubscribe', session_uuid: previous })
  clearTimeout(collapseTimer)
  historyCursor = 0
  historyLoaded.value = false
  events.value = []
  liveSequences.value = new Set()
  fullBySequence.value = new Map()
  activityHint.value = ''
  if (sessionUuid && !props.readOnly) {
    void loadHistory()
    if (running.value) subscribe()
  }
})

const connected = useLocalWSStatus()

// 断线重连后 WS 订阅会丢失，恢复连接时重新订阅并补拉历史
watch(connected, (value) => {
  if (!props.readOnly && value && running.value) {
    subscribe()
    void loadHistory()
  }
})

watch(
  () => props.status,
  (status, prev) => {
    if (props.readOnly) return
    if (status === 'created' || status === 'running') {
      expanded.value = true
      followTail = true
      void loadHistory()
      subscribe()
    }
    // 运行结束后自动收起，让最终输出成为焦点
    if (prev === 'running' && status && status !== 'running' && status !== 'created') {
      void loadHistory()
      wsSend({ type: 'executor.unsubscribe', session_uuid: props.sessionUuid })
      clearTimeout(collapseTimer)
      collapseTimer = setTimeout(() => {
        expanded.value = false
      }, 1000)
    }
  },
  { immediate: true },
)

interface ExecutorEventPayload {
  type: string
  content?: string
  session_id?: string
  tool_use_id?: string
  permission_requests?: PermissionRequestPayload[]
  timestamp?: number
  tool_result_interrupted?: boolean
  interrupted_attempt?: number
  activity?: { kind?: string }
  [key: string]: unknown
}

const unsubscribeEvents = onMessage('executor.event', (payload: Record<string, unknown>) => {
  if (props.readOnly) return
  const sessionUuid = String(payload?.session_uuid || '')
  if (!sessionUuid || sessionUuid !== props.sessionUuid) return
  const raw = payload.event
  const event: ExecutorEventPayload | null =
    typeof raw === 'string' ? (JSON.parse(raw) as ExecutorEventPayload) : (raw as ExecutorEventPayload)
  const sequence = Number(payload.sequence || 0)
  if (!event || !sequence) return
  if (event.type === 'activity') {
    activityHint.value = String(event.content || event.activity?.kind || '')
    if (activityHint.value === 'resumed') activityHint.value = ''
    return
  }
  if (event.type === 'thinking' || event.type === 'message' || event.type === 'tool_call' || event.type === 'tool_result') {
    activityHint.value = ''
  }
  mergeEvents([
    {
      session_uuid: sessionUuid,
      sequence,
      event_type: String(event.type || payload.event_type || ''),
      event: {
        type: event.type,
        content: event.content,
        timestamp: event.timestamp,
        session_id: typeof event.session_id === 'string' ? event.session_id : undefined,
        tool_use_id: typeof event.tool_use_id === 'string' ? event.tool_use_id : undefined,
        tool_result_interrupted: event.tool_result_interrupted === true,
        interrupted_attempt: typeof event.interrupted_attempt === 'number' ? event.interrupted_attempt : undefined,
        permission_requests: Array.isArray(event.permission_requests)
          ? event.permission_requests
          : undefined,
      },
      created_at: Number(event.timestamp || 0),
    },
  ], true)
})

function toggleExpanded() {
  expanded.value = !expanded.value
  // 首次展开时补拉历史事件（旧会话可能未订阅过实时流）
  if (!props.readOnly && expanded.value && !historyLoaded.value) {
    void loadHistory()
  }
}
</script>

<style scoped>
/* 执行过程区块：设计稿用细边框与圆角把它和消息正文分开 */
.exec-process {
  margin-top: 8px;
  padding: 8px 12px;
  border: 1px solid #eef0f3;
  border-radius: 10px;
  background: #fff;
}

/* 汇总行：与正文左对齐 */
.exec-summary {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 6px;
  padding: 3px 0;
  border: 0;
  background: transparent;
  cursor: pointer;
  text-align: left;
}
.exec-summary-glyph {
  flex: 0 0 auto;
  color: #8b8b8b;
  font-size: 13px;
}
.exec-summary-title {
  flex: 0 0 auto;
  color: #1f2328;
  font-size: 14px;
  font-weight: 500;
}
.exec-summary-chip {
  min-width: 0;
  overflow: hidden;
  padding: 1px 8px;
  border-radius: 6px;
  background: #f2f3f5;
  color: #8b8b8b;
  font-size: 12px;
  white-space: nowrap;
  text-overflow: ellipsis;
}
.exec-summary-gap {
  flex: 1;
  min-width: 8px;
}
.exec-summary-state {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  color: #8b8b8b;
  font-size: 12px;
}
.exec-summary-state.is-done {
  color: #16a34a;
}
.exec-summary-state.is-running {
  color: #185fa5;
}
.exec-summary-state.is-failed {
  color: #dc2626;
}
.exec-summary-state.is-interrupted,
.exec-row-state.is-interrupted {
  color: #b45309;
}
.exec-interrupt-hint,
.exec-full-link {
  margin: 4px 0 0;
  padding: 0;
  border: 0;
  background: transparent;
  color: #8b8b8b;
  font-size: 12px;
  cursor: pointer;
  text-align: left;
}

/* 时间线：无边框无底色，仅靠缩进与图标分层。
   不再限定高度与内部滚动：执行过程直接铺进消息流，由外层会话区统一滚动 */
.exec-body {
  display: flex;
  flex-direction: column;
  padding: 2px 0 2px 1px;
}
.exec-row {
  min-width: 0;
}
/* 行之间用极淡的分隔线，不再只靠缩进分层 */
.exec-row + .exec-row {
  border-top: 1px solid #f4f5f7;
}
.exec-row-head {
  display: flex;
  width: 100%;
  min-width: 0;
  align-items: center;
  gap: 6px;
  padding: 4px 0;
  border: 0;
  background: transparent;
  cursor: pointer;
  text-align: left;
}
.exec-row-head.is-static {
  cursor: default;
}
.exec-row-head:hover .exec-row-label,
.exec-row-head:hover .exec-row-tool {
  color: #1f2328;
}
.exec-row-icon {
  flex: 0 0 auto;
  color: #b9bcc2;
  font-size: 13px;
}
.icon-thinking {
  color: #8b8b8b;
}
.icon-done {
  color: #8b8b8b;
}
.icon-running {
  color: #185fa5;
}
.icon-permission {
  color: #dc2626;
}
.exec-row-label {
  flex: 0 0 auto;
  color: #595959;
  font-size: 13px;
}
.exec-row-label.is-permission {
  color: #dc2626;
}
.exec-row-tool {
  flex: 0 0 auto;
  color: #1f2328;
  font-family: 'SF Mono', Menlo, Consolas, monospace;
  font-size: 13px;
  font-weight: 500;
}
.exec-row-args {
  min-width: 0;
  overflow: hidden;
  color: #9ca3af;
  font-size: 13px;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.exec-row-gap {
  flex: 1;
  min-width: 8px;
}

/* 耗时：彩色小圆点 + 文本，完成绿 / 进行中蓝 */
.exec-duration {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 4px;
  color: #6b7280;
  font-size: 12px;
}
.exec-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #16a34a;
}
.exec-duration.is-running .exec-dot {
  background: #185fa5;
  animation: exec-pulse 1.4s ease-in-out infinite;
}
@keyframes exec-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.35;
  }
}
.exec-caret {
  flex: 0 0 auto;
  color: #c4c7cc;
  font-size: 10px;
}
/* 行展开控件：设计稿用方形 +/- 按钮，与卡片头的箭头 caret 区分开 */
.exec-row-caret {
  display: inline-flex;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border: 1px solid #e5e7eb;
  border-radius: 4px;
  color: #8b8b8b;
  background: #fff;
  font-size: 10px;
}
/* 工具行尾的完成态文案 */
.exec-row-state {
  flex: 0 0 auto;
  font-size: 12px;
  white-space: nowrap;
}
.exec-row-state.is-done {
  color: #9ca3af;
}
.exec-row-state.is-running {
  color: #185fa5;
}

/* 行内容：缩进一级后做成浅灰内容块（设计稿把展开内容统一放进灰底块，等宽字体贴近命令行输出） */
.exec-row-text {
  margin: 2px 0 8px 19px;
  padding: 8px 10px;
  border-radius: 8px;
  background: #f2f3f5;
  color: #4b5563;
  font-family: 'SF Mono', Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.7;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.exec-row-text.is-permission-text {
  background: #fef2f2;
  color: #a32d2d;
}
.exec-tool-result {
  min-width: 0;
}
.exec-result-label {
  margin: 0;
  padding-left: 19px;
  color: #9ca3af;
  font-size: 12px;
}
.exec-row-text.is-plain {
  margin-bottom: 8px;
}
.exec-running-hint {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0;
  padding: 0 0 6px 19px;
  color: #185fa5;
  font-size: 12px;
}
.exec-empty {
  margin: 2px 0;
  padding-left: 19px;
  color: #9ca3af;
  font-size: 12px;
}
</style>
