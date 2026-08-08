// stores/sessions.js — 本地多会话管理（localStorage 持久化，沿用旧 key 'ai_sessions'）。
// 消息以结构化对象存储：
//   user: { role:'user', text }
//   ai:   { role:'ai', mode, steps:[], memory, toolCall, ragResults, answer, interrupted, sandbox, streaming }
import { defineStore } from 'pinia'

const KEY = 'ai_sessions'

function load() {
  try { return JSON.parse(localStorage.getItem(KEY) || '[]') } catch { return [] }
}

export const useSessions = defineStore('sessions', {
  state: () => ({
    sessions: load(),
    currentId: null,
  }),
  getters: {
    current: (s) => s.sessions.find(x => x.id === s.currentId) || null,
    messages() { return this.current ? this.current.messages : [] },
  },
  actions: {
    save() { localStorage.setItem(KEY, JSON.stringify(this.sessions)) },
    newSession() {
      const id = Date.now().toString()
      this.sessions.unshift({ id, title: '新对话', messages: [], ts: Date.now() })
      if (this.sessions.length > 5) this.sessions = this.sessions.slice(0, 5)
      this.currentId = id
      this.save()
      return id
    },
    switchSession(id) { this.currentId = id },
    deleteSession(id) {
      this.sessions = this.sessions.filter(s => s.id !== id)
      if (this.currentId === id) this.currentId = this.sessions[0]?.id || null
      this.save()
    },
    // 追加一条消息，返回其在 state 中的响应式引用（供流式就地更新）。
    addMessage(sessionId, msg) {
      const s = this.sessions.find(x => x.id === sessionId)
      if (!s) return null
      s.messages.push(msg)
      if (s.messages.filter(m => m.role === 'user').length === 1 && msg.role === 'user') {
        s.title = (msg.text || '').slice(0, 20) || '新对话'
      }
      this.save()
      return s.messages[s.messages.length - 1]
    },
    reset() { this.sessions = []; this.currentId = null; this.save() },
  },
})
