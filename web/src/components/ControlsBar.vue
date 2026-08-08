<template>
  <div class="controls-bar">
    <div class="rag-toggle-wrap" :class="{ on: chat.ragOn }" @click="chat.toggleRag()">
      <div class="toggle-pill"></div>
      <span class="toggle-label">知识库</span>
    </div>

    <div class="tools-btn-wrap">
      <div class="tools-btn" :class="{ active: skills.enabledCount > 0 }" @click="$emit('open-skills')">
        <span>🧩</span>
        <span>Skill 广场</span>
        <span class="tool-count" v-if="skills.enabledCount > 0">{{ skills.enabledCount }}</span>
      </div>
    </div>

    <div class="controls-right" :class="chat.modeHint.cls">{{ chat.modeHint.text }}</div>
  </div>
</template>

<script setup>
import { useChat } from '../stores/chat'
import { useSkills } from '../stores/skills'

defineEmits(['open-skills'])
const chat = useChat()
const skills = useSkills()
</script>

<style scoped>
.controls-right.hint-primary { color: var(--primary-d); }
.controls-right.hint-pink { color: var(--pink-d); }
.controls-right.hint-muted { color: var(--text3); }
</style>
