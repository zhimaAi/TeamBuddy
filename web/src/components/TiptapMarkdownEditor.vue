<template>
  <div
    ref="rootRef"
    class="tiptap-markdown-editor"
    :class="{
      'is-disabled': disabled,
      'is-compact': variant === 'compact',
    }"
  >
    <BubbleMenu
      v-if="editor"
      :editor="editor"
      :should-show="shouldShowBubbleMenu"
      :options="bubbleMenuOptions"
    >
      <div
        class="floating-toolbar"
        role="toolbar"
        :aria-label="t('components.markdown.floatingToolbar')"
      >
        <div class="floating-menu-anchor">
          <button
            type="button"
            class="floating-tool floating-type-trigger"
            :aria-label="t('components.markdown.blockType')"
            aria-haspopup="menu"
            :aria-expanded="typeMenuOpen"
            @pointerdown.prevent.stop="toggleTypeMenu"
            @keydown.enter.prevent="toggleTypeMenu"
            @keydown.space.prevent="toggleTypeMenu"
          >
            <span class="floating-type-label">
              <img
                v-if="currentBlockTypeItem.iconSrc"
                :src="currentBlockTypeItem.iconSrc"
                alt=""
                class="block-type-icon block-type-image-icon"
              />
              <component
                :is="currentBlockTypeItem.icon"
                v-else-if="currentBlockTypeItem.icon"
                class="block-type-icon"
              />
              <template v-else>{{ currentBlockTypeItem.shortLabel }}</template>
            </span>
            <DownOutlined class="floating-type-chevron" />
          </button>
          <div
            v-if="typeMenuOpen"
            class="floating-popover block-type-menu"
            role="menu"
            @pointerdown.stop
          >
            <button
              v-for="item in BLOCK_TYPE_ITEMS"
              :key="item.key"
              type="button"
              class="block-type-menu-item"
              :class="{ 'is-selected': currentBlockType === item.key }"
              role="menuitemradio"
              :aria-checked="currentBlockType === item.key"
              @pointerdown.prevent="applyBlockType(item.key)"
              @keydown.enter.prevent="applyBlockType(item.key)"
              @keydown.space.prevent="applyBlockType(item.key)"
            >
              <span class="block-type-menu-icon">
                <img
                  v-if="item.iconSrc"
                  :src="item.iconSrc"
                  alt=""
                  class="block-type-icon block-type-image-icon"
                />
                <component
                  :is="item.icon"
                  v-else-if="item.icon"
                  class="block-type-icon"
                />
                <template v-else>{{ item.shortLabel }}</template>
              </span>
              <span>{{ t(item.label) }}</span>
            </button>
          </div>
        </div>

        <button
          v-for="tool in FLOATING_TOOLS"
          :key="tool.key"
          type="button"
          class="floating-tool"
          :class="{ active: isFloatingToolActive(tool.key) }"
          :aria-label="t(tool.label)"
          @pointerdown.prevent="applyFloatingTool(tool.key)"
          @keydown.enter.prevent="applyFloatingTool(tool.key)"
          @keydown.space.prevent="applyFloatingTool(tool.key)"
        >
          <component :is="tool.icon" />
        </button>

        <div class="floating-menu-anchor">
          <button
            type="button"
            class="floating-tool floating-color-trigger"
            :aria-label="t('components.markdown.textColor')"
            aria-haspopup="menu"
            :aria-expanded="colorMenuOpen"
            @pointerdown.prevent.stop="toggleColorMenu"
            @keydown.enter.prevent="toggleColorMenu"
            @keydown.space.prevent="toggleColorMenu"
          >
            <span
              class="floating-color-letter"
              :style="{ color: selectedTextColor }"
              aria-hidden="true"
            >
              {{ TEXT_COLOR_ICON }}
            </span>
            <DownOutlined class="floating-type-chevron" />
          </button>
          <div
            v-if="colorMenuOpen"
            class="floating-popover text-color-menu"
            role="menu"
            :aria-label="t('components.markdown.textColor')"
            :class="{ 'has-custom-colors': customColors }"
            @pointerdown.stop
          >
            <button
              v-for="item in TEXT_COLOR_ITEMS"
              :key="item.key"
              type="button"
              class="text-color-item"
              :class="{ 'is-selected': selectedTextColor === item.value }"
              role="menuitemradio"
              :aria-checked="selectedTextColor === item.value"
              :aria-label="t(item.label)"
              :title="t(item.label)"
              @pointerdown.prevent="applyTextColor(item)"
              @keydown.enter.prevent="applyTextColor(item)"
              @keydown.space.prevent="applyTextColor(item)"
            >
              <span
                :class="['text-color-swatch', `is-${item.key}`]"
                aria-hidden="true"
              >
                {{ TEXT_COLOR_ICON }}
              </span>
            </button>
            <div v-if="customColors" class="custom-color-controls" role="group" :aria-label="t('components.markdown.customColor')">
              <input
                :value="customColor"
                class="custom-color-picker"
                type="color"
                :aria-label="t('components.markdown.customColor')"
                @input="updateCustomColor"
              />
              <input
                :value="customColor"
                class="custom-color-hex"
                type="text"
                maxlength="7"
                :aria-label="t('components.markdown.colorHex')"
                @input="updateCustomColor"
              />
              <button type="button" @click="applyCustomColor(true)">
                {{ t('components.markdown.resetTextColor') }}
              </button>
            </div>
          </div>
        </div>

        <form
          v-if="linkEditorOpen"
          class="floating-popover link-editor"
          @pointerdown.stop
          @submit.prevent="applyLink"
        >
          <input
            ref="linkInputRef"
            v-model="linkUrl"
            type="url"
            :aria-label="t('components.markdown.linkUrl')"
            :placeholder="t('components.markdown.linkUrlPlaceholder')"
            @keydown.esc.prevent="closeLinkEditor"
          />
          <button
            type="submit"
            class="link-confirm"
            :aria-label="t('components.markdown.confirm')"
          >
            <CheckOutlined />
          </button>
          <button
            v-if="editor.isActive('link')"
            type="button"
            class="link-remove"
            :aria-label="t('components.markdown.delete')"
            @pointerdown.prevent="removeLink"
          >
            <DeleteOutlined />
          </button>
        </form>
      </div>
    </BubbleMenu>

    <EditorContent
      :editor="editor"
      class="tiptap-editor-content"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from 'vue'
