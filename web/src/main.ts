import { createApp } from 'vue'
import { createPinia } from 'pinia'
import Antd, { message } from 'ant-design-vue'
import App from './App.vue'
import router from './router'
import { setApiFeedbackHandler } from '@/api/client'
import { applySession, fetchSession, registerSessionRecovery } from '@/composables/useCloudSession'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { i18n, initializeLocale, startLocaleSync } from '@/i18n'
import './assets/styles/global.css'

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(i18n)

async function bootstrap() {
  // Before the first navigation of the route, restore the logged-in state with the backend HttpOnly Session as the only truth.
  const authStore = useAuthStore(pinia)
  const appStore = useAppStore(pinia)

  console.log(
    '[app] 前端启动 mode=' +
      import.meta.env.MODE +
      ' api_base_url=' +
      (import.meta.env.VITE_API_BASE_URL || '(空)'),
  )

  initializeLocale()
  startLocaleSync()

  // 先挂载页面再异步探测会话，避免启动等待导致白屏
  app.use(router)
  app.use(Antd)
  setApiFeedbackHandler(({ message: feedbackMessage }) => {
    message.warning(feedbackMessage)
  })
  registerSessionRecovery(pinia)
  app.mount('#app')

  try {
    // 首次握手给后端留 30 秒：dev 模式下 go run 可能仍在编译。
    applySession(await fetchSession(30_000), pinia)
  } catch (error) {
    console.log('[session] 启动探测 30 秒耗尽仍未成功', String(error))
    authStore.setLocalSession(false)
    authStore.clearCloudAuth()
    appStore.setCloudStatus('offline')
  } finally {
    authStore.markSessionReady()
  }
}

void bootstrap()
