<template>
  <div class="auth-overlay show">
    <div class="auth-card">
      <div class="auth-title">AGI-saber</div>
      <div class="auth-sub">{{ auth.mode === 'login' ? '登录以使用专属记忆' : '创建一个新账号' }}</div>

      <div class="auth-tabs">
        <div class="auth-tab" :class="{ active: auth.mode === 'login' }" @click="auth.setMode('login')">登录</div>
        <div class="auth-tab" :class="{ active: auth.mode === 'register' }" @click="auth.setMode('register')">注册</div>
      </div>

      <div class="auth-field">
        <label>用户名</label>
        <input type="text" v-model="username" autocomplete="username" placeholder="3-32 字符" @keydown.enter="submit" />
      </div>
      <div class="auth-field">
        <label>密码</label>
        <input type="password" v-model="password" autocomplete="current-password" placeholder="至少 8 位" @keydown.enter="submit" />
      </div>

      <div class="auth-error" :class="{ show: !!auth.error }">{{ auth.error }}</div>

      <button class="auth-submit" :disabled="auth.submitting" @click="submit">
        {{ auth.submitting ? '处理中...' : (auth.mode === 'login' ? '登录' : '注册') }}
      </button>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useAuth } from '../stores/auth'

const auth = useAuth()
const username = ref('')
const password = ref('')

async function submit() {
  const ok = await auth.submit(username.value.trim(), password.value)
  if (ok) password.value = ''
}
</script>