import type { Component } from 'vue'
import { mergeAttributes, Node } from '@tiptap/core'
import type { Editor as TiptapEditor } from '@tiptap/core'
import { Markdown } from '@tiptap/markdown'
import Highlight from '@tiptap/extension-highlight'
import { TaskItem, TaskList } from '@tiptap/extension-list'
import Placeholder from '@tiptap/extension-placeholder'
import Paragraph from '@tiptap/extension-paragraph'
import { TableKit } from '@tiptap/extension-table'
import { Color, TextStyle } from '@tiptap/extension-text-style'
import StarterKit from '@tiptap/starter-kit'
import { EditorContent, useEditor } from '@tiptap/vue-3'
import { BubbleMenu } from '@tiptap/vue-3/menus'
import {
  BoldOutlined,
  CheckOutlined,
  CodeOutlined,
  DeleteOutlined,
  DownOutlined,
  ItalicOutlined,
  LinkOutlined,
  OrderedListOutlined,
  StrikethroughOutlined,
  UnderlineOutlined,
  UnorderedListOutlined,
} from '@ant-design/icons-vue'
import { message } from 'ant-design-vue'
import { useAppI18n } from '@/i18n'
import markdownHighlightBlockIcon from '@/assets/icons/markdown-highlight-block.svg'
import markdownQuoteIcon from '@/assets/icons/markdown-quote.svg'
import markdownTaskListIcon from '@/assets/icons/markdown-task-list.svg'

const props = withDefaults(
  defineProps<{
    modelValue?: string
    disabled?: boolean
    placeholder?: string
    titlePlaceholder?: string
    ensureTitleHeading?: boolean
    variant?: 'document' | 'compact'
    onUploadImages?: (files: File[]) => Promise<string[]>
    /** 仅解析图片展示地址，原始引用仍用于 Markdown 保存。 */
    resolveImageSrc?: (src: string) => Promise<string>
    customColors?: boolean
    defaultTextColor?: string
  }>(),
  {
    modelValue: '',
    disabled: false,
    placeholder: '',
    titlePlaceholder: '',
    ensureTitleHeading: false,
    variant: 'document',
    onUploadImages: undefined,
    customColors: false,
    defaultTextColor: '#262626',
  },
)

const emit = defineEmits<{
  'update:modelValue': [value: string]
  ready: []
}>()

const { t } = useAppI18n()
const HEADING_LEVELS = [1, 2, 3, 4, 5, 6] as const
const TEXT_COLOR_ICON = 'A'
const HEADING_SCROLL_TOP_GAP = 24

type FloatingToolKey = 'bold' | 'italic' | 'underline' | 'strike' | 'link' | 'inlineCode'
type TextColorKey = 'neutral' | 'success' | 'warning' | 'error' | 'primary'
type BlockTypeKey =
  | 'paragraph'
  | 'heading1'
  | 'heading2'
  | 'heading3'
  | 'heading4'
  | 'heading5'
  | 'heading6'
  | 'bulletList'
  | 'orderedList'
  | 'taskList'
  | 'highlightBlock'
  | 'blockquote'
  | 'codeBlock'

type TextColorItem = {
  key: TextColorKey
  value: string
  label: string
}

type BlockTypeItem = {
  key: BlockTypeKey
  label: string
  shortLabel: string
  icon?: Component
  iconSrc?: string
}

