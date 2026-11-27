<template>
  <article
    class="member-row"
    :class="{
      'member-row--batch': batchMode,
      'member-row--selected': batchMode && selected,
    }"
  >
    <a-checkbox
      v-if="batchMode"
      class="member-checkbox"
      :checked="selected"
      :aria-label="t('agents.selectAgent', { name: member.name })"
      @change="emit('toggle')"
    />
    <div class="member-content">
      <div class="member-heading">
        <img
          :src="member.avatar || AGENT_AVATARS[0]"
          alt=""
        />
        <strong>{{ member.name }}</strong>
        <span
          v-if="member.member_role === 'leader'"
          class="leader-badge"
        >
          <CrownOutlined />
          {{ t('expertGroups.leader') }}
        </span>
      </div>
      <p class="member-description">
        {{ member.description || member.prompt || t('agents.missingDescription') }}
      </p>
      <footer>
        <div class="tag-list">
          <span class="badge">
            <img
              :src="pipelineAgentCliIcon"
              alt=""
              aria-hidden="true"
            />
            {{ member.cli_type }}
          </span>
          <span class="badge">
            <img
              :src="pipelineAgentModelIcon"
              alt=""
              aria-hidden="true"
            />
            {{ member.model_name === DEFAULT_CLI_MODEL ? t('components.cliSelect.modelDefault') : member.model_name }}
          </span>
        </div>
        <div class="member-actions">
          <button
            type="button"
            class="action-btn action-btn-edit"
            :aria-label="t('expertGroups.editAgent')"
            @click="emit('edit')"
          >
            <EditOutlined />
          </button>
          <button
            type="button"
            class="action-btn action-btn-danger"
            :aria-label="t('expertGroups.deleteAgent')"
            @click="emit('remove')"
          >
            <DeleteOutlined />
          </button>
        </div>
      </footer>
    </div>
  </article>
</template>

<script setup lang="ts">
import { DEFAULT_CLI_MODEL } from '@/utils/cliModelOptions'
import { CrownOutlined, DeleteOutlined, EditOutlined } from '@ant-design/icons-vue'
import pipelineAgentCliIcon from '@/assets/icons/pipeline-agent-cli.svg'
import pipelineAgentModelIcon from '@/assets/icons/pipeline-agent-model.svg'
import { useAppI18n } from '@/i18n'
import type { ExpertMember } from '@/types/pipeline'
import { AGENT_AVATARS } from './agentPipeline'

defineProps<{
  member: ExpertMember
  batchMode?: boolean
  selected?: boolean
}>()

const emit = defineEmits<{
  edit: []
  remove: []
  toggle: []
}>()
const { t } = useAppI18n()
</script>

<style scoped>
.member-row {
  position: relative;
  display: flex;
  gap: 16px;
  overflow: hidden;
  padding: 24px 0;
  background: #fff;
}

.member-row::after {
  position: absolute;
  right: 0;
  bottom: 0;
  left: 0;
  height: 1px;
  background: #f0f0f0;
  content: '';
}

.member-row--batch {
  padding: 24px 8px;
  border-radius: 12px;
}

.member-row--selected {
  background: #f5f9ff;
}

.member-row--selected::after {
  display: none;
}

.member-checkbox {
  flex: 0 0 auto;
  margin-top: 12px;
}

.member-checkbox :deep(.ant-checkbox-inner) {
  width: 16px;
  height: 16px;
  border-color: #d9d9d9;
  border-radius: 2px;
}

.member-content {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  gap: 20px;
}

.member-heading {
  display: flex;
  min-width: 0;
  align-items: center;
  gap: 10px;
}

.member-heading > img {
  width: 40px;
  height: 40px;
  flex: 0 0 40px;
  border-radius: 50%;
  object-fit: cover;
}

.member-heading strong {
  overflow: hidden;
  color: #1d1d1f;
  font-size: 16px;
  font-weight: 600;
  line-height: 24px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.leader-badge {
  display: inline-flex;
  height: 20px;
  flex: 0 0 auto;
  align-items: center;
  gap: 3px;
  padding: 0 5px;
  border-radius: 6px;
  color: #3157e2;
  background: #e5efff;
  font-size: 12px;
  line-height: 20px;
}

.member-description {
  display: -webkit-box;
  overflow: hidden;
  margin: 0;
  color: #595959;
  font-size: 14px;
  line-height: 26px;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.member-row footer {
  display: flex;
  min-height: 26px;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

.tag-list,
.member-actions {
  display: flex;
  align-items: center;
}

.tag-list {
  min-width: 0;
  gap: 8px;
}

.member-actions {
  flex: 0 0 auto;
  gap: 8px;
}

.badge {
  display: inline-flex;
  height: 26px;
  min-width: 0;
  align-items: center;
  gap: 5px;
  overflow: hidden;
  padding: 0 10px;
  border-radius: 999px;
  color: #6e6e73;
  background: rgba(0, 0, 0, 0.05);
  font-size: 12px;
  font-weight: 600;
  line-height: 26px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.badge img {
  width: 14px;
  height: 14px;
  flex: 0 0 14px;
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
  color: #595959;
  background: transparent;
  cursor: pointer;
  transition:
    color 0.18s,
    background-color 0.18s;
}

.action-btn:hover {
  color: #262626;
  background: rgba(0, 0, 0, 0.06);
}

.action-btn-edit {
  color: #3157e2;
}

.action-btn-edit:hover {
  color: #3157e2;
}

.action-btn-danger:hover {
  color: #ff4d4f;
  background: #fff1f0;
}

.action-btn:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

@media (prefers-reduced-motion: reduce) {
  .action-btn {
    transition: none;
  }
}
</style>
