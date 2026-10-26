<script setup lang="ts">
import { computed } from 'vue'
import darkApi from '@/assets/icons/sidebar-nav-dark-api.svg'
import darkCommand from '@/assets/icons/sidebar-nav-dark-command.svg'
import darkProject from '@/assets/icons/sidebar-nav-dark-project.svg'
import darkSettings from '@/assets/icons/sidebar-nav-dark-settings.svg'
import darkTaskCheck from '@/assets/icons/sidebar-nav-dark-task-check.svg'
import darkTaskFrame from '@/assets/icons/sidebar-nav-dark-task-frame.svg'
import lightAgent from '@/assets/icons/sidebar-nav-light-agent.svg'
import lightApi from '@/assets/icons/sidebar-nav-light-api.svg'
import lightCommand from '@/assets/icons/sidebar-nav-light-command.svg'
import lightDashboard from '@/assets/icons/sidebar-nav-light-dashboard.svg'
import lightKnowledge from '@/assets/icons/sidebar-nav-light-knowledge.svg'
import lightNewConversation from '@/assets/icons/sidebar-nav-light-new-conversation.svg'
import lightProject from '@/assets/icons/sidebar-nav-light-project.svg'
import lightSettings from '@/assets/icons/sidebar-nav-light-settings.svg'
import lightTaskCheck from '@/assets/icons/sidebar-nav-light-task-check.svg'
import lightTaskFrame from '@/assets/icons/sidebar-nav-light-task-frame.svg'
import navRemote from '@/assets/icons/sidebar-nav-remote.svg'
import navMore from '@/assets/icons/sidebar-nav-more.svg'
import conversationFolderOpen from '@/assets/icons/sidebar-conversation-folder-open.svg'
import conversationFolderClosed from '@/assets/icons/sidebar-conversation-folder-closed.svg'
import sidebarSearch from '@/assets/icons/sidebar-search.svg'
import workspaceAdd from '@/assets/icons/sidebar-workspace-add.svg'
import footerSettings from '@/assets/icons/sidebar-footer-settings.svg'
import conversationAdd from '@/assets/icons/sidebar-conversation-add.svg'
import conversationMore from '@/assets/icons/sidebar-conversation-more.svg'
import conversationChevron from '@/assets/icons/sidebar-conversation-chevron.svg'

type SidebarMenuIconName =
  | 'dashboard'
  | 'newConversation'
  | 'task'
  | 'agent'
  | 'project'
  | 'command'
  | 'book'
  | 'api'
  | 'settings'
  | 'remote'
  | 'more'
  | 'conversationFolderOpen'
  | 'conversationFolderClosed'
  | 'search'
  | 'workspaceAdd'
  | 'footerSettings'
  | 'conversationAdd'
  | 'conversationMore'
  | 'conversationChevron'

const props = defineProps<{
  name: SidebarMenuIconName
  dark?: boolean
}>()

const designSources: Partial<Record<SidebarMenuIconName, string>> = {
  dashboard: lightDashboard,
  newConversation: lightNewConversation,
  agent: lightAgent,
  book: lightKnowledge,
  remote: navRemote,
  more: navMore,
  conversationFolderOpen,
  conversationFolderClosed,
  search: sidebarSearch,
  workspaceAdd,
  footerSettings,
  conversationAdd,
  conversationMore,
  conversationChevron,
}

const iconSources: Partial<Record<SidebarMenuIconName, { light: string[]; dark: string[] }>> = {
  task: {
    light: [lightTaskFrame, lightTaskCheck],
    dark: [darkTaskFrame, darkTaskCheck],
  },
  project: { light: [lightProject], dark: [darkProject] },
  command: { light: [lightCommand], dark: [darkCommand] },
  api: { light: [lightApi], dark: [darkApi] },
  settings: { light: [lightSettings], dark: [darkSettings] },
}

const designSource = computed(() => designSources[props.name])
const designStyle = computed(() => ({ '--icon-source': `url("${designSource.value}")` }))
const sources = computed(() => iconSources[props.name]?.[props.dark ? 'dark' : 'light'] ?? [])
</script>

<template>
  <span class="sidebar-menu-icon" :class="props.name" aria-hidden="true">
    <span
      v-if="designSource"
      class="design-icon"
      :class="{ 'is-dark': props.dark }"
      :style="designStyle"
    >
      <img :src="designSource" alt="">
    </span>
    <img v-else v-for="source in sources" :key="source" :src="source" alt="">
  </span>
</template>

<style scoped>
.sidebar-menu-icon {
  position: relative;
  display: inline-block;
  width: 20px;
  height: 20px;
  flex: 0 0 20px;
}

.sidebar-menu-icon > img {
  position: absolute;
  display: block;
  top: 50%;
  left: 50%;
  max-width: none;
  transform: translate(-50%, -50%) scale(1.14);
  transform-origin: center;
}

.design-icon {
  position: absolute;
  display: inline-flex;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
}

.design-icon img {
  display: block;
  max-width: none;
}

.design-icon.is-dark {
  background-color: #94a3b8;
  -webkit-mask-image: var(--icon-source);
  -webkit-mask-position: center;
  -webkit-mask-repeat: no-repeat;
  mask-image: var(--icon-source);
  mask-position: center;
  mask-repeat: no-repeat;
}

.design-icon.is-dark img {
  visibility: hidden;
}

.conversationAdd,
.conversationMore,
.workspaceAdd,
.footerSettings {
  width: 16px;
  height: 16px;
  flex-basis: 16px;
}

.conversationChevron {
  width: 14px;
  height: 14px;
  flex-basis: 14px;
}

.task img:last-child {
  top: 58.33%;
  left: 50%;
}
</style>
