import { computed, shallowReactive, toValue } from 'vue'
import type { MaybeRefOrGetter } from 'vue'
import type { KnowledgeDocumentReference } from '@/types/knowledge-reference'

// 只活在当前渲染进程里。切到其他页面再回来时恢复输入，关掉客户端后自然消失。
const drafts = shallowReactive(new Map<string, {
  value: string
  references: KnowledgeDocumentReference[]
}>())

export const NEW_CONVERSATION_DRAFT_KEY = 'new-conversation'

/** 正文直接按当前会话取值和写入，避免异步监听把旧正文保存到新会话。 */
export function useComposerDraft(key: MaybeRefOrGetter<string>) {
  return computed({
    get: () => readComposerDraft(toValue(key)),
    set: (value: string) => writeComposerDraft(toValue(key), value),
  })
}

export function readComposerDraft(key: string) {
  return key ? drafts.get(key)?.value ?? '' : ''
}

export function writeComposerDraft(key: string, value: string) {
  if (!key) return
  if (value) drafts.set(key, { value, references: drafts.get(key)?.references ?? [] })
  else drafts.delete(key)
}

function copyReference(reference: KnowledgeDocumentReference): KnowledgeDocumentReference {
  return {
    ...reference,
    knowledge: {
      ...reference.knowledge,
      fragment: reference.knowledge.fragment ? { ...reference.knowledge.fragment } : null,
    },
  }
}

export function readComposerDraftReferences(key: string): KnowledgeDocumentReference[] {
  const draft = key ? drafts.get(key) : undefined
  return draft?.references
    .filter((reference) => draft.value.includes(reference.marker))
    .map(copyReference) ?? []
}

export function writeComposerDraftReferences(key: string, references: KnowledgeDocumentReference[]) {
  if (!key) return
  const value = drafts.get(key)?.value ?? ''
  if (!value && !references.length) {
    drafts.delete(key)
    return
  }
  drafts.set(key, { value, references: references.map(copyReference) })
}

/** 流水线按步骤隔离草稿，其他执行方式保留任务级草稿。 */
export function composerDraftKey(taskUuid: string, stepUuid = '') {
  return taskUuid && stepUuid ? `${taskUuid}:step:${stepUuid}` : taskUuid
}