const FLOATING_TOOLS: Array<{ key: FloatingToolKey; label: string; icon: Component }> = [
  { key: 'bold', label: 'components.markdown.bold', icon: BoldOutlined },
  { key: 'italic', label: 'components.markdown.italic', icon: ItalicOutlined },
  { key: 'underline', label: 'components.markdown.underline', icon: UnderlineOutlined },
  { key: 'strike', label: 'components.markdown.strike', icon: StrikethroughOutlined },
  { key: 'link', label: 'components.markdown.link', icon: LinkOutlined },
  { key: 'inlineCode', label: 'components.markdown.inlineCode', icon: CodeOutlined },
]

const TEXT_COLOR_ITEMS: TextColorItem[] = [
  { key: 'neutral', value: '#262626', label: 'components.markdown.textColorNeutral' },
  { key: 'success', value: '#16a34a', label: 'components.markdown.textColorGreen' },
  { key: 'warning', value: '#ed744a', label: 'components.markdown.textColorOrange' },
  { key: 'error', value: '#fb363f', label: 'components.markdown.textColorRed' },
  { key: 'primary', value: '#3157e2', label: 'components.markdown.textColorBlue' },
]

const BLOCK_TYPE_ITEMS: BlockTypeItem[] = [
  { key: 'paragraph', label: 'components.markdown.text', shortLabel: 'T' },
  { key: 'heading1', label: 'components.markdown.heading1', shortLabel: 'H₁' },
  { key: 'heading2', label: 'components.markdown.heading2', shortLabel: 'H₂' },
  { key: 'heading3', label: 'components.markdown.heading3', shortLabel: 'H₃' },
  { key: 'heading4', label: 'components.markdown.heading4', shortLabel: 'H₄' },
  { key: 'heading5', label: 'components.markdown.heading5', shortLabel: 'H₅' },
  { key: 'heading6', label: 'components.markdown.heading6', shortLabel: 'H₆' },
  {
    key: 'bulletList',
    label: 'components.markdown.unorderedList',
    shortLabel: '•',
    icon: UnorderedListOutlined,
  },
  {
    key: 'orderedList',
    label: 'components.markdown.orderedList',
    shortLabel: '1.',
    icon: OrderedListOutlined,
  },
  {
    key: 'taskList',
    label: 'components.markdown.checkList',
    shortLabel: '☑',
    iconSrc: markdownTaskListIcon,
  },
  {
    key: 'highlightBlock',
    label: 'components.markdown.highlightBlock',
    shortLabel: '▰',
    iconSrc: markdownHighlightBlockIcon,
  },
  {
    key: 'blockquote',
    label: 'components.markdown.quote',
    shortLabel: '“',
    iconSrc: markdownQuoteIcon,
  },
  { key: 'codeBlock', label: 'components.markdown.codeBlock', shortLabel: '</>' },
]

const linkEditorOpen = ref(false)
const typeMenuOpen = ref(false)
const colorMenuOpen = ref(false)
const rootRef = ref<HTMLElement>()
const linkUrl = ref('')
const linkInputRef = ref<HTMLInputElement>()
const editorRevision = ref(0)
let applyingExternalValue = false

const bubbleMenuOptions = {
  placement: 'top' as const,
  offset: 8,
  onHide() {
    typeMenuOpen.value = false
  },
  get shift() {
    return {
      boundary: rootRef.value,
      crossAxis: true,
      padding: 8,
    }
  },
}

