// stores/auth.js — 登录/注册/登出 + JWT 持久化（localStorage）。
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

const TOKEN_KEY = 'agi_auth_token'
const USER_KEY = 'agi_auth_user'

export const useAuth = defineStore('auth', {
  state: () => ({
    token: localStorage.getItem(TOKEN_KEY) || '',
    username: localStorage.getItem(USER_KEY) || '',
    mode: 'login', // 'login' | 'register'
    error: '',
    overlay: !(localStorage.getItem(TOKEN_KEY)), // 无 token 则一开始就显示登录层
    submitting: false,
  }),
  getters: {
    loggedIn: (s) => !!s.token,
  },
  actions: {
    setMode(m) { this.mode = m; this.error = '' },
    showOverlay(msg) { this.overlay = true; if (msg) this.error = msg },
    hideOverlay() { this.overlay = false; this.error = '' },
    persist() {
      localStorage.setItem(TOKEN_KEY, this.token)
      localStorage.setItem(USER_KEY, this.username)
    },
    async submit(username, password) {
      if (!username || !password) { this.error = '用户名和密码不能为空'; return false }
      this.submitting = true
      this.error = ''
      const url = this.mode === 'login' ? '/api/auth/login' : '/api/auth/register'
      try {
        const resp = await fetch((import.meta.env.VITE_API_BASE || '') + url, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ username, password }),
        })
        const data = await resp.json()
        if (!resp.ok) { this.error = data.error || '操作失败'; return false }
        this.token = data.token
        this.username = data.username
        this.persist()
        this.hideOverlay()
        return true
      } catch (e) {
        this.error = '网络错误，请稍后再试'
        return false
      } finally {
        this.submitting = false
      }
    },
    logout() {
      this.token = ''
      this.username = ''
      localStorage.removeItem(TOKEN_KEY)
      localStorage.removeItem(USER_KEY)
      this.showOverlay()
    },
  },
})
