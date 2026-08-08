// stores/chat.js — 对话发送 + SSE 流式，事件驱动就地更新消息对象。
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { readSSE } from '../composables/useSSE'
import { useSessions } from './sessions'
import { useDocs } from './docs'

export const useChat = defineStore('chat', {
  state: () => ({
    loading: false,
    abort: null,
    ragOn: false,
  }),
  getters: {
    modeHint(s) {
      if (s.ragOn) return { text: '📚 知识库增强模式', cls: 'hint-pink' }
      return { text: '💬 直接对话模式', cls: 'hint-muted' }
    },
  },
  actions: {
    toggleRag() { this.ragOn = !this.ragOn },
    abortInflight() {
      if (this.abort) { this.abort.abort(); this.abort = null }
      this.loading = false
    },
    stop() {
      this.abortInflight()
      apiFetch('/api/chat/cancel', { method: 'POST' }).catch(() => {})
    },
    handleEvent(ai, evt, data) {
      switch (evt) {
        case 'route': ai.mode = data.mode || 'chat'; break
        case 'memory': ai.memory = data.extracted_info || ''; break
        case 'sandbox_ready': ai.sandbox = data.workspace || ''; break
        case 'step': ai.steps.push({ type: data.type, content: data.content || '', params: data.params || null }); break
        case 'tool_call': ai.toolCall = { tool_name: data.tool_name, params: data.params, tool_result: data.tool_result }; break
        case 'rag_result': ai.ragResults = data.search_results || []; break
        case 'token': ai.answer += (data.content || ''); break
        case 'done':
          if (!ai.answer && data.answer) ai.answer = data.answer
          if (data.interrupted) ai.interrupted = true
          break
      }
    },
    async send(text) {
      const msg = (text || '').trim()
      if (!msg || this.loading) return
      const sess = useSessions()
      const docs = useDocs()
      if (!sess.currentId) sess.newSession()
      const sessionId = sess.currentId

      sess.addMessage(sessionId, { role: 'user', text: msg })
      const ai = sess.addMessage(sessionId, {
        role: 'ai', mode: 'chat', steps: [], memory: '', toolCall: null,
        ragResults: null, answer: '', interrupted: false, sandbox: '', streaming: true,
      })

      this.loading = true
      this.abort = new AbortController()
      const body = { message: msg, use_rag: this.ragOn, explicit: true }

      try {
        const resp = await apiFetch('/api/chat/stream', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
          signal: this.abort.signal,
        })
        if (!resp.ok) {
          // 流式不可用 → 回落同步
          const data = await apiFetch('/api/chat', {
            method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
          }).then(r => r.json())
          ai.mode = data.mode || 'chat'
          if (data.extracted_info) ai.memory = data.extracted_info
          if (data.steps) ai.steps = data.steps.map(s => ({ type: s.type, content: s.content, params: s.params }))
          if (data.tool_call && data.tool_call.tool_name) ai.toolCall = data.tool_call
          if (data.search_results) ai.ragResults = data.search_results
          ai.answer = data.answer || ''
          ai.interrupted = !!data.interrupted
        } else {
          await readSSE(resp, (evt, data) => this.handleEvent(ai, evt, data))
        }
        docs.loadLibrary()
      } catch (e) {
        if (e.name === 'AbortError') ai.answer = ai.answer || '🛑 已中断'
        else if (e.message !== 'unauthorized') ai.answer = '❌ 请求失败，请检查服务是否正常运行。'
      } finally {
        ai.streaming = false
        this.loading = false
        this.abort = null
        sess.save()
      }
    },
  },
})
