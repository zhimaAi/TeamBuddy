<template>
  <div class="expert-workspace">
    <section
      v-if="selectedGroup"
      class="expert-detail-pane"
    >
      <AgentDetailHeader :title="selectedGroup.name">
        <template #actions>
          <a-button
            v-if="allMembers.length"
            size="small"
            class="batch-config-button"
            :class="{ 'batch-cancel-button': batchMode }"
            :disabled="batchSaving"
            @click="toggleBatchMode"
          >
            <template #icon>
              <img
                :class="batchMode ? 'batch-cancel-icon' : 'batch-config-icon'"
                :src="batchMode ? batchCancelIcon : batchConfigIcon"
                alt=""
                aria-hidden="true"
              />
            </template>
            {{ batchMode ? t('agents.cancelBatch') : t('agents.batch') }}
          </a-button>
          <a-button
            v-if="batchMode"
            size="small"
            :disabled="batchSaving || selectedMemberUuids.length === allMembers.length"
            @click="selectAllMembers"
          >
            {{ t('agents.selectAll') }}
          </a-button>
          <AgentAddDropdown
            v-if="!batchMode"
            :label="t('expertGroups.addMemberCount', { count: selectedGroup.members.length })"
            @create="openMemberEditor('member')"
            @copy="openReusablePicker('member')"
          />
        </template>
      </AgentDetailHeader>

      <div
        class="detail-scroll"
        :class="{ 'detail-scroll--batch': batchMode }"
      >
        <section class="member-section member-section--leader">
          <MemberCard
            v-if="selectedGroup.leader"
            :member="selectedGroup.leader"
            :batch-mode="batchMode"
            :selected="selectedMemberUuids.includes(selectedGroup.leader.uuid)"
            @edit="openMemberEditor('leader', selectedGroup.leader)"
            @remove="removeMember(selectedGroup.leader)"
            @toggle="toggleMemberSelection(selectedGroup.leader.uuid)"
          />
          <div
            v-else
            class="member-empty"
          >
            <a-empty :description="t('expertGroups.leaderEmpty')" />
            <p>{{ t('expertGroups.leaderEmptyHint') }}</p>
            <div class="empty-actions">
              <a-button
                type="primary"
                ghost
                @click="openReusablePicker('leader')"
              >
                <template #icon><PlusOutlined /></template>
                {{ t('expertGroups.selectLeader') }}
              </a-button>
              <a-button @click="openMemberEditor('leader')">
                <template #icon><PlusOutlined /></template>
                {{ t('expertGroups.createLeader') }}
              </a-button>
            </div>
          </div>
        </section>

        <section class="member-section member-section--members">
          <MemberCard
            v-for="member in selectedGroup.members"
            :key="member.uuid"
            :member="member"
            :batch-mode="batchMode"
            :selected="selectedMemberUuids.includes(member.uuid)"
            @edit="openMemberEditor('member', member)"
            @remove="removeMember(member)"
            @toggle="toggleMemberSelection(member.uuid)"
          />
          <div
            v-if="!selectedGroup.members.length"
            class="member-empty"
          >
            <a-empty :description="t('expertGroups.membersEmpty')" />
            <p>{{ t('expertGroups.membersEmptyHint') }}</p>
            <div class="empty-actions">
              <a-button
                type="primary"
                ghost
                @click="openReusablePicker('member')"
              >
                <template #icon><PlusOutlined /></template>
                {{ t('expertGroups.selectMember') }}
              </a-button>
              <a-button @click="openMemberEditor('member')">
                <template #icon><PlusOutlined /></template>
                {{ t('expertGroups.createMember') }}
              </a-button>
            </div>
          </div>
        </section>
      </div>

      <AgentBatchConfigBar
        v-if="batchMode"
        :selected-count="selectedMemberUuids.length"
        :saving="batchSaving"
        @apply="applyBatchConfiguration"
        @cancel="clearBatchSelection"
      />
    </section>
    <section
      v-else
      class="expert-detail-empty"
    >
      <a-empty :description="t('expertGroups.selectHint')" />
    </section>

    <PipelineEditorModal
      v-model:open="groupEditorOpen"
      kind="expert_group"
      :expert-group="editingGroup"
      @expert-created="handleGroupSaved"
      @expert-updated="handleGroupSaved"
    />
    <AgentEditorModal
      v-model:open="memberEditorOpen"
      :expert-group-uuid="selectedUuid"
      :is-cloud-expert-group="selectedGroup?.source_type === 'cloud'"
      :expert-member="editingMember"
      :expert-role="memberRole"
      :current-avatar="editingMember?.avatar"
      @saved="reload(selectedUuid)"
    />
    <CopyAgentModal
      v-model:open="copyModalOpen"
      :target-expert-group-uuid="selectedUuid"
      :expert-role="memberRole"
      :source-steps="reusableSteps"
      :loading="reusableLoading"
      :multiple="memberRole === 'member'"
      @saved="reload(selectedUuid)"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { message, Modal } from 'ant-design-vue'
