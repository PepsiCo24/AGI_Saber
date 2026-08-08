<template>
  <div class="messages" ref="scroller">
    <div v-if="!sessions.messages.length" class="welcome">
      <div class="welcome-icon">◈</div>
      <div class="welcome-title">AGI-saber 为你服务</div>
      <div class="welcome-desc">开启知识库可检索你上传的文件；选择工具后将自动推理并调用它们完成任务。</div>
    </div>
    <MessageBubble v-for="(m, i) in sessions.messages" :key="i" :msg="m" />
  </div>
</template>

<script setup>
import { ref, watch, nextTick } from 'vue'
import { useSessions } from '../stores/sessions'
import MessageBubble from './MessageBubble.vue'

const sessions = useSessions()
const scroller = ref(null)

// 消息变化（含流式增量）时滚到底
watch(() => JSON.stringify(sessions.messages), async () => {
  await nextTick()
  if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight
}, { flush: 'post' })
</script>
