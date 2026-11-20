<template>
  <div class="markdown-preview-shell">
    <a-empty v-if="!content.trim()" :description="emptyText || t('components.markdown.empty')" />
    <article
      v-else
      ref="previewRef"
      class="markdown-preview"
      :class="{ 'image-thumbnails': imageThumbnails }"
      @click="handleAttachmentClick"
      v-html="renderedContent"
    ></article>
    <a-modal :open="Boolean(previewImage)" :footer="null" width="min(90vw, 1000px)" centered destroy-on-close @update:open="previewImage = undefined">
      <img v-if="previewImage" class="full-image" :src="previewImage.src" :alt="previewImage.alt" />
    </a-modal>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import MarkdownIt from 'markdown-it'
import apiClient from '@/api/client'
import { revealTaskOutputFile } from '@/composables/useDesktop'
import { useAppI18n } from '@/i18n'
import { allowLocalFileLinks, LOCAL_FILE_LINK_ATTRIBUTE, localFileTarget } from '@/utils/localFileLink'

const { t } = useAppI18n()

const props = withDefaults(defineProps<{
  content?: string
  emptyText?: string
  taskUuid?: string
  sessionUuid?: string
  fileRoots?: string[]
  imageThumbnails?: boolean
  resolveImage?: (source: string, signal: AbortSignal) => Promise<Blob | undefined>
  openLink?: (href: string) => boolean
}>(), {
  content: '',
  emptyText: '',
  taskUuid: '',
  sessionUuid: '',
  fileRoots: () => [],
})
const emit = defineEmits<{
  'attachments-loaded': []
}>()

const markdown = new MarkdownIt({
  breaks: true,
  linkify: true,
})
allowLocalFileLinks(markdown)

// 仅合并连续的纯图片段落，保留正文、说明和代码的原有顺序。
markdown.core.ruler.after('inline', 'image_thumbnail_groups', (state) => {
  if (!props.imageThumbnails) return
  const grouped: typeof state.tokens = []
  let previousImages = false
  for (let index = 0; index < state.tokens.length; index++) {
    const open = state.tokens[index]
    const inline = state.tokens[index + 1]
    const close = state.tokens[index + 2]
    const children = inline?.children || []
    const imagesOnly = open?.type === 'paragraph_open' && inline?.type === 'inline' && close?.type === 'paragraph_close' &&
      children.some(child => child.type === 'image') &&
      children.every(child => child.type === 'image' || child.type === 'softbreak' || child.type === 'hardbreak' || child.type === 'text' && !child.content.trim())
    if (imagesOnly) {
      open.tag = close.tag = 'div'
      open.attrJoin('class', 'markdown-image-grid')
      inline.children = children.filter(child => child.type === 'image')
      if (previousImages) grouped.pop()
      else grouped.push(open)
      grouped.push(inline, close)
      index += 2
    } else if (open) {
      grouped.push(open)
    }
    previousImages = imagesOnly
  }
  state.tokens = grouped
})

const previewRef = ref<HTMLElement>()
const objectUrls = new Set<string>()
const previewImage = ref<{ src: string; alt: string }>()
let hydrationVersion = 0
let imageController: AbortController | undefined

