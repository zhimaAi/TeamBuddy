import skillsEnUS from '@/locales/en-US/skills.json'
import skillsZhCN from '@/locales/zh-CN/skills.json'
import commonEnUS from '@/locales/en-US/common.json'
import agentsEnUS from '@/locales/en-US/agents.json'
import expertGroupsEnUS from '@/locales/en-US/expert-groups.json'
import apisEnUS from '@/locales/en-US/apis.json'
import commandsEnUS from '@/locales/en-US/commands.json'
import componentsEnUS from '@/locales/en-US/components.json'
import layoutEnUS from '@/locales/en-US/layout.json'
import knowledgeEnUS from '@/locales/en-US/knowledge.json'
import projectsEnUS from '@/locales/en-US/projects.json'
import remoteChannelsEnUS from '@/locales/en-US/remote-channels.json'
import routesEnUS from '@/locales/en-US/routes.json'
import settingsEnUS from '@/locales/en-US/settings.json'
import toolsEnUS from '@/locales/en-US/tools.json'
import teamworkEnUS from '@/locales/en-US/teamwork.json'
import workbenchEnUS from '@/locales/en-US/workbench.json'
import workflowBoardEnUS from '@/locales/en-US/workflows.board.json'
import workflowTaskEnUS from '@/locales/en-US/workflows.task.json'
import commonZhCN from '@/locales/zh-CN/common.json'
import agentsZhCN from '@/locales/zh-CN/agents.json'
import expertGroupsZhCN from '@/locales/zh-CN/expert-groups.json'
import apisZhCN from '@/locales/zh-CN/apis.json'
import commandsZhCN from '@/locales/zh-CN/commands.json'
import componentsZhCN from '@/locales/zh-CN/components.json'
import layoutZhCN from '@/locales/zh-CN/layout.json'
import knowledgeZhCN from '@/locales/zh-CN/knowledge.json'
import projectsZhCN from '@/locales/zh-CN/projects.json'
import remoteChannelsZhCN from '@/locales/zh-CN/remote-channels.json'
import routesZhCN from '@/locales/zh-CN/routes.json'
import settingsZhCN from '@/locales/zh-CN/settings.json'
import toolsZhCN from '@/locales/zh-CN/tools.json'
import teamworkZhCN from '@/locales/zh-CN/teamwork.json'
import workbenchZhCN from '@/locales/zh-CN/workbench.json'
import workflowBoardZhCN from '@/locales/zh-CN/workflows.board.json'
import workflowTaskZhCN from '@/locales/zh-CN/workflows.task.json'

export const zhCNMessages = {
  skills: skillsZhCN,
  agents: agentsZhCN,
  expertGroups: expertGroupsZhCN,
  apis: apisZhCN,
  commands: commandsZhCN,
  common: commonZhCN,
  components: componentsZhCN,
  layout: layoutZhCN,
  knowledge: knowledgeZhCN,
  projects: projectsZhCN,
  remoteChannels: remoteChannelsZhCN,
  routes: routesZhCN,
  settings: settingsZhCN,
  teamwork: teamworkZhCN,
  tools: toolsZhCN,
  workbench: workbenchZhCN,
  workflows: {
    board: workflowBoardZhCN,
    task: workflowTaskZhCN,
  },
}

export type MessageSchema = typeof zhCNMessages

export const enUSMessages = {
  skills: skillsEnUS,
  agents: agentsEnUS,
  expertGroups: expertGroupsEnUS,
  apis: apisEnUS,
  commands: commandsEnUS,
  common: commonEnUS,
  components: componentsEnUS,
  layout: layoutEnUS,
  knowledge: knowledgeEnUS,
  projects: projectsEnUS,
  remoteChannels: remoteChannelsEnUS,
  routes: routesEnUS,
  settings: settingsEnUS,
  teamwork: teamworkEnUS,
  tools: toolsEnUS,
  workbench: workbenchEnUS,
  workflows: {
    board: workflowBoardEnUS,
    task: workflowTaskEnUS,
  },
} satisfies MessageSchema

type MessageLeaf<T> = T extends string
  ? never
  : {
      [K in keyof T & string]: T[K] extends string ? K : `${K}.${MessageLeaf<T[K]>}`
    }[keyof T & string]

export type MessageKey = MessageLeaf<MessageSchema>
