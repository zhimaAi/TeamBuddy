<template>
  <a-card :title="t('settings.codexReporting.title')">
    <p>{{ t('settings.codexReporting.scope') }}</p>
    <CodexSyncGuidance v-if="state && !error" :state="state" :description="syncDescription(state)" aria-live="polite" />
    <a-alert v-if="error" type="error" show-icon :message="error" />
    <a-button v-if="state !== 'ready' || error" :type="state && !error ? 'primary' : 'link'" :loading="busy" @click="activate">
      {{ error ? t('common.actions.retry') : state ? syncAction(state) : t('settings.codexReporting.checking') }}
    </a-button>
  </a-card>
</template>

<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import CodexSyncGuidance from '@/components/CodexSyncGuidance.vue'
import { useAppI18n } from '@/i18n'
import { configureCodexReporting } from '@/composables/useDesktop'
import { ensureCodexSync, syncDescription, syncAction, syncError } from '@/composables/useCodexSync'
const { t } = useAppI18n()
const state = ref<CodexSyncState>(), busy = ref(false), error = ref('')
let mounted = true
async function refresh() {
  if (!mounted || busy.value) return
  busy.value = true
  try { state.value = await configureCodexReporting('status'); error.value = '' }
  catch (e) { error.value = syncError(e) }
  finally { busy.value = false }
}
async function activate() {
  if (busy.value) return
  if (!state.value || error.value) { await refresh(); return }
  busy.value = true
  try { await ensureCodexSync({ initial: state.value, confirmed: true }) }
  catch (e) { error.value = syncError(e) }
  finally { busy.value = false }
  await refresh()
}
onMounted(() => { void refresh(); window.addEventListener('focus', refresh) })
onBeforeUnmount(() => { mounted = false; window.removeEventListener('focus', refresh) })
</script>
