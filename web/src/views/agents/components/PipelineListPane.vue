<template>
  <section class="pipeline-pane">
    <header class="pane-header">
      <div
        class="pane-switch"
        :aria-label="t('agents.title')"
      >
        <button
          type="button"
		  class="pane-switch-item"
		  :class="{ active: mode === 'pipeline' }"
		  :aria-pressed="mode === 'pipeline'"
		  :disabled="navigationDisabled"
		  @click="changeMode('pipeline')"
        >
          <ApartmentOutlined />
          <span>{{ t('agents.pipeline') }}</span>
        </button>
          <button
            type="button"
            class="pane-switch-item"
			:class="{ active: mode === 'expert_group' }"
			:aria-pressed="mode === 'expert_group'"
			:disabled="navigationDisabled"
			@click="changeMode('expert_group')"
          >
            <TeamOutlined />
            <span>{{ t('agents.expertTeam') }}</span>
          </button>
      </div>
      <button
        type="button"
        class="create-button"
		:aria-label="mode === 'expert_group' ? t('expertGroups.newGroup') : t('agents.newPipeline')"
		:disabled="navigationDisabled"
		@click="createResource"
      >
        <PlusOutlined />
      </button>
    </header>
    <div class="pipeline-list">
      <article
		v-for="resource in resources"
		:key="resource.uuid"
        class="pipeline-card"
		:class="{ active: selectedUuid === resource.uuid }"
      >
        <div
          class="pipeline-main"
          role="button"
		  :tabindex="navigationDisabled ? -1 : 0"
		  :aria-pressed="selectedUuid === resource.uuid"
		  :aria-disabled="navigationDisabled"
		  @click="selectResource(resource.uuid)"
		  @keydown.enter="selectResource(resource.uuid)"
		  @keydown.space.prevent="selectResource(resource.uuid)"
        >
          <div class="pipeline-summary">
            <img
			  :src="resource.avatar || PIPELINE_AVATARS[0]"
              alt=""
            />
            <div class="pipeline-copy">
              <span class="pipeline-title">
				<strong>{{ resource.name }}</strong>
				<em :class="resourceLabelClass(resource)">
				  {{ resourceLabel(resource) }}
                </em>
				<a-tooltip v-if="resourceMissingConfig(resource)">
                  <template #title> <b>{{ t('agents.missingConfigTitle') }}</b><br />{{ t('agents.completeConfig') }} </template>
                  <span class="abnormal">
                    <ExclamationOutlined />
                  </span>
                </a-tooltip>
              </span>
			  <div class="pipeline-description">{{ resource.description || t('agents.noDescription') }}</div>
            </div>
          </div>
          <div class="avatar-chain">
            <a-tooltip
			  v-for="(step, index) in visibleMembers(resource)"
              :key="step.uuid"
              :title="step.name"
            >
              <span class="agent-avatar-item">
                <img
				  :src="memberAvatar(step, index)"
                  alt=""
                />
              </span>
            </a-tooltip>
            <a-tooltip
              v-if="hiddenMemberCount(resource)"
              :trigger="['hover', 'focus']"
            >
              <template #title>
                <div
                  v-for="(member, index) in hiddenMembers(resource)"
                  :key="member.uuid"
                  class="overflow-member"
                >
                  <img
                    :src="memberAvatar(member, visibleMemberLimit(resource) + index)"
                    alt=""
                  />
                  <span class="overflow-member-name">{{ member.name }}</span>
                </div>
              </template>
              <span class="avatar-overflow" tabindex="0">
                {{ `+${hiddenMemberCount(resource)}` }}
              </span>
            </a-tooltip>
          </div>
        </div>
        <a-dropdown
		  :open="activeMenuUuid === resource.uuid"
          :trigger="['click']"
		  @update:open="updateMenuOpen(resource.uuid, $event)"
        >
          <button
            type="button"
            class="action-btn pipeline-edit"
            :disabled="Boolean(copyingUuid) || navigationDisabled"
            :aria-label="t('agents.actions')"
            aria-haspopup="menu"
			:aria-expanded="activeMenuUuid === resource.uuid"
            @click.stop
          >
            <LoadingOutlined
			  v-if="copyingUuid === resource.uuid"
              spin
            />
            <img
              v-else
              class="action-btn-icon"
              :src="commonMoreActionsIcon"
              alt=""
              aria-hidden="true"
            />
          </button>
          <template #overlay>
			<a-menu @click="handleActionMenuClick($event, resource)">
              <a-menu-item
                key="copy"
                :disabled="Boolean(copyingUuid) || navigationDisabled"
              >
                <LoadingOutlined
				  v-if="copyingUuid === resource.uuid"
                  spin
                />
                <CopyOutlined v-else />
                {{ t('agents.copy') }}
              </a-menu-item>
              <a-menu-item
				v-if="canManage(resource)"
                key="edit"
                :disabled="navigationDisabled"
              >
                <EditOutlined /> {{ t('agents.edit') }}
              </a-menu-item>
              <a-menu-item
				v-if="canManage(resource)"
                key="delete"
                :disabled="navigationDisabled"
              >
                <DeleteOutlined /> {{ t('agents.delete') }}
              </a-menu-item>
            </a-menu>
          </template>
        </a-dropdown>
      </article>
      <a-empty
		v-if="!resources.length"
		:description="mode === 'expert_group' ? t('expertGroups.empty') : t('agents.emptyHint')"
      />
    </div>
  </section>