import { PlusOutlined } from '@ant-design/icons-vue'
import apiClient from '@/api/client'
import batchCancelIcon from '@/assets/icons/batch-cancel.svg'
import batchConfigIcon from '@/assets/icons/batch-config.svg'
import { useExpertGroupStore } from '@/stores/expert-group'
import type { ExpertGroup, ExpertMember, ReusableAgent } from '@/types/pipeline'
import { useAppI18n } from '@/i18n'
import { type ReusablePipelineStep } from './agentPipeline'
import AgentAddDropdown from './AgentAddDropdown.vue'
import AgentBatchConfigBar from './AgentBatchConfigBar.vue'
import AgentDetailHeader from './AgentDetailHeader.vue'
import AgentEditorModal from './AgentEditorModal.vue'
import CopyAgentModal from './CopyAgentModal.vue'
import MemberCard from './ExpertMemberCard.vue'
import PipelineEditorModal from './PipelineEditorModal.vue'

const { t } = useAppI18n()
const props = defineProps<{ selectedUuid: string }>()
const emit = defineEmits<{
  'update:selectedUuid': [uuid: string]
  'batch-saving-change': [saving: boolean]
}>()
const store = useExpertGroupStore()
const { items: groups } = storeToRefs(store)
const selectedUuid = computed({
  get: () => props.selectedUuid,
  set: (uuid: string) => emit('update:selectedUuid', uuid),
})
const selectedGroup = computed(() => groups.value.find((item) => item.uuid === selectedUuid.value))
const allMembers = computed<ExpertMember[]>(() => {
  if (!selectedGroup.value) return []
  return [
    ...(selectedGroup.value.leader ? [selectedGroup.value.leader] : []),
    ...selectedGroup.value.members,
  ]
})
const groupEditorOpen = ref(false)
const memberEditorOpen = ref(false)
const copyModalOpen = ref(false)
const editingGroup = ref<ExpertGroup>()
const editingMember = ref<ExpertMember>()
const memberRole = ref<'leader' | 'member'>('member')
const reusableAgents = ref<ReusableAgent[]>([])
const reusableLoading = ref(false)
const batchMode = ref(false)
const batchSaving = ref(false)
const selectedMemberUuids = ref<string[]>([])
const reusableSteps = computed<ReusablePipelineStep[]>(() =>
  reusableAgents.value.map((agent, index) => ({
    ...agent,
    pipelineName: agent.source_name,
    sort_order: index,
  })),
)

async function reload(preferred = selectedUuid.value) {
  try {
    await store.load(true)
    selectedUuid.value = groups.value.some((item) => item.uuid === preferred)
      ? preferred
      : groups.value[0]?.uuid || ''
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('expertGroups.loadFailed'))
  }
}

function openGroupEditor(group?: ExpertGroup) {
  editingGroup.value = group
  groupEditorOpen.value = true
}

function handleGroupSaved(group: ExpertGroup) {
  store.upsert(group)
  selectedUuid.value = group.uuid
}

async function handleGroupAction(key: string | number, group: ExpertGroup) {
  if (key === 'edit') return openGroupEditor(group)
  if (key === 'copy') {
    try {
      const item = await apiClient.post<ExpertGroup>(`/expert-groups/${group.uuid}/copy`, {})
      await reload(item.uuid)
      message.success(t('expertGroups.copied', { name: item.name }))
    } catch (error) {
      message.error(error instanceof Error ? error.message : t('expertGroups.copyFailed'))
    }
    return
  }
  Modal.confirm({
    title: t('expertGroups.deleteConfirm', { name: group.name }),
    okType: 'danger',
    onOk: async () => {
      try {
        await apiClient.delete(`/expert-groups/${group.uuid}`)
        store.remove(group.uuid)
        selectedUuid.value = groups.value[0]?.uuid || ''
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('expertGroups.deleteFailed'))
        throw error
      }
    },
  })
}

function openMemberEditor(role: 'leader' | 'member', member?: ExpertMember) {
  memberRole.value = role
  editingMember.value = member
  memberEditorOpen.value = true
}

function removeMember(member: ExpertMember) {
  if (!selectedGroup.value) return
  Modal.confirm({
    title: t('expertGroups.deleteAgent'),
    okType: 'danger',
    onOk: async () => {
      try {
        await apiClient.delete(`/expert-groups/${selectedGroup.value?.uuid}/members/${member.uuid}`)
        await reload(selectedUuid.value)
        message.success(t('expertGroups.memberRemoved'))
      } catch (error) {
        message.error(error instanceof Error ? error.message : t('expertGroups.memberDeleteFailed'))
        throw error
      }
    },
  })
}

