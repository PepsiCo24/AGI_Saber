<template>
  <div class="input-area">
    <div class="input-row">
      <textarea class="input-box" v-model="text" ref="ta" rows="1"
        placeholder="输入消息，Enter 发送，Shift+Enter 换行…"
        @keydown="onKey" @input="resize"></textarea>
      <button class="send-btn" :class="{ stop: chat.loading }" @click="onSend">{{ chat.loading ? '■' : '➤' }}</button>
    </div>
    <div class="input-hint">
      <span class="hint-chip" @click="quick('你是谁？')">你是谁</span>
      <span class="hint-chip" @click="quick('现在几点？')">现在几点</span>
      <span class="hint-chip" @click="quick('北京天气怎么样？')">北京天气</span>
      <span class="hint-chip" @click="quick('我喜欢周杰伦的音乐')">记住偏好</span>
      <span class="hint-chip" @click="quick('写一份关于人工智能发展趋势的报告')">生成报告</span>
    </div>
  </div>
</template>

<script setup>
import { ref, nextTick } from 'vue'
import { useChat } from '../stores/chat'

const chat = useChat()
const text = ref('')
const ta = ref(null)

function resize() {
  const el = ta.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = Math.min(el.scrollHeight, 140) + 'px'
}
function onKey(e) {
  if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); onSend() }
}
async function onSend() {
  if (chat.loading) { chat.stop(); return }
  const msg = text.value.trim()
  if (!msg) return
  text.value = ''
  await nextTick(); resize()
  chat.send(msg)
}
async function quick(m) { text.value = m; onSend() }
</script>