</template>

<script setup lang="ts">
import {
  ApartmentOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  ExclamationOutlined,
  LoadingOutlined,
  PlusOutlined,
  TeamOutlined,
} from '@ant-design/icons-vue'
import { computed, onDeactivated, ref } from 'vue'
import type { ExpertGroup, ExpertMember, Pipeline, PipelineStep } from '@/types/pipeline'
import commonMoreActionsIcon from '@/assets/icons/common-more-actions.svg'
import {
  isPipelineCloud,
  missingSteps,
	AGENT_AVATARS,
  PIPELINE_AVATARS,
  resolveAgentAvatar,
} from './agentPipeline'
import { useAppI18n } from '@/i18n'

const { t } = useAppI18n()

const props = withDefaults(defineProps<{
  pipelines: Pipeline[]
	expertGroups?: ExpertGroup[]
  selectedUuid: string
  copyingUuid?: string
	mode?: 'pipeline' | 'expert_group'
  navigationDisabled?: boolean
}>(), { expertGroups: () => [], mode: 'pipeline', navigationDisabled: false })

const emit = defineEmits<{
	copy: [resource: Pipeline | ExpertGroup]
  create: []
	delete: [resource: Pipeline | ExpertGroup]
	edit: [resource: Pipeline | ExpertGroup]
  select: [uuid: string]
	'change-mode': [mode: 'pipeline' | 'expert_group']
}>()

const MAX_VISIBLE_PIPELINE_STEPS = 6
const MAX_VISIBLE_EXPERT_MEMBERS = 7
const activeMenuUuid = ref('')
const resources = computed<Array<Pipeline | ExpertGroup>>(() =>
	props.mode === 'expert_group' ? props.expertGroups : props.pipelines,
)

function updateMenuOpen(uuid: string, open: boolean) {
  if (props.navigationDisabled && open) return
  activeMenuUuid.value = open ? uuid : ''
}

function handleActionMenuClick({ key }: { key: string | number }, resource: Pipeline | ExpertGroup) {
  if (props.navigationDisabled) return
  activeMenuUuid.value = ''
  if (key === 'copy') {
	  emit('copy', resource)
    return
  }
  if (key === 'edit') {
	  emit('edit', resource)
    return
  }
	if (key === 'delete') emit('delete', resource)
}

function isExpertResource(resource: Pipeline | ExpertGroup): resource is ExpertGroup {
	return 'ready' in resource
}

function isExpertCloud(resource: ExpertGroup): boolean {
	return resource.source_type === 'cloud'
}

function resourceMembers(resource: Pipeline | ExpertGroup): Array<PipelineStep | ExpertMember> {
	if (!isExpertResource(resource)) return resource.steps || []
	return [...(resource.leader ? [resource.leader] : []), ...resource.members]
}

function visibleMembers(resource: Pipeline | ExpertGroup) {
	return resourceMembers(resource).slice(0, visibleMemberLimit(resource))
}

