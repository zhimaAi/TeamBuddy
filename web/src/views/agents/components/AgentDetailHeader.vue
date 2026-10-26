<template>
  <header class="agent-detail-header">
    <div class="header-copy">
      <h3 :title="title">{{ title }}</h3>
    </div>
    <div
      v-if="$slots.actions"
      class="header-actions"
    >
      <slot name="actions" />
    </div>
  </header>
</template>

<script setup lang="ts">
import { useAppI18n } from '@/i18n'

defineProps<{ title: string }>()

const { t } = useAppI18n()
void t
</script>

<style scoped>
.agent-detail-header {
  position: relative;
  z-index: 1;
  display: flex;
  min-height: 64px;
  flex: 0 0 64px;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  padding: 16px 24px;
  background: #fff;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.08);
}

.header-copy {
  display: flex;
  min-width: 0;
  flex: 1;
  flex-direction: column;
  gap: 4px;
}

.header-eyebrow {
  display: flex;
  align-items: center;
  gap: 4px;
  color: #86868b;
  font-size: 12px;
  line-height: 20px;
}

.header-eyebrow img {
  width: 14px;
  height: 14px;
  flex: 0 0 14px;
}

.agent-detail-header h3 {
  overflow: hidden;
  margin: 0;
  color: #262626;
  font-size: 20px;
  font-weight: 600;
  line-height: 28px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.header-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 8px;
}

.agent-detail-header :deep(.ant-btn) {
  height: 32px;
  padding-inline: 16px;
  border-radius: 6px;
  box-shadow: none;
  font-size: 14px;
}

.agent-detail-header :deep(.ant-btn-primary) {
  border-color: #3157e2;
  background: #3157e2;
}

.agent-detail-header :deep(.ant-btn-primary:hover) {
  border-color: #2475fc;
  background: #2475fc;
}

.agent-detail-header :deep(.ant-btn-primary:active) {
  border-color: #3157e2;
  background: #3157e2;
}

.agent-detail-header :deep(.batch-config-button.ant-btn),
.agent-detail-header :deep(.batch-cancel-button.ant-btn) {
  display: inline-flex;
  min-width: 112px;
  flex: 0 0 auto;
  align-items: center;
  justify-content: center;
  white-space: nowrap;
}

.agent-detail-header :deep(.batch-cancel-button.ant-btn) {
  color: #3157e2;
  border-color: transparent;
  background: #e5efff;
}

.agent-detail-header :deep(.batch-cancel-button.ant-btn:hover:not(:disabled)) {
  color: #2475fc;
  border-color: transparent;
  background: #dce8ff;
}

.agent-detail-header :deep(.batch-cancel-icon),
.agent-detail-header :deep(.batch-config-icon) {
  display: block;
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
  margin-inline-end: 8px;
}

@media (max-width: 760px) {
  .agent-detail-header {
    min-height: 64px;
    flex: 0 0 auto;
    align-items: flex-start;
    flex-direction: column;
    gap: 12px;
  }

  .header-actions {
    width: 100%;
    flex-wrap: wrap;
  }
}
</style>