const MarkdownTextStyle = TextStyle.extend({
  renderMarkdown: (node, helpers) => {
    const content = helpers.renderChildren(node.content || [])
    const color = typeof node.attrs?.color === 'string' ? node.attrs.color.toLowerCase() : ''
    if (!/^#[0-9a-f]{6}$/i.test(color)) return content
    return `<span style="color: ${color}" data-goteams-color="${color}">${content}</span>`
  },
})

const MarkdownImage = Node.create({
  name: 'image',
  inline: true,
  group: 'inline',
  draggable: true,
  atom: true,

  addAttributes() {
    return {
      src: {
        default: null,
        parseHTML: (element) => element.getAttribute('data-goteams-image-src') || element.getAttribute('src'),
      },
      alt: { default: null },
      title: { default: null },
    }
  },

  parseHTML() {
    return [{ tag: 'img[src]' }]
  },

  renderHTML({ HTMLAttributes }) {
    return ['img', mergeAttributes(HTMLAttributes)]
  },

  addNodeView() {
    const resolveImageSrc = props.resolveImageSrc
    if (!resolveImageSrc) return null
    return ({ node }) => {
      const image = document.createElement('img')
      let currentNode = node
      let version = 0
      let destroyed = false
      image.onerror = () => {
        image.title = t('components.markdown.attachmentLoadFailed')
      }
      const updateImage = () => {
        const src = String(currentNode.attrs.src || '')
        const requestVersion = ++version
        image.alt = String(currentNode.attrs.alt || '')
        image.title = String(currentNode.attrs.title || '')
        image.setAttribute('data-goteams-image-src', src)
        image.removeAttribute('src')
        // NodeView 不修改节点属性，避免签名刷新或本地图片加载触发脏状态、写入临时地址。
        void resolveImageSrc(src).then((resolved) => {
          if (!destroyed && requestVersion === version) image.src = resolved
        }).catch(() => {
          if (!destroyed && requestVersion === version) {
            image.title = t('components.markdown.attachmentLoadFailed')
          }
        })
      }
      updateImage()
      return {
        dom: image,
        update(nextNode) {
          if (nextNode.type !== currentNode.type) return false
          const previous = currentNode.attrs
          currentNode = nextNode
          if (previous.src !== nextNode.attrs.src || previous.alt !== nextNode.attrs.alt || previous.title !== nextNode.attrs.title) {
            updateImage()
          }
          return true
        },
        destroy() {
          destroyed = true
          version += 1
          image.onerror = null
        },
      }
    }
  },

  markdownTokenName: 'image',

  parseMarkdown(token, helpers) {
    return helpers.createNode('image', {
      src: token.href || '',
      alt: token.text || null,
      title: token.title || null,
    })
  },

  renderMarkdown(node) {
    const src = String(node.attrs?.src || '')
    const alt = String(node.attrs?.alt || '')
      .replace(/\\/g, '\\\\')
      .replace(/\[/g, '\\[')
      .replace(/\]/g, '\\]')
    const title = String(node.attrs?.title || '').replace(/"/g, '\\"')
    return title ? `![${alt}](${src} "${title}")` : `![${alt}](${src})`
  },
})

const MarkdownParagraph = Paragraph.extend({
  parseMarkdown(token, helpers) {
    const tokens = token.tokens || []
    // 默认段落解析把独占一行的图片提升为块节点；项目图片是行内节点，必须保留段落。
    if (tokens.length === 1 && tokens[0].type === 'image') {
      return helpers.createNode('paragraph', undefined, helpers.parseInline(tokens))
    }
    return this.parent?.(token, helpers) ?? helpers.createNode('paragraph', undefined, helpers.parseInline(tokens))
  },
})

function ensureRequiredTitleHeading(instance: TiptapEditor) {
  if (!props.ensureTitleHeading || props.disabled) return false
  const firstNode = instance.state.doc.firstChild
  if (firstNode?.type.name === 'heading' && firstNode.attrs.level === 1) return false
  return instance.commands.insertContentAt(0, {
    type: 'heading',
    attrs: { level: 1 },
  })
}

const editor = useEditor({
  editable: !props.disabled,
  content: props.modelValue || '',
  contentType: 'markdown',
  extensions: [
    StarterKit.configure({
      paragraph: false,
      heading: { levels: [...HEADING_LEVELS] },
      link: { openOnClick: false },
    }),
    MarkdownParagraph,
    TaskList,
    TaskItem.configure({ nested: true }),
    TableKit.configure({ table: { resizable: false } }),
    Highlight,
    MarkdownImage,
    MarkdownTextStyle,
    Color,
    Placeholder.configure({
      includeChildren: true,
      showOnlyCurrent: false,
      placeholder: ({ node }) =>
        node.type.name === 'heading' && node.attrs.level === 1 && props.titlePlaceholder
          ? props.titlePlaceholder
          : props.placeholder,
    }),
    Markdown.configure({
      markedOptions: { gfm: true, breaks: false },
    }),
  ],
  editorProps: {
    handlePaste(_view, event) {
      const files = Array.from(event.clipboardData?.files || []).filter((file) =>
        file.type.startsWith('image/'),
      )
      if (!files.length || !props.onUploadImages) return false
      event.preventDefault()
      const selection = editor.value?.state.selection
      if (selection) void insertUploadedImages(files, selection.from, selection.to)
      return true
    },
    handleDrop(view, event, _slice, moved) {
      if (moved) return false
      const files = Array.from(event.dataTransfer?.files || []).filter((file) =>
        file.type.startsWith('image/'),
      )
      if (!files.length || !props.onUploadImages) return false
      const coordinates = view.posAtCoords({ left: event.clientX, top: event.clientY })
      if (!coordinates) return false
      event.preventDefault()
      void insertUploadedImages(files, coordinates.pos, coordinates.pos)
      return true
    },
  },
  onCreate({ editor: instance }) {
    ensureRequiredTitleHeading(instance)
    editorRevision.value += 1
    emit('ready')
  },
  onSelectionUpdate() {
    editorRevision.value += 1
  },
  onUpdate({ editor: instance }) {
    editorRevision.value += 1
    if (applyingExternalValue || ensureRequiredTitleHeading(instance)) return
    emit('update:modelValue', instance.getMarkdown())
  },
})

async function insertUploadedImages(files: File[], from: number, to: number) {
  if (!props.onUploadImages) return
  try {
    const markdown = (await props.onUploadImages(files)).filter(Boolean).join('\n')
    const instance = editor.value
    if (!instance || !markdown) return
    const maxPosition = instance.state.doc.content.size
    instance.commands.insertContentAt(
      {
        from: Math.min(from, maxPosition),
        to: Math.min(to, maxPosition),
      },
      markdown,
      { contentType: 'markdown', updateSelection: true },
    )
    instance.commands.focus()
  } catch (error) {
    message.error(error instanceof Error ? error.message : String(error))
  }
}

const currentBlockType = computed<BlockTypeKey>(() => {
  void editorRevision.value
  const instance = editor.value
  if (!instance) return 'paragraph'
  for (const level of HEADING_LEVELS) {
    if (instance.isActive('heading', { level })) return `heading${level}`
  }
  if (instance.isActive('taskList')) return 'taskList'
  if (instance.isActive('bulletList')) return 'bulletList'
  if (instance.isActive('orderedList')) return 'orderedList'
  if (instance.isActive('blockquote')) return 'blockquote'
  if (instance.isActive('codeBlock')) return 'codeBlock'
  return 'paragraph'
})

const currentBlockTypeItem = computed(
  () => BLOCK_TYPE_ITEMS.find((item) => item.key === currentBlockType.value) || BLOCK_TYPE_ITEMS[0],
)

const selectedTextColor = computed(() => {
  void editorRevision.value
  const color = editor.value?.getAttributes('textStyle').color
  return typeof color === 'string' && /^#[0-9a-f]{6}$/i.test(color) ? color : props.defaultTextColor
})

function shouldShowBubbleMenu({
  editor: instance,
  from,
  to,
}: {
  editor: TiptapEditor
  from: number
  to: number
}) {
  return instance.isEditable && from !== to
}

function toggleTypeMenu() {
  typeMenuOpen.value = !typeMenuOpen.value
  colorMenuOpen.value = false
  linkEditorOpen.value = false
}

const customColor = ref('#262626')
const colorSelection = ref<{ from: number; to: number }>()

function toggleColorMenu() {
  const selection = editor.value?.state.selection
  if (selection) colorSelection.value = { from: selection.from, to: selection.to }
  customColor.value = selectedTextColor.value
  colorMenuOpen.value = !colorMenuOpen.value
  typeMenuOpen.value = false
  linkEditorOpen.value = false
}

function applyBlockType(value: BlockTypeKey) {
  const instance = editor.value
  if (!instance) return
  const chain = instance.chain().focus()
  if (value === 'paragraph') chain.setParagraph().run()
  else if (value.startsWith('heading')) {
    const level = Number(value.slice(7)) as (typeof HEADING_LEVELS)[number]
    chain.setHeading({ level }).run()
  } else if (value === 'bulletList') chain.toggleBulletList().run()
  else if (value === 'orderedList') chain.toggleOrderedList().run()
  else if (value === 'taskList') chain.toggleTaskList().run()
  else if (value === 'highlightBlock') chain.toggleHighlight().run()
  else if (value === 'blockquote') chain.toggleBlockquote().run()
  else if (value === 'codeBlock') chain.toggleCodeBlock().run()
  typeMenuOpen.value = false
}

function isFloatingToolActive(key: FloatingToolKey) {
  void editorRevision.value
  const instance = editor.value
  if (!instance) return false
  if (key === 'inlineCode') return instance.isActive('code')
  return instance.isActive(key)
}

function applyFloatingTool(key: FloatingToolKey) {
  const instance = editor.value
  if (!instance) return
  typeMenuOpen.value = false
  colorMenuOpen.value = false
  if (key === 'link') {
    toggleLinkEditor()
    return
  }
  linkEditorOpen.value = false
  const chain = instance.chain().focus()
  if (key === 'bold') chain.toggleBold().run()
  else if (key === 'italic') chain.toggleItalic().run()
  else if (key === 'underline') chain.toggleUnderline().run()
  else if (key === 'strike') chain.toggleStrike().run()
  else chain.toggleCode().run()
}

function applyTextColor(item: TextColorItem) {
  editor.value?.chain().focus().setColor(item.value).run()
  colorMenuOpen.value = false
}

function updateCustomColor(event: Event) {
  if (!(event.target instanceof HTMLInputElement)) return
  customColor.value = event.target.value
  applyCustomColor()
}

function applyCustomColor(reset = false) {
  const instance = editor.value
  if (!instance || (!reset && !/^#[0-9a-f]{6}$/i.test(customColor.value))) return
  const chain = instance.chain()
  if (colorSelection.value) chain.setTextSelection(colorSelection.value)
  if (reset) chain.focus().unsetColor().run()
  else chain.setColor(customColor.value.toLowerCase()).run()
  if (reset) colorMenuOpen.value = false
}

function toggleLinkEditor() {
  const instance = editor.value
  if (!instance) return
  linkUrl.value = instance.getAttributes('link').href || ''
  linkEditorOpen.value = !linkEditorOpen.value
  typeMenuOpen.value = false
  colorMenuOpen.value = false
  if (linkEditorOpen.value) void nextTick(() => linkInputRef.value?.focus())
}

function closeLinkEditor() {
  linkEditorOpen.value = false
  editor.value?.commands.focus()
}

function applyLink() {
  const instance = editor.value
  const url = linkUrl.value.trim()
  if (!instance || !url) return
  if (/^(?:javascript|data|vbscript):/i.test(url)) {
    message.error(t('components.markdown.linkUrlInvalid'))
    return
  }
  instance.chain().focus().extendMarkRange('link').setLink({ href: url }).run()
  linkEditorOpen.value = false
}

function removeLink() {
  editor.value?.chain().focus().extendMarkRange('link').unsetLink().run()
  linkEditorOpen.value = false
}

watch(
  () => props.modelValue,
  (value) => {
    const instance = editor.value
    if (!instance || value === instance.getMarkdown()) return
    applyingExternalValue = true
    instance.commands.setContent(value || '', { contentType: 'markdown', emitUpdate: false })
    ensureRequiredTitleHeading(instance)
    applyingExternalValue = false
    editorRevision.value += 1
  },
)

watch(
  () => props.disabled,
  (disabled) => editor.value?.setEditable(!disabled),
)

onBeforeUnmount(() => editor.value?.destroy())

defineExpose({
  getValue() {
    return editor.value?.getMarkdown() ?? props.modelValue
  },
  focus() {
    editor.value?.commands.focus()
  },
  htmlToMarkdown(value: string) {
    const instance = editor.value
    if (!instance) return value
    const currentMarkdown = instance.getMarkdown()
    applyingExternalValue = true
    try {
      instance.commands.setContent(value || '', { contentType: 'html', emitUpdate: false })
      return instance.getMarkdown()
    } finally {
      instance.commands.setContent(currentMarkdown, { contentType: 'markdown', emitUpdate: false })
      applyingExternalValue = false
      editorRevision.value += 1
    }
  },
  scrollToHeading(index: number) {
    if (index < 0) return
    const container = rootRef.value
    const heading =
      editor.value?.view.dom.querySelectorAll<HTMLElement>('h1, h2, h3, h4, h5, h6')[index]
    if (!container || !heading) return

    const containerTop = container.getBoundingClientRect().top
    const headingTop = heading.getBoundingClientRect().top
    const targetTop = container.scrollTop + headingTop - containerTop - HEADING_SCROLL_TOP_GAP
    container.scrollTo({ top: Math.max(0, targetTop), behavior: 'smooth' })
  },
})
</script>

<style scoped>
.text-color-menu.has-custom-colors { flex-wrap: wrap; width: 192px; }
.custom-color-controls { display: flex; align-items: center; gap: 8px; width: 100%; font-size: 12px; }
.custom-color-picker { flex-shrink: 0; width: 32px; height: 26px; padding: 0; border: 0; background: transparent; cursor: pointer; }
.custom-color-hex { flex: 1; min-width: 0; width: 82px; padding: 4px; border: 1px solid #d9d9d9; border-radius: 4px; }
.custom-color-controls button { flex-shrink: 0; min-height: 24px; padding: 2px 8px; border: 1px solid #d9d9d9; border-radius: 4px; color: #262626; background: #fff; cursor: pointer; }

.tiptap-markdown-editor {
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  background: #fff;
}

.tiptap-editor-content {
  min-height: 100%;
}

.tiptap-markdown-editor :deep(.ProseMirror) {
  min-height: 100%;
  padding: 24px 48px 64px;
  color: #262626;
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
  font-size: 16px;
  line-height: 1.9;
  outline: none;
}

/* Windows 原生文本指针可能在白色编辑区变白，使用带描边的静态指针保持可见。 */
body.goteams-windows-desktop .tiptap-markdown-editor:not(.is-disabled) :deep(.ProseMirror) {
  cursor: url('@/assets/icons/markdown-text-cursor.svg') 12 12, text;
}

.tiptap-markdown-editor :deep(.ProseMirror h1),
.tiptap-markdown-editor :deep(.ProseMirror h2),
.tiptap-markdown-editor :deep(.ProseMirror h3),
.tiptap-markdown-editor :deep(.ProseMirror h4),
.tiptap-markdown-editor :deep(.ProseMirror h5),
.tiptap-markdown-editor :deep(.ProseMirror h6) {
  border: 0;
}

.tiptap-markdown-editor :deep(.ProseMirror h1) {
  margin: 0 0 16px;
  color: #1d1d1f;
  font-size: 32px;
  font-weight: 600;
  line-height: 42px;
  letter-spacing: -0.6px;
}

.tiptap-markdown-editor :deep(.ProseMirror h2) {
  margin: 28px 0 12px;
  color: #202329;
  font-size: 26px;
  font-weight: 700;
  line-height: 36px;
}

.tiptap-markdown-editor :deep(.ProseMirror h3) {
  margin: 18px 0 0;
  color: #202329;
  font-size: 22px;
  font-weight: 700;
  line-height: 32px;
}

.tiptap-markdown-editor :deep(.ProseMirror h4),
.tiptap-markdown-editor :deep(.ProseMirror h5),
.tiptap-markdown-editor :deep(.ProseMirror h6) {
  margin: 18px 0 0;
  color: #202329;
  font-weight: 600;
  line-height: 1.5;
}

.tiptap-markdown-editor :deep(.ProseMirror p),
.tiptap-markdown-editor :deep(.ProseMirror ul),
.tiptap-markdown-editor :deep(.ProseMirror ol),
.tiptap-markdown-editor :deep(.ProseMirror blockquote) {
  margin: 0;
  padding-top: 14px;
  font-size: 16px;
  line-height: 30px;
}

.tiptap-markdown-editor :deep(.ProseMirror p.is-editor-empty:first-child::before),
.tiptap-markdown-editor :deep(.ProseMirror .is-empty::before) {
  float: left;
  height: 0;
  color: #bfbfbf;
  content: attr(data-placeholder);
  pointer-events: none;
}

.tiptap-markdown-editor :deep(.ProseMirror code) {
  padding: 2px 6px;
  border-radius: 4px;
  color: #49505a;
  background: #f1f3f6;
  font-family: SFMono-Regular, Consolas, 'Liberation Mono', monospace;
  font-size: 12px;
  font-weight: 600;
}

.tiptap-markdown-editor :deep(.ProseMirror pre) {
  overflow-x: auto;
  margin: 18px 0 0;
  padding: 16px;
  border-radius: 8px;
  background: #1f2329;
}

.tiptap-markdown-editor :deep(.ProseMirror pre code) {
  padding: 0;
  color: #f5f5f5;
  background: transparent;
  font-size: 13px;
  font-weight: 400;
}

.tiptap-markdown-editor :deep(.ProseMirror blockquote) {
  margin-top: 14px;
  padding-left: 16px;
  border-left: 3px solid #d9d9d9;
  color: #6b7280;
}

.tiptap-markdown-editor :deep(.ProseMirror mark) {
  padding: 1px 3px;
  border-radius: 3px;
  background: #fff3a3;
}

.tiptap-markdown-editor :deep(.ProseMirror ul[data-type='taskList']) {
  padding-left: 0;
  list-style: none;
}

.tiptap-markdown-editor :deep(.ProseMirror ul[data-type='taskList'] li) {
  display: flex;
  align-items: flex-start;
  gap: 8px;
}

.tiptap-markdown-editor :deep(.ProseMirror ul[data-type='taskList'] li > div) {
  flex: 1;
}

.tiptap-markdown-editor :deep(.ProseMirror table) {
  width: 100%;
  margin: 20px 0 0;
  border: 1px solid rgba(28, 29, 32, 0.09);
  border-spacing: 0;
  border-collapse: separate;
  border-radius: 13px;
  color: #262626;
  font-size: 12px;
  line-height: 20px;
}

.tiptap-markdown-editor :deep(.ProseMirror th),
.tiptap-markdown-editor :deep(.ProseMirror td) {
  padding: 13px 14px;
  border-right: 1px solid rgba(28, 29, 32, 0.07);
  border-bottom: 1px solid rgba(28, 29, 32, 0.07);
}

.tiptap-markdown-editor :deep(.ProseMirror th) {
  color: #555961;
  background: #f7f8fa;
  font-weight: 650;
}

.tiptap-markdown-editor :deep(.ProseMirror img) {
  display: block;
  max-width: 100%;
  height: auto;
  margin-top: 14px;
  border-radius: 8px;
}

.tiptap-markdown-editor.is-compact :deep(.ProseMirror) {
  padding: 0 4px 24px 0;
  font-size: 14px;
  line-height: 28px;
}

.tiptap-markdown-editor.is-compact :deep(.ProseMirror h1) {
  margin-bottom: 10px;
  font-size: 24px;
  line-height: 34px;
}

.tiptap-markdown-editor.is-compact :deep(.ProseMirror h2) {
  margin-top: 18px;
  font-size: 20px;
  line-height: 30px;
}

.tiptap-markdown-editor.is-compact :deep(.ProseMirror h3) {
  margin-top: 14px;
  font-size: 18px;
  line-height: 28px;
}

.tiptap-markdown-editor.is-compact :deep(.ProseMirror p),
.tiptap-markdown-editor.is-compact :deep(.ProseMirror ul),
.tiptap-markdown-editor.is-compact :deep(.ProseMirror ol),
.tiptap-markdown-editor.is-compact :deep(.ProseMirror blockquote) {
  padding-top: 8px;
  font-size: 14px;
  line-height: 28px;
}

.is-disabled :deep(.ProseMirror) {
  cursor: default;
}

.floating-toolbar {
  position: relative;
  z-index: 20;
  display: flex;
  max-width: calc(100vw - 32px);
  align-items: center;
  gap: 2px;
  padding: 4px 6px;
  border: 1px solid #f0f0f0;
  border-radius: 12px;
  background: #fff;
  box-shadow:
    0 6px 30px 5px rgba(0, 0, 0, 0.05),
    0 16px 24px 2px rgba(0, 0, 0, 0.04),
    0 8px 10px -5px rgba(0, 0, 0, 0.08);
}

.floating-tool,
.link-confirm,
.link-remove {
  display: inline-flex;
  width: 28px;
  height: 28px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 8px;
  color: #595959;
  background: transparent;
  cursor: pointer;
  font-size: 15px;
  transition:
    color 0.18s ease,
    background 0.18s ease;
}

.floating-menu-anchor {
  position: relative;
  display: flex;
}

.floating-type-trigger {
  width: auto;
  min-width: 42px;
  gap: 4px;
  padding: 0 7px;
  font-size: 14px;
}

.floating-type-label {
  display: inline-flex;
  min-width: 16px;
  align-items: center;
  justify-content: center;
}

.floating-type-chevron {
  color: #8c8c8c;
  font-size: 8px;
}

.floating-color-trigger {
  width: 42px;
  gap: 4px;
}

.floating-color-letter {
  font-size: 15px;
  font-weight: 500;
  line-height: 1;
}

.floating-tool:hover,
.floating-tool.active {
  color: #3157e2;
  background: #f0f1f3;
}

.floating-tool:focus-visible,
.link-confirm:focus-visible,
.link-remove:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.floating-popover {
  position: absolute;
  z-index: 24;
  padding: 10px;
  border: 1px solid rgba(28, 29, 32, 0.1);
  border-radius: 11px;
  background: rgba(255, 255, 255, 0.98);
  box-shadow:
    0 12px 30px rgba(15, 23, 42, 0.14),
    0 2px 6px rgba(15, 23, 42, 0.07);
}

.block-type-menu {
  top: 36px;
  left: 0;
  display: flex;
  width: 130px;
  max-height: min(406px, calc(100vh - 80px));
  flex-direction: column;
  gap: 2px;
  overflow-y: auto;
}

.block-type-menu-item {
  display: flex;
  width: 100%;
  height: 30px;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
  padding: 0 7px;
  border: 0;
  border-radius: 7px;
  color: #262626;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  line-height: 22px;
  text-align: left;
}

.block-type-menu-item:hover,
.block-type-menu-item:focus-visible,
.block-type-menu-item.is-selected {
  outline: none;
  background: #f5f5f5;
}

.block-type-menu-item.is-selected {
  color: #3157e2;
}

.block-type-menu-icon {
  display: inline-flex;
  width: 22px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  color: #595959;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  font-size: 13px;
  text-align: center;
}

.block-type-icon {
  color: #262626;
  font-size: 16px;
}

.block-type-image-icon {
  display: block;
  width: 16px;
  height: 16px;
  flex: 0 0 auto;
}

.text-color-menu {
  top: 36px;
  right: -4px;
  display: flex;
  gap: 6px;
  padding: 8px;
}

.text-color-item {
  display: inline-flex;
  width: 30px;
  height: 30px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 1px solid transparent;
  border-radius: 7px;
  background: transparent;
  cursor: pointer;
}

.text-color-item:hover,
.text-color-item:focus-visible {
  border-color: #d9d9d9;
  outline: none;
  background: #f5f6f8;
}

.text-color-item.is-selected {
  border-color: #3157e2;
  box-shadow: 0 0 0 2px rgba(49, 87, 226, 0.08);
}

.text-color-swatch {
  display: inline-flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  border-radius: 5px;
  font-size: 15px;
  font-weight: 500;
  line-height: 1;
}

.text-color-swatch.is-neutral {
  color: #262626;
  background: #f5f5f5;
}

.text-color-swatch.is-success {
  color: #16a34a;
  background: #f0fdf4;
}

.text-color-swatch.is-warning {
  color: #ed744a;
  background: #fff5e5;
}

.text-color-swatch.is-error {
  color: #fb363f;
  background: #fef2f2;
}

.text-color-swatch.is-primary {
  color: #3157e2;
  background: #e5efff;
}

.link-editor {
  top: 36px;
  left: 44px;
  display: flex;
  width: 264px;
  align-items: center;
  gap: 6px;
  padding: 8px;
}

.link-editor input {
  min-width: 0;
  flex: 1;
  height: 30px;
  padding: 0 10px;
  border: 1px solid #d9d9d9;
  border-radius: 7px;
  color: #262626;
  outline: none;
}

.link-editor input:focus {
  border-color: #3157e2;
  box-shadow: 0 0 0 2px rgba(49, 87, 226, 0.15);
}

.link-confirm {
  color: #3157e2;
}

.link-remove {
  color: #fb363f;
}

@media (max-width: 860px) {
  .tiptap-markdown-editor :deep(.ProseMirror) {
    padding-inline: 24px;
  }

  .block-type-menu {
    left: 0;
  }
}
</style>