function changeMode(mode: 'pipeline' | 'expert_group') {
  if (props.navigationDisabled) return
  emit('change-mode', mode)
}

function createResource() {
  if (props.navigationDisabled) return
  emit('create')
}

function selectResource(uuid: string) {
  if (props.navigationDisabled) return
  emit('select', uuid)
}

function hiddenMemberCount(resource: Pipeline | ExpertGroup) {
	return Math.max(resourceMembers(resource).length - visibleMemberLimit(resource), 0)
}

function hiddenMembers(resource: Pipeline | ExpertGroup) {
  return resourceMembers(resource).slice(visibleMemberLimit(resource))
}

function visibleMemberLimit(resource: Pipeline | ExpertGroup) {
	return isExpertResource(resource)
		? MAX_VISIBLE_EXPERT_MEMBERS
		: MAX_VISIBLE_PIPELINE_STEPS
}

function memberAvatar(member: PipelineStep | ExpertMember, index: number) {
	if ('expert_group_uuid' in member) {
		return member.avatar || AGENT_AVATARS[index % AGENT_AVATARS.length]
	}
	return resolveAgentAvatar(member as PipelineStep, index)
}

function resourceLabel(resource: Pipeline | ExpertGroup) {
	if (isExpertResource(resource)) return isExpertCloud(resource) ? t('agents.team') : t('agents.private')
	return isPipelineCloud(resource) ? t('agents.team') : t('agents.private')
}

function resourceLabelClass(resource: Pipeline | ExpertGroup) {
	if (isExpertResource(resource)) return isExpertCloud(resource) ? 'cloud' : 'local'
	return isPipelineCloud(resource) ? 'cloud' : 'local'
}

function resourceMissingConfig(resource: Pipeline | ExpertGroup) {
	return isExpertResource(resource) ? !resource.ready : missingSteps(resource).length > 0
}

function canManage(resource: Pipeline | ExpertGroup) {
	// 云端资源只读（团队），无论流水线还是专家团；本地资源可编辑/删除。
	return isExpertResource(resource) ? !isExpertCloud(resource) : !isPipelineCloud(resource)
}

onDeactivated(() => {
  activeMenuUuid.value = ''
})
</script>

<style scoped>
.pipeline-pane {
  display: flex;
  width: 348px;
  height: 100%;
  min-width: 0;
  min-height: 0;
  flex: 0 0 348px;
  flex-direction: column;
  overflow: hidden;
  border-right: 1px solid #d9d9d9;
  background: #fff;
}

.pane-header {
  display: flex;
  height: 60px;
  flex: 0 0 60px;
  align-items: center;
  justify-content: space-between;
  padding: 16px 24px 12px;
}

.pane-switch {
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 2px;
  border-radius: 32px;
  background: #edeff2;
}

.pane-switch-item {
  display: inline-flex;
  height: 28px;
  align-items: center;
  justify-content: center;
  gap: 4px;
  padding: 0 12px;
  border: 0;
  border-radius: 32px;
  color: #595959;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  line-height: 22px;
}

.pane-switch-item :deep(svg) {
  width: 16px;
  height: 16px;
}

.pane-switch-item.active {
  color: #262626;
  background: #fff;
  box-shadow: 0 1px 2px rgba(0, 0, 0, 0.08);
  font-weight: 600;
}

.create-button {
  display: inline-flex;
  width: 23.2071px;
  height: 24px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 6px;
  color: #3157e2;
  background: transparent;
  cursor: pointer;
  font-size: 14px;
  transition:
    color 0.18s,
    background-color 0.18s;
}

.create-button:hover:not(:disabled) {
  color: #2475fc;
  background: #f2f4f7;
}

.create-button:disabled,
.pane-switch-item:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.pipeline-list {
  display: flex;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  gap: 8px;
  overflow-y: auto;
  padding: 0 24px 24px;
}

.pipeline-card {
  position: relative;
  width: 100%;
  min-height: 145px;
  overflow: hidden;
  border: 0;
  border-radius: 12px;
  background: #fff;
  transition: background-color 0.18s;
}

.pipeline-card:hover {
  background: #f2f4f7;
}

.pipeline-card.active {
  background: #e5efff;
}

