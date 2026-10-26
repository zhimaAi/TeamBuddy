import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { UserInfo } from '@/api/types'

export const useAuthStore = defineStore('auth', () => {
  // ---- Local browser Session ----
  const localSession = ref<boolean>(
    localStorage.getItem('goteams_local_session') === 'true',
  )

  // ---- Cloud login ----
  // The cloud JWT is only saved in the SecretStore of the Go service, and the browser only holds the HttpOnly Session.
  const cloudToken = ref<string | null>(null)
  const cloudUser = ref<UserInfo | null>(null)
  const cloudSession = ref(false)
  const sessionReady = ref(false)
  let resolveSessionReady: () => void
  const sessionReadyPromise = new Promise<void>((resolve) => { resolveSessionReady = resolve })

  function markSessionReady() {
    sessionReady.value = true
    resolveSessionReady()
  }

  function waitForSessionReady(): Promise<void> {
    return sessionReady.value ? Promise.resolve() : sessionReadyPromise
  }

  // ---- Getters ----
  const hasLocalSession = computed(() => localSession.value)
  const cloudLoggedIn = computed(() => cloudSession.value)

  // Backwards compatible aliases
  const token = cloudToken
  const user = cloudUser
  const isAuthenticated = cloudLoggedIn

  // ---- Actions ----
  /** Set up local Session */
  function setLocalSession(value: boolean) {
    localSession.value = value
    localStorage.setItem('goteams_local_session', value ? 'true' : 'false')
  }

  /** Set cloud authentication information */
  function setCloudAuth(newToken: string, userInfo: UserInfo) {
    // newToken only retains parameters to be compatible with the old caller, and does not write localStorage.
    cloudToken.value = newToken || 'http-only-session'
    cloudUser.value = userInfo
    cloudSession.value = true
  }

  /** Clear cloud authentication information */
  function clearCloudAuth() {
    cloudToken.value = null
    cloudUser.value = null
    cloudSession.value = false
    localStorage.removeItem('goteams_cloud_token')
    localStorage.removeItem('goteams_cloud_user')
  }

  // Backwards compatible aliases
  function setAuth(newToken: string, userInfo: UserInfo) {
    setCloudAuth(newToken, userInfo)
    setLocalSession(true)
  }

  function logout() {
    clearCloudAuth()
  }

  return {
    localSession,
    sessionReady,
    markSessionReady,
    waitForSessionReady,
    cloudToken,
    cloudUser,
    cloudSession,
    hasLocalSession,
    cloudLoggedIn,
    // backward compatible
    token,
    user,
    isAuthenticated,
    setLocalSession,
    setCloudAuth,
    clearCloudAuth,
    setAuth,
    logout,
  }
})
