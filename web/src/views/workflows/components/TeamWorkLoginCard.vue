<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { useAppI18n } from '@/i18n'
import { useLastLogin } from '@/composables/useLastLogin'
import { useBrowserLogin } from '@/composables/useBrowserLogin'
import featureAgentIcon from '@/assets/icons/team-login-feature-agent.svg'
import featureCollaborateIcon from '@/assets/icons/team-login-feature-collaborate.svg'
import featureSyncIcon from '@/assets/icons/team-login-feature-sync.svg'

const { t } = useAppI18n()
const { pending, errorMessage, startBrowserLogin, cancelBrowserLogin } = useBrowserLogin()
const { getLastLogin, getSavedServers, saveLastLogin } = useLastLogin()
const customLoginOpen = ref(false)
const customUrl = ref('')
const addressError = ref('')
const addressInput = ref<{ focus: () => void } | null>(null)

const emit = defineEmits<{
  (event: 'use-local'): void
}>()

const features = computed(() => [
  { key: 'sync', label: t('teamwork.login.featureSync'), icon: featureSyncIcon },
  { key: 'collaborate', label: t('teamwork.login.featureCollaborate'), icon: featureCollaborateIcon },
  { key: 'agent', label: t('teamwork.login.featureAgent'), icon: featureAgentIcon },
])

function handleLogin() {
  if (pending.value) return
  addressError.value = ''
  void startBrowserLogin()
}

async function showCustomLogin() {
  const last = getLastLogin()
  customUrl.value = last?.serverType === 'custom' ? last.serverUrl : getSavedServers()[0] || ''
  addressError.value = ''
  customLoginOpen.value = true
  await nextTick()
  addressInput.value?.focus()
}

async function authorizeCustomLogin() {
  if (pending.value) return
  addressError.value = ''
  const address = customUrl.value.trim()
  if (!address) {
    addressError.value = t('teamwork.connection.addressRequired')
    addressInput.value?.focus()
    return
  }
  let serverUrl: string
  try {
    const parsed = new URL(address.includes('://') ? address : `https://${address}`)
    if (!['https:', 'http:'].includes(parsed.protocol) || !parsed.hostname || parsed.username || parsed.password || parsed.search || parsed.hash) throw new Error()
    serverUrl = parsed.toString()
  } catch {
    addressError.value = t('teamwork.connection.addressInvalid')
    addressInput.value?.focus()
    return
  }
  const ok = await startBrowserLogin({ serverType: 'custom', serverUrl })
  if (ok) saveLastLogin({ serverType: 'custom', serverUrl })
}

function closeCustomLogin() {
  customLoginOpen.value = false
  addressError.value = ''
}

function handleCancel() {
  void cancelBrowserLogin()
}
</script>

<template>
  <section class="team-login-card">
    <div class="team-login-intro">
      <h2 class="team-login-title">{{ t('teamwork.login.title') }}</h2>
      <p class="team-login-subtitle">{{ t('teamwork.login.subtitle') }}</p>
    </div>

    <p class="team-login-description">{{ t('teamwork.login.description') }}</p>

    <ul class="team-login-features">
      <li v-for="feature in features" :key="feature.key" class="team-login-feature">
        <img class="team-login-feature-icon" :src="feature.icon" alt="" aria-hidden="true">
        <span class="team-login-feature-label">{{ feature.label }}</span>
      </li>
    </ul>

    <div class="team-login-actions">
      <button
        type="button"
        class="team-login-submit"
        :disabled="pending"
        @click="handleLogin"
      >
        <span v-if="pending" class="team-login-spinner" aria-hidden="true" />
        {{ t('teamwork.login.action') }}
      </button>

      <button v-if="!customLoginOpen" type="button" class="team-login-link" :disabled="pending" @click="showCustomLogin">
        {{ t('teamwork.connection.customAction') }}
      </button>
      <form v-else class="custom-login-form" @submit.prevent="authorizeCustomLogin">
        <label for="team-custom-server">{{ t('teamwork.connection.openSourceAddress') }}</label>
        <a-input
          id="team-custom-server"
          ref="addressInput"
          v-model:value="customUrl"
          size="large"
          :disabled="pending"
          :placeholder="t('teamwork.connection.openSourceAddressPlaceholder')"
          :status="addressError ? 'error' : undefined"
          :aria-invalid="!!addressError"
          :aria-describedby="addressError ? 'team-custom-server-error' : undefined"
          @update:value="addressError = ''"
        />
        <p v-if="addressError" id="team-custom-server-error" class="team-login-error" role="alert">{{ addressError }}</p>
        <a-button class="custom-login-authorize" size="large" html-type="submit" :loading="pending" :disabled="pending">
          {{ t('teamwork.connection.authorize') }}
        </a-button>
        <a-button v-if="!pending" type="text" @click="closeCustomLogin">{{ t('common.actions.cancel') }}</a-button>
      </form>
      <p v-if="pending" class="team-login-hint">
        <span>{{ t('teamwork.login.pending') }}</span>
        <button type="button" class="team-login-link" @click="handleCancel">
          {{ t('teamwork.login.cancel') }}
        </button>
      </p>
      <p v-else class="team-login-hint">
        <span>{{ t('teamwork.login.later') }}</span>
        <button type="button" class="team-login-link" @click="emit('use-local')">
          {{ t('teamwork.login.laterLocal') }}
        </button>
      </p>

      <p v-if="errorMessage" class="team-login-error" role="alert">{{ errorMessage }}</p>
    </div>
  </section>
