import apiClient from '@/api/client'
import { t } from '@/i18n'
import type { TaskExecutionHistoryDetail } from '@/types/task-execution-history'

interface ExecutionRecords extends TaskExecutionHistoryDetail {
  sessions: Record<string, unknown>[]
  conversation_summaries: Record<string, unknown>[]
}
interface ConversationRecords { title: string; executions: ExecutionRecords[] }
function heading(value: string) { return value.replace(/[\r\n]+/g, ' ') }
function jsonBlock(value: unknown) {
  const text = JSON.stringify(value, null, 2)
  const runs = text.match(/`+/g) || []
  const fence = '`'.repeat(Math.max(3, ...runs.map(run => run.length + 1)))
  return `${fence}json\n${text}\n${fence}`
}
export async function exportConversationMarkdown(taskUuid: string) {
  const data = await apiClient.get<ConversationRecords>(`/tasks/${encodeURIComponent(taskUuid)}/records`)
  const lines = [`# ${heading(data.title)}`, '', `Task: ${taskUuid}`, '']
  for (const execution of data.executions) {
    lines.push(`## ${t('layout.sidebar.exportExecution', { count: execution.execution_no })} · ${heading(execution.target_name || execution.execution_tool || execution.execution_mode)}`, '')
    const steps = new Map(execution.steps.map(step => [step.uuid, step.name]))
    const emittedSessions = new Set<string>()
    for (const record of execution.progress) {
      lines.push(`### ${heading(steps.get(record.task_step_uuid) || record.cli_type || 'AI')} · ${new Date(record.created_at).toISOString()}`, '', `Status: ${record.status}`, '')
      if (record.user_prompt) lines.push(`#### ${t('layout.sidebar.exportUser')}`, '', record.user_prompt, '')
      if (record.session_uuid && !emittedSessions.has(record.session_uuid)) {
        emittedSessions.add(record.session_uuid)
        for (const event of execution.session_events[record.session_uuid] || []) {
          if (event.event?.content) lines.push(`#### AI · ${heading(event.event_type)} · ${new Date(event.created_at).toISOString()}`, '', event.event.content, '')
        }
      }
      if (record.final_result) lines.push(`#### ${t('layout.sidebar.exportAssistant')}`, '', record.final_result, '')
    }
    // 原始事件保留工具调用、流式片段、错误及运行中的输出，防止只导出最终回复而遗漏记录。
    lines.push(`### ${t('layout.sidebar.exportRecords')}`, '', jsonBlock({
      steps: execution.steps,
      progress: execution.progress,
      sessions: execution.sessions,
      session_events: execution.session_events,
      conversation_summaries: execution.conversation_summaries,
    }), '')
  }
  const blob = new Blob([lines.join('\n')], { type: 'text/markdown;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = `${data.title.replace(/[<>:"/\\|?*\u0000-\u001f]/g, '_').slice(0, 100) || 'conversation'}-${taskUuid.slice(0, 8)}.md`
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
  // 保留下载启动所需的时间，并释放临时 URL。
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