.pipeline-main {
  display: flex;
  width: 100%;
  min-height: 145px;
  box-sizing: border-box;
  flex-direction: column;
  justify-content: space-between;
  padding: 20px 12px;
  border: 0;
  border-radius: inherit;
  color: #595959;
  background: transparent;
  cursor: pointer;
  font: inherit;
  text-align: left;
}

.pipeline-main:focus-visible,
.action-btn:focus-visible,
.create-button:focus-visible,
.pane-switch-item:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.pipeline-summary {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
}

.pipeline-summary > img {
  width: 40px;
  height: 40px;
  flex: 0 0 40px;
  border-radius: 50%;
  object-fit: cover;
}

.pipeline-copy {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
}

.pipeline-title {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 6px;
  padding-right: 26px;
}

.pipeline-title strong {
  overflow: hidden;
  color: #1d1d1f;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pipeline-title em {
  display: inline-flex;
  height: 20px;
  flex: 0 0 auto;
  align-items: center;
  padding: 0 3px;
  border-radius: 6px;
  color: #3157e2;
  background: #e5efff;
  font-size: 12px;
  font-style: normal;
  line-height: 20px;
}

.pipeline-title em.cloud {
  color: #ed744a;
  background: #fff5e5;
}

.abnormal {
  display: flex;
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  color: #ef4444;
  background: #fef2f2;
  font-size: 9px;
}

.pipeline-description {
  display: -webkit-box;
  overflow: hidden;
  margin-top: 2px;
  color: #595959;
  font-size: 12px;
  line-height: 20px;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.avatar-chain { display: flex; min-height: 25px; flex-wrap: nowrap; align-items: center; gap: 8px; padding-top: 12px; }
.agent-avatar-item { position: relative; display: block; width: 24px; height: 24px; flex: 0 0 24px; opacity: .5; }
.agent-avatar-item > img:first-child { width: 24px; height: 24px; border: 1px solid #fff; border-radius: 50%; object-fit: cover; background: #fff; }
.pipeline-card.active .agent-avatar-item { opacity: 1; }
.pipeline-main[aria-disabled='true'] { cursor: not-allowed; }

.avatar-overflow {
  display: inline-flex;
  width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  flex: 0 0 24px;
  border: 0;
  border-radius: 50%;
  color: #fff;
  background: #8c8c8c;
  font-size: 12px;
  line-height: 1;
  white-space: nowrap;
  opacity: 0.55;
}

.avatar-overflow:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.overflow-member {
  display: flex;
  align-items: center;
  gap: 8px;
}

.overflow-member + .overflow-member {
  margin-top: 8px;
}

.overflow-member > img {
  width: 24px;
  height: 24px;
  flex: 0 0 24px;
  border-radius: 50%;
  object-fit: cover;
}

.overflow-member-name {
  min-width: 0;
  overflow-wrap: anywhere;
}

.action-btn {
  display: inline-flex;
  width: 24px;
  min-width: 24px;
  height: 24px;
  align-items: center;
  justify-content: center;
  padding: 0;
  border: 0;
  border-radius: 6px;
  background: transparent;
  cursor: pointer;
  opacity: 0;
  pointer-events: none;
  transition:
    opacity 0.18s,
    background-color 0.18s;
}

.action-btn-icon {
  display: block;
  width: 16px;
  height: 16px;
}

.pipeline-card:hover .action-btn,
.pipeline-card:focus-within .action-btn,
.action-btn[aria-expanded='true'] {
  opacity: 1;
  pointer-events: auto;
}

.action-btn:hover:not(:disabled) {
  background: #e4e6eb;
}

.action-btn:active:not(:disabled) {
  background: #d9dce2;
}

.action-btn:disabled {
  color: #cbd1da;
  cursor: not-allowed;
}

.pipeline-edit {
  position: absolute;
  z-index: 1;
  top: 20px;
  right: 12px;
}

@media (max-width: 900px) {
  .pipeline-pane {
    width: 100%;
    height: auto;
    min-height: 340px;
    flex: 0 0 340px;
    border-right: 0;
    border-bottom: 1px solid #d9d9d9;
  }
}

@media (prefers-reduced-motion: reduce) {
  .pipeline-card {
    transition: none;
  }

  .action-btn,
  .create-button,
  .pane-switch-item {
    transition: none;
  }
}
</style>