</template>

<style scoped>
.team-login-card {
  box-sizing: border-box;
  display: flex;
  width: 660px;
  max-width: 100%;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding: 60px 64px 56px;
  border: 1px solid rgb(29 29 31 / 9%);
  border-radius: 18px;
  background: #fff;
  box-shadow: 0 14px 36px rgb(15 23 42 / 6%), 0 1px 2px rgb(0 0 0 / 4%);
}

.team-login-intro {
  display: flex;
  flex-direction: column;
  align-items: center;
}

.team-login-title {
  margin: 0;
  color: #1d1d1f;
  font-size: 22px;
  font-weight: 600;
  letter-spacing: -0.55px;
  line-height: 33px;
  text-align: center;
}

.team-login-subtitle {
  margin: 0;
  padding-top: 4px;
  color: #8c8c8c;
  font-size: 14px;
  font-weight: 400;
  line-height: 22px;
  text-align: center;
}

.team-login-description {
  width: 500px;
  max-width: 100%;
  margin: 0;
  padding-top: 24px;
  color: #595959;
  font-size: 16px;
  font-weight: 400;
  line-height: 24px;
  text-align: center;
}

.team-login-features {
  display: grid;
  width: 500px;
  max-width: 100%;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
  margin: 0;
  padding: 24px 0 0;
  list-style: none;
}

.team-login-feature {
  display: flex;
  height: 44px;
  align-items: center;
  justify-content: center;
  gap: 7px;
  border-radius: 10px;
  background: #f7f9fc;
}

.team-login-feature-icon {
  display: block;
  width: 17px;
  height: 17px;
  flex: 0 0 17px;
}

.team-login-feature-label {
  min-width: 0;
  overflow: hidden;
  color: #667085;
  font-size: 12px;
  font-weight: 600;
  line-height: 20px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.team-login-actions {
  display: flex;
  width: 300px;
  max-width: 100%;
  flex-direction: column;
  align-items: center;
  gap: 16px;
  /* 设计稿 3099:14716 的内层列高 233.391（内容 201.391），按钮容器紧接其后，
     净间距 32px；按此对齐，整卡高度回到设计稿的 436px。 */
  padding-top: 32px;
}

.team-login-submit {
  display: flex;
  width: 100%;
  height: 48px;
  align-items: center;
  justify-content: center;
  gap: 10px;
  padding: 12px 16px;
  border: 0;
  border-radius: 6px;
  background: #3157e2;
  box-shadow: 0 5px 12px rgb(0 113 227 / 16%);
  color: #fff;
  cursor: pointer;
  font-size: 16px;
  font-weight: 400;
  line-height: 24px;
  transition: background-color 180ms ease, box-shadow 180ms ease;
}

.custom-login-form {
  display: flex;
  width: 100%;
  min-width: 0;
  flex-direction: column;
  gap: 12px;
}

.custom-login-form label {
  color: #595959;
  font-size: 14px;
}

.custom-login-authorize {
  height: auto;
  min-height: 40px;
  border-color: #3157e2;
  color: #3157e2;
  white-space: normal;
}

.team-login-submit:hover:not(:disabled) {
  background: #2a4bcb;
}

.team-login-submit:disabled {
  cursor: not-allowed;
  opacity: 0.7;
}

.team-login-submit:focus-visible,
.team-login-link:focus-visible {
  outline: 2px solid #3157e2;
  outline-offset: 2px;
}

.team-login-spinner {
  display: block;
  width: 16px;
  height: 16px;
  flex: 0 0 16px;
  border: 2px solid rgb(255 255 255 / 45%);
  border-radius: 50%;
  border-top-color: #fff;
  animation: team-login-spin 900ms linear infinite;
}

@keyframes team-login-spin {
  to { transform: rotate(360deg); }
}

.team-login-hint {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: center;
  gap: 4px;
  row-gap: 2px;
  margin: 0;
  color: #8c8c8c;
  font-size: 14px;
  font-weight: 400;
  line-height: 22px;
  text-align: center;
}

/* 提示文案较长，300px 内放不下时按钮整体换到下一行居中；同时把操作按钮固定成
   不可收缩、不拆字的一整块，否则 flex 收缩会把「取消登录」压成两行两字。 */
.team-login-hint > span {
  min-width: 0;
  flex: 0 1 auto;
}

.team-login-link {
  flex: 0 0 auto;
  padding: 0;
  border: 0;
  border-radius: 4px;
  background: transparent;
  color: #3157e2;
  cursor: pointer;
  font-size: 14px;
  font-weight: 600;
  line-height: 22px;
  white-space: nowrap;
}

.team-login-error {
  margin: 0;
  color: #fb363f;
  font-size: 13px;
  line-height: 20px;
  text-align: center;
}

@media (max-width: 640px) {
  .team-login-card {
    padding: 32px 24px 28px;
  }

  .team-login-actions {
    padding-top: 32px;
  }

  .team-login-features {
    grid-template-columns: minmax(0, 1fr);
  }
}

@media (prefers-reduced-motion: reduce) {
  .team-login-submit {
    transition: none;
  }

  .team-login-spinner {
    animation-duration: 2s;
  }
}
</style>