function taskAttachmentPath(value: string) {
  const normalized = value.trim().replace(/\\/g, '/').replace(/[?#].*$/, '')
  const match = normalized.match(/(?:^|\/)attachments\/([0-9a-f-]{36}\.[a-z0-9]{1,16})$/i)
  return match ? `attachments/${match[1]}` : ''
}

const originalImageRenderer = markdown.renderer.rules.image
markdown.renderer.rules.image = (tokens, index, options, env, self) => {
  const token = tokens[index]
  const source = token.attrGet('src') || ''
  const path = env.taskUuid ? taskAttachmentPath(source) : ''
  const localImage = env.taskUuid && !/^(?:https?:|data:|blob:|\/\/)/i.test(source)
  const managed = props.resolveImage || path || localImage
  if (managed || props.imageThumbnails) {
    const sourceIndex = token.attrIndex('src')
    if (sourceIndex >= 0) token.attrs?.splice(sourceIndex, 1)
    if (props.resolveImage || !managed) token.attrSet('data-preview-image-source', source)
    else if (path) token.attrSet('data-task-attachment-path', path)
    else token.attrSet('data-local-image-path', localFileTarget(source) || source)
    token.attrSet('hidden', '')
  }
  const html = originalImageRenderer
    ? originalImageRenderer(tokens, index, options, env, self)
    : self.renderToken(tokens, index, options)
  if (!managed && !props.imageThumbnails) return html
  const escape = markdown.utils.escapeHtml
  return `<span class="markdown-local-image"><button type="button" class="markdown-image-preview" disabled aria-label="${escape(t('components.markdown.previewAttachment', { name: token.content }))}">${html}<span class="markdown-image-status" role="status">${escape(t('components.markdown.imageLoading'))}</span></button><button type="button" class="markdown-image-retry" hidden>${escape(t('common.actions.retry'))}</button></span>`
}

const originalLinkOpenRenderer = markdown.renderer.rules.link_open
markdown.renderer.rules.link_open = (tokens, index, options, env, self) => {
  const token = tokens[index]
  const href = token.attrGet('href') || ''
  const path = env.taskUuid ? taskAttachmentPath(href) : ''
  const fileTarget = localFileTarget(href)
  if (path) {
    token.attrSet('href', '#')
    token.attrSet('data-task-attachment-path', path)
  } else if (env.taskUuid && fileTarget) {
    token.attrSet('href', '#')
    token.attrSet(LOCAL_FILE_LINK_ATTRIBUTE, fileTarget)
    token.attrSet('title', t('components.markdown.revealInFolder'))
  }
  return originalLinkOpenRenderer
    ? originalLinkOpenRenderer(tokens, index, options, env, self)
    : self.renderToken(tokens, index, options)
}

// `#[文件名]` 是输入框文档引用的持久化标记（见 task-progress/documentMention.ts）。
// 在这里把它解析成行内节点，让历史消息也渲染成文件标签，而不是把内部语法直接摆给用户看。
const DOC_REFERENCE_RE = /^#\[([^\]\n]{1,200})\]/

// S-UI-13: 知识库选中片段会在文件名前追加「· 选」后缀，聊天记录里沿用相同标记。
// 历史消息没有运行时引用登记，按 i18n 文本识别「· 选」并区分渲染。
function splitDocReferenceLabel(label: string) {
  const fragmentBadge = ' · '
  const badgeIdx = label.lastIndexOf(fragmentBadge)
  if (badgeIdx < 0) return { fileName: label, badge: '', hasBadge: false }
  // 段尾的「 · 选」= 选中片段标记；其余「 · N」= 重复去重序号，归文件名部分
  const possible = label.slice(badgeIdx + fragmentBadge.length)
  // 去重序号是纯数字，选中片段 badge 长度很短且不是纯数字
  const isFragmentBadge = !/^\d+$/.test(possible)
  return {
    fileName: label.slice(0, badgeIdx),
    badge: isFragmentBadge ? `${fragmentBadge}${possible}` : '',
    hasBadge: isFragmentBadge,
  }
}

markdown.inline.ruler.before('emphasis', 'doc_reference', (state, silent) => {
  if (state.src.charCodeAt(state.pos) !== 0x23 /* # */) return false
  const match = DOC_REFERENCE_RE.exec(state.src.slice(state.pos))
  if (!match) return false
  if (!silent) {
    const token = state.push('doc_reference', '', 0)
    // 先存整段标签：渲染时再按当前语言识别「· 选」后缀，避免 locale 切换后渲染与解析错位
    token.content = match[1]
  }
  state.pos += match[0].length
  return true
})

markdown.renderer.rules.doc_reference = (tokens, index) => {
  const { fileName, badge, hasBadge } = splitDocReferenceLabel(tokens[index].content)
  // badge 是从 marker 原始文本里切出的 i18n 后缀（如「 · 选」），直接拼入 HTML 不需要再次转义
  const className = hasBadge
    ? 'markdown-doc-reference markdown-doc-reference-fragment'
    : 'markdown-doc-reference'
  const titleAttr = hasBadge
    ? ` title="${markdown.utils.escapeHtml(t('workflows.task.progress.referenceFragmentHint'))}"`
    : ''
  const badgeHtml = hasBadge
    ? `<span class="markdown-doc-reference-badge">${markdown.utils.escapeHtml(badge)}</span>`
    : ''
  return `<span class="${className}"${titleAttr}>${markdown.utils.escapeHtml(fileName)}${badgeHtml}</span>`
}

// 渲染前归一化：统一换行符、折叠连续空行、去行尾空格，避免脏换行产生多余间距
function normalizeMarkdown(text: string) {
  return text
    .replace(/\r\n/g, '\n')
    .replace(/\n{3,}/g, '\n\n')
    .replace(/[ \t]+\n/g, '\n')
    .trim()
}

const renderedContent = computed(() =>
  markdown.render(normalizeMarkdown(props.content || ''), {
    taskUuid: props.taskUuid,
    sessionUuid: props.sessionUuid,
    fileRoots: props.fileRoots,
  }),
)

watch(
  () => [renderedContent.value, props.taskUuid, props.sessionUuid, props.resolveImage] as const,
  () => void hydrateAttachmentImages(),
  { immediate: true },
)

async function hydrateAttachmentImages() {
  const version = ++hydrationVersion
  imageController?.abort()
  const controller = new AbortController()
  imageController = controller
  previewImage.value = undefined
  releaseObjectUrls()
  await nextTick()
  if (version !== hydrationVersion) return
  const images = [...(previewRef.value?.querySelectorAll<HTMLImageElement>('.markdown-local-image img') || [])]
  let nextImage = 0
  await Promise.all(Array.from({ length: Math.min(4, images.length) }, async () => {
    while (nextImage < images.length && !controller.signal.aborted) {
      const image = images[nextImage++]
      if (image) await loadPreviewImage(image, version, controller.signal)
    }
  }))
  if (version === hydrationVersion) emit('attachments-loaded')
}

async function loadPreviewImage(image: HTMLImageElement, version: number, signal: AbortSignal) {
  if (image.dataset.loading === String(version) || signal.aborted) return
  const card = image.closest('.markdown-local-image')
  const button = card?.querySelector<HTMLButtonElement>('.markdown-image-preview')
  const status = card?.querySelector<HTMLElement>('.markdown-image-status')
  const retry = card?.querySelector<HTMLButtonElement>('.markdown-image-retry')
  if (!button || !status || !retry) return
  image.dataset.loading = String(version)
  image.hidden = true
  button.disabled = true
  retry.hidden = true
  status.hidden = false
  status.textContent = t('components.markdown.imageLoading')
  let objectUrl = ''
  try {
    const source = image.dataset.previewImageSource
    let blob: Blob | undefined
    if (source !== undefined) {
      blob = await props.resolveImage?.(source, signal)
    } else {
      const attachment = image.dataset.taskAttachmentPath
      blob = await apiClient.getBlob(
        `/tasks/${encodeURIComponent(props.taskUuid)}/files/${attachment ? 'raw' : 'image'}`,
        { path: attachment || image.dataset.localImagePath || '', ...(attachment ? {} : { session_uuid: props.sessionUuid }) },
        signal,
      )
    }
    if (version !== hydrationVersion || signal.aborted || !image.isConnected) return
    if (blob) {
      if (!blob.type.startsWith('image/')) throw new Error('Invalid image')
      objectUrl = URL.createObjectURL(blob)
      objectUrls.add(objectUrl)
      image.src = objectUrl
    } else if (source) {
      image.src = source
    } else {
      throw new Error('Image unavailable')
    }
    await image.decode()
    if (version !== hydrationVersion || signal.aborted || !image.isConnected) return
    image.hidden = false
    status.hidden = true
    button.disabled = false
  } catch {
    if (objectUrl) {
      URL.revokeObjectURL(objectUrl)
      objectUrls.delete(objectUrl)
    }
    if (version !== hydrationVersion || signal.aborted || !image.isConnected) return
    image.removeAttribute('src')
    status.textContent = t('components.markdown.imageUnavailable')
    retry.hidden = false
  } finally {
    if (image.dataset.loading === String(version)) delete image.dataset.loading
  }
}

async function handleAttachmentClick(event: MouseEvent) {
  const target = event.target
  if (!(target instanceof Element)) return
  const card = target.closest('.markdown-local-image')
  const image = card?.querySelector<HTMLImageElement>('img')
  if (image && target.closest('.markdown-image-retry')) {
    event.preventDefault()
    if (imageController) {
      await loadPreviewImage(image, hydrationVersion, imageController.signal)
      emit('attachments-loaded')
    }
    return
  }
  if (image && target.closest('.markdown-image-preview')) {
    event.preventDefault()
    if (image.naturalWidth && !image.hidden) previewImage.value = { src: image.src, alt: image.alt }
    return
  }
  const link = target.closest<HTMLAnchorElement>('a[href]')
  if (link && props.openLink?.(link.getAttribute('href') || '')) {
    event.preventDefault()
    return
  }
  const fileAnchor = target.closest<HTMLAnchorElement>(`a[${LOCAL_FILE_LINK_ATTRIBUTE}]`)
  const fileRef = fileAnchor?.getAttribute(LOCAL_FILE_LINK_ATTRIBUTE)
  if (fileAnchor && fileRef && props.taskUuid) {
    event.preventDefault()
    try {
      const result = await revealTaskOutputFile(props.taskUuid, fileRef, props.sessionUuid)
      if (result === 'copied') message.info(t('components.markdown.revealUseDesktop'))
    } catch (error) {
      message.error(error instanceof Error && error.message ? error.message : t('components.markdown.revealFailed'))
    }
    return
  }
  const anchor = target.closest<HTMLAnchorElement>('a[data-task-attachment-path]')
  const path = anchor?.dataset.taskAttachmentPath
  if (!anchor || !path || !props.taskUuid) return
  event.preventDefault()
  try {
    const blob = await apiClient.getBlob(
      `/tasks/${encodeURIComponent(props.taskUuid)}/files/raw`,
      { path },
    )
    const objectUrl = URL.createObjectURL(blob)
    const download = document.createElement('a')
    download.href = objectUrl
    download.download = anchor.textContent?.trim() || path.split('/').at(-1) || 'attachment'
    download.click()
    URL.revokeObjectURL(objectUrl)
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('components.markdown.attachmentDownloadFailed'))
  }
}

function releaseObjectUrls() {
  for (const objectUrl of objectUrls) URL.revokeObjectURL(objectUrl)
  objectUrls.clear()
}

onBeforeUnmount(() => {
  hydrationVersion += 1
  imageController?.abort()
  releaseObjectUrls()
})
</script>

<style scoped>
.markdown-preview-shell {
  min-height: 100%;
  padding: 20px 26px 48px;
  box-sizing: border-box;
  background: #fff;
}

.markdown-preview-shell :deep(.ant-empty) {
  margin-top: 96px;
}

/*
 * 文档引用标签。与输入框内的引用标签、输入框上方的引用 chip 保持同一套视觉：
 * 100px 圆角胶囊、浅灰底、深色字，只展示文件名，不暴露 #[...] 内部语法。
 */
.markdown-preview :deep(.markdown-doc-reference) {
  display: inline-block;
  max-width: 240px;
  padding: 1px 8px;
  border-radius: 100px;
  color: rgba(0, 0, 0, 0.9);
  background: #f2f2f2;
  font-size: 13px;
  font-weight: 500;
  line-height: 20px;
  vertical-align: -4px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/*
 * S-UI-13: 知识库选中片段引用，与整篇文档引用做视觉区分。
 * 与 ChatComposer 输入框的 .mention-token-fragment 保持同一套配色（Primary Soft + 主蓝文字），
 * 避免用户在输入框 / 聊天记录 / 历史消息里看到三种不同的「选中片段」表现。
 */
.markdown-preview :deep(.markdown-doc-reference-fragment) {
  color: #3157e2;
  background: #e5efff;
}

/*
 * S-UI-13: 「· 选」后缀比主文件名淡一些，视觉上不抢主名风头。
 * 整体胶囊是蓝底，所以 badge 用更浅的蓝或半透明白。
 */
.markdown-preview :deep(.markdown-doc-reference-fragment .markdown-doc-reference-badge) {
  color: rgba(49, 87, 226, 0.6);
}

.markdown-preview {
  /* 覆盖消息卡片继承的 pre-wrap：渲染 HTML 里的格式换行不再产生间距，
     内容中的真实换行仍由 breaks: true 转成 <br> 保留 */
  white-space: normal;
  overflow-wrap: anywhere;
  color: #2f3440;
  font-size: 14px;
  line-height: 1.6;
}

.markdown-preview :deep(h1),
.markdown-preview :deep(h2),
.markdown-preview :deep(h3),
.markdown-preview :deep(h4),
.markdown-preview :deep(h5),
.markdown-preview :deep(h6) {
  color: #1f2430;
  font-weight: 650;
  line-height: 1.35;
  margin: 1.2em 0 0.5em;
}

.markdown-preview :deep(h1) {
  padding-bottom: 0.35em;
  border-bottom: 1px solid #e7eaf0;
  font-size: 1.9em;
}

.markdown-preview :deep(h2) {
  padding-bottom: 0.3em;
  border-bottom: 1px solid #eef0f4;
  font-size: 1.5em;
}

.markdown-preview :deep(h3) {
  font-size: 1.24em;
}

.markdown-preview :deep(h4) {
  font-size: 1em;
}

.markdown-preview :deep(h5) {
  font-size: 0.92em;
}

.markdown-preview :deep(h6) {
  font-size: 0.85em;
}

.markdown-preview :deep(p),
.markdown-preview :deep(ul),
.markdown-preview :deep(ol),
.markdown-preview :deep(blockquote),
.markdown-preview :deep(pre),
.markdown-preview :deep(table) {
  margin: 0.8em 0;
}

.markdown-preview :deep(> :first-child) {
  margin-top: 0;
}

.markdown-preview :deep(> :last-child) {
  margin-bottom: 0;
}

.markdown-preview :deep(a) {
  color: #3a7a3a;
  text-decoration: none;
}

.markdown-preview :deep(a:hover) {
  text-decoration: underline;
}

.markdown-preview :deep(code) {
  padding: 0.15em 0.35em;
  border-radius: 4px;
  background: #f3f6f1;
  color: #b04b67;
  font-family: "SFMono-Regular", Consolas, monospace;
  font-size: 0.88em;
}

.markdown-preview :deep(pre) {
  overflow-x: auto;
  padding: 14px 16px;
  border-radius: 7px;
  background: #1f2430;
  font-size: 0.92em;
  line-height: 1.6;
}

.markdown-preview :deep(pre code) {
  padding: 0;
  background: transparent;
  color: #e8eaf0;
}

.markdown-preview :deep(blockquote) {
  margin-left: 0;
  padding-left: 14px;
  border-left: 3px solid #9fb39a;
  color: #686f7c;
}

.markdown-preview :deep(table) {
  width: 100%;
  border-collapse: collapse;
}

.markdown-preview :deep(th),
.markdown-preview :deep(td) {
  padding: 7px 10px;
  border: 1px solid #dfe3ea;
  text-align: left;
}

.markdown-preview :deep(th) {
  background: #f7f9f4;
}

.markdown-preview :deep(img) {
  max-width: 100%;
}

.markdown-preview :deep(.markdown-local-image) {
  display: inline-flex;
  flex-direction: column;
  max-width: 100%;
  overflow: hidden;
  border: 1px solid #d9d9d9;
  border-radius: 12px;
  vertical-align: middle;
}
.markdown-preview :deep(.markdown-image-preview) {
  display: flex;
  align-items: center;
  justify-content: center;
  min-width: 120px;
  min-height: 80px;
  padding: 0;
  border: 0;
  background: #f5f6f8;
  cursor: zoom-in;
}
.markdown-preview :deep(.markdown-image-preview:disabled) { cursor: default; }
.markdown-preview :deep(.markdown-local-image img:not([hidden])) {
  display: block;
  max-width: 100%;
  max-height: 240px;
  object-fit: contain;
}
.markdown-preview :deep(.markdown-image-status) { padding: 16px; color: #595959; font-size: 12px; }
.markdown-preview :deep(.markdown-image-retry) {
  min-height: 32px;
  border: 0;
  background: #fff;
  color: #3157e2;
  cursor: pointer;
}
.markdown-preview :deep(.markdown-local-image button:focus-visible) { outline: 2px solid #3157e2; outline-offset: -2px; }
.image-thumbnails :deep(.markdown-image-grid) {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin: 8px 0;
}
.image-thumbnails :deep(.markdown-local-image) {
  box-sizing: border-box;
  flex: 0 0 160px;
  width: 160px;
  height: 120px;
  border-radius: 8px;
  background: #f5f6f8;
}
.image-thumbnails :deep(.markdown-image-preview) {
  flex: 1;
  min-width: 0;
  min-height: 0;
  width: 100%;
  overflow: hidden;
}
.image-thumbnails :deep(.markdown-local-image img:not([hidden])) {
  width: 100%;
  height: 100%;
  max-height: none;
}
.image-thumbnails :deep(.markdown-image-status) { padding: 8px; }
.image-thumbnails :deep(.markdown-image-retry) {
  flex-shrink: 0;
  min-height: 28px;
  background: transparent;
}
.full-image { display: block; max-width: 100%; max-height: 80vh; margin: auto; object-fit: contain; }

@media (max-width: 640px) {
  .markdown-preview-shell {
    padding: 16px 18px 36px;
  }
}
</style>
