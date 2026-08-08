<template>
  <div class="msg-row" :class="msg.role">
    <div class="avatar" :class="msg.role">{{ msg.role === 'user' ? '◇' : '◈' }}</div>
    <div class="msg-body">
      <div class="msg-name">{{ msg.role === 'user' ? '你' : 'AGI-saber' }}</div>

      <!-- 用户消息：纯文本 -->
      <div v-if="msg.role === 'user'" class="bubble user">{{ msg.text }}</div>

      <!-- 兼容旧格式（存的是 html 串） -->
      <div v-else-if="msg.html" class="bubble ai" v-html="msg.html"></div>

      <!-- AI 消息：结构化渲染 -->
      <div v-else class="bubble ai">
        <div v-if="msg.memory" class="memory-note">
          <span class="memory-tag">🧠 记忆</span>
          <span class="memory-content">{{ msg.memory }}</span>
        </div>

        <ThinkPanel :msg="msg" />

        <div v-if="msg.interrupted" class="interrupted-badge">🛑 已中断</div>

        <div v-if="!msg.answer && msg.streaming" class="typing"><span></span><span></span><span></span></div>
        <div v-else class="answer-text" v-html="rendered"></div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue'
import { renderMarkdown } from '../utils/markdown'
import ThinkPanel from './ThinkPanel.vue'

const props = defineProps({ msg: { type: Object, required: true } })
const rendered = computed(() => renderMarkdown(props.msg.answer || ''))
</script>