async function openReusablePicker(role: 'leader' | 'member') {
  memberRole.value = role
  copyModalOpen.value = true
  reusableLoading.value = true
  try {
    const result = await apiClient.get<{ items: ReusableAgent[] }>('/expert-groups/reusable-agents')
    reusableAgents.value = result.items || []
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('expertGroups.loadFailed'))
  } finally {
    reusableLoading.value = false
  }
}

function toggleBatchMode() {
  if (batchMode.value) {
    resetBatchState()
    return
  }
  batchMode.value = true
}

function resetBatchState() {
  batchMode.value = false
  selectedMemberUuids.value = []
}

function setBatchSaving(saving: boolean) {
  batchSaving.value = saving
  emit('batch-saving-change', saving)
}

function clearBatchSelection() {
  if (batchSaving.value) return
  selectedMemberUuids.value = []
}

function selectAllMembers() {
  if (batchSaving.value) return
  selectedMemberUuids.value = allMembers.value.map((member) => member.uuid)
}

function toggleMemberSelection(memberUuid: string) {
  if (batchSaving.value) return
  selectedMemberUuids.value = selectedMemberUuids.value.includes(memberUuid)
    ? selectedMemberUuids.value.filter((item) => item !== memberUuid)
    : [...selectedMemberUuids.value, memberUuid]
}

async function applyBatchConfiguration(payload: { cliType: string; modelName: string; thinkingLevel?: string }) {
  if (!selectedGroup.value || !selectedMemberUuids.value.length || batchSaving.value) return
  const groupUuid = selectedGroup.value.uuid
  const memberUuids = [...selectedMemberUuids.value]
  const selectedCount = memberUuids.length
  setBatchSaving(true)
  try {
    const updated = await apiClient.put<ExpertGroup>(
      `/expert-groups/${encodeURIComponent(groupUuid)}/members/execution-config`,
      {
        member_uuids: memberUuids,
        cli_type: payload.cliType,
        model_name: payload.modelName,
        thinking_level: payload.thinkingLevel,
      },
    )
    store.upsert(updated)
    message.success(t('agents.batchUpdated', { count: selectedCount }))
    resetBatchState()
  } catch (error) {
    message.error(error instanceof Error ? error.message : t('agents.batchFailed'))
  } finally {
    setBatchSaving(false)
  }
}

watch(
  () => groups.value.length,
  () => {
    if (!selectedUuid.value) selectedUuid.value = groups.value[0]?.uuid || ''
  },
  { immediate: true },
)
watch(
  () => props.selectedUuid,
  () => resetBatchState(),
)
watch(
  () => allMembers.value.map((member) => member.uuid).join(','),
  () => {
    const available = new Set(allMembers.value.map((member) => member.uuid))
    selectedMemberUuids.value = selectedMemberUuids.value.filter((uuid) => available.has(uuid))
  },
)

defineExpose({ openGroupEditor, handleGroupAction })
</script>

<style scoped>
.expert-workspace {
  display: flex;
  min-width: 0;
  min-height: 0;
  flex: 1;
  background: #fff;
}

.expert-detail-pane {
  position: relative;
  display: flex;
  min-width: 0;
  min-height: 0;
  flex: 1;
  flex-direction: column;
  overflow: hidden;
}

.detail-scroll {
  min-height: 0;
  flex: 1;
  overflow-y: auto;
  padding: 0 24px 24px;
}

.detail-scroll--batch {
  padding-bottom: 120px;
}

.member-section--leader {
  border-bottom: 1px solid #f0f0f0;
}

.member-section--leader :deep(.member-row::after) {
  display: none;
}

.member-empty {
  display: flex;
  min-height: 280px;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  padding: 32px 16px;
  text-align: center;
}

.member-empty :deep(.ant-empty) {
  margin: 0;
}

.member-empty :deep(.ant-empty-description) {
  color: #262626;
  font-size: 16px;
  font-weight: 600;
}

.member-empty p {
  margin: 4px 0 18px;
  color: #8c8c8c;
  font-size: 14px;
  line-height: 22px;
}

.empty-actions {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
}

.empty-actions :deep(.ant-btn) {
  height: 34px;
  border-radius: 6px;
  box-shadow: none;
}

.expert-detail-empty {
  display: flex;
  flex: 1;
  align-items: center;
  justify-content: center;
}

@media (max-width: 900px) {
  .expert-workspace {
    min-height: 420px;
  }
}
</style>
