<script setup lang="ts">
import { ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import apiClient, { ApiError } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { useConversationStore } from '@/stores/conversation'
import { useLastLogin } from '@/composables/useLastLogin'
import { refreshSession } from '@/composables/useCloudSession'
import { clearTaskFilesIndexCache } from '@/composables/useTaskFilesIndex'
import LoginForm from './components/LoginForm.vue'
import MyWorkView from './components/MyWorkView.vue'
import { useAppI18n } from '@/i18n'

type CloudConfigState = 'loading' | 'configured' | 'unconfigured'

interface CloudLoginResponse {
  status: string
  user?: {
    id: string
    username: string
    display_name?: string
    displayName?: string
    avatar?: string
    role?: string
  }
  device_registered?: boolean
  admin_id?: string
}

const authStore = useAuthStore()
const configState = ref<CloudConfigState>('loading')
const loginLoading = ref(false)
const officialUrl = ref('')
const { t } = useAppI18n()

async function loadCloudStatus() {
  try {
    const data = await apiClient.get<{ api_base_url: string }>('/config/cloud')
    officialUrl.value = data.api_base_url?.trim() ?? ''
    configState.value = officialUrl.value ? 'configured' : 'unconfigured'
  } catch {
    configState.value = 'unconfigured'
  }
}

async function handleLogin(form: {
  serverType: 'official' | 'custom'
  serverUrl: string
  username: string
  password: string
}) {
  loginLoading.value = true
  try {
    const serverType = form.serverType === 'custom' ? 'custom' : 'official'
    await apiClient.post<CloudLoginResponse>('/auth/login', {
      username: form.username,
      password: form.password,
      server_type: serverType,
      server_url: serverType === 'custom' ? form.serverUrl : officialUrl.value,
    })
    await refreshSession()
    clearTaskFilesIndexCache()
    void useConversationStore().loadConversations(true).catch(() => {})
    message.success(t('workbench.loginSuccess'))

    // Remember this login method and auto-select it and refill the custom domain next time.
    useLastLogin().saveLastLogin({
      serverType,
      serverUrl: serverType === 'custom' ? form.serverUrl : officialUrl.value,
    })
  } catch (error) {
    const errorMessage = error instanceof ApiError ? error.message : t('workbench.loginFailed')
    message.error(errorMessage)
  } finally {
    loginLoading.value = false
  }
}

watch(() => authStore.cloudLoggedIn, (loggedIn) => {
  if (!loggedIn && configState.value === 'loading') {
    void loadCloudStatus()
  }
}, { immediate: true })
</script>

<template>
  <MyWorkView v-if="authStore.cloudLoggedIn" />
  <div v-else class="workbench-page">
    <section class="login-card">
      <div class="brand-mark" aria-hidden="true">
        <img src="@/assets/logo.svg" alt="TeamBuddy" />
      </div>

      <div v-if="configState === 'loading'" class="card-state loading-state">
        <a-spin size="large" />
      </div>

      <template v-else>
        <h1>{{ t('workbench.welcome') }}</h1>
        <p class="login-subtitle">{{ t('workbench.loginSubtitle') }}</p>

        <LoginForm
          :loading="loginLoading"
          :official-url="officialUrl"
          @submit="handleLogin"
        />
      </template>
    </section>
  </div>
</template>

<style scoped>
.workbench-page {
  display: flex;
  width: 100%;
  min-height: 100%;
  align-items: center;
  justify-content: center;
  background: #f5f6f8;
}

.login-card {
  display: flex;
  width: min(500px, 100%);
  min-height: 516px;
  flex-direction: column;
  align-items: center;
  padding: 50px;
  border-radius: 8px;
  background: #fff;
  box-shadow: 0 14px 30px rgba(15, 23, 42, 0.14);
}

.brand-mark {
  display: flex;
  width: 80px;
  height: 80px;
  flex: 0 0 80px;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
}
.brand-mark img{
  width: 100%;
  height: 100%;
}

.login-card h1 {
  margin: 30px 0 0;
  color: #111827;
  font-size: 26px;
  font-weight: 600;
  line-height: 36px;
}

.login-subtitle,
.card-state p {
  margin: 8px 0 0;
  color: #63738c;
  font-size: 16px;
  line-height: 24px;
  text-align: center;
}

.card-state {
  display: flex;
  width: 100%;
  flex: 1;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  padding-bottom: 50px;
}

.card-state h1 {
  margin-top: 0;
}

@media (max-width: 620px) {
  .login-card {
    min-height: 480px;
    padding: 40px 24px;
  }

  .brand-mark {
    width: 72px;
    height: 72px;
    flex-basis: 72px;
  }

  .login-card h1 {
    font-size: 24px;
  }
}
</style>
