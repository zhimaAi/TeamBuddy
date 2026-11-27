<template>
  <div>
    <p>{{ description }}</p>
    <template v-if="cli && (state === 'not_configured' || state === 'hooks_untrusted')">
      <ol class="codex-trust-steps">
        <li>{{ t('settings.codexReporting.cli.selectHooks') }}</li>
        <li>{{ t('settings.codexReporting.trustCallbacks') }}</li>
        <li>{{ t('settings.codexReporting.returnToTask') }}</li>
      </ol>
    </template>
    <template v-else-if="!cli && (state === 'not_configured' || state === 'hooks_untrusted' || state === 'inspection_failed')">
      <ol class="codex-trust-steps">
        <li>{{ t('settings.codexReporting.selectHooks') }}</li>
        <li>{{ t('settings.codexReporting.trustCallbacks') }}</li>
        <li>{{ t('settings.codexReporting.returnToTask') }}</li>
      </ol>
      <p class="codex-reload-tip">{{ t('settings.codexReporting.reloadHint') }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
// 静态确认框独立挂载，不依赖页面注入的 i18n 上下文。
import { t } from '@/i18n'

defineProps<{ state: CodexSyncState; description: string; cli?: boolean }>()
</script>

<style scoped>
.codex-trust-steps { padding-inline-start: 1.5em; }
.codex-trust-steps li + li { margin-top: 4px; }
.codex-reload-tip { color: var(--ant-color-text-secondary, #595959); }
</style>
