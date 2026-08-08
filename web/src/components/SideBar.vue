<template>
  <aside class="sidebar">
    <div class="sidebar-logo">
      <div class="logo-mark">◈</div>
      <div>
        <div class="logo-name">AGI-saber</div>
        <div class="logo-sub">智能协作 · 一站直达</div>
      </div>
    </div>

    <!-- 个人文件 -->
    <div class="sec-label">个人文件</div>
    <div class="blackhole">
      <div class="upload-zone" :class="{ drag: dragging }"
           @click="fileInput.click()"
           @dragover.prevent="dragging = true"
           @dragleave="dragging = false"
           @drop.prevent="onDrop">
        <div class="uz-icon">⬆</div>
        <div class="uz-text">上传个人文件</div>
        <div class="uz-sub">点击或拖拽到此处</div>
      </div>
      <input ref="fileInput" type="file" accept=".txt,.md,.pdf" multiple style="display:none" @change="onPick" />
      <div class="doc-list">
        <div v-if="!docs.uploaded.length" style="font-size:11px;color:var(--text3);padding:6px 0">暂无文档</div>
        <div v-for="d in docs.uploaded.slice(0, 6)" :key="d.name" class="doc-item">
          <span class="doc-icon">{{ d.needsOCR ? '!' : '✓' }}</span>
          <span class="doc-name" :title="docTitle(d)">{{ d.name }}</span>
          <span class="doc-chunks">{{ d.needsOCR ? '需 OCR' : `${d.chunks || 0} 块${d.indexed ? '/' + d.indexed : ''}` }}</span>
          <span v-if="d.docHash" class="doc-del" title="删除" @click="removeDoc(d)">×</span>
        </div>
      </div>
    </div>

    <!-- 本地文档库 -->
    <div class="sec-label sec-label-row">
      <span>本地文档库</span>
      <button class="mini-refresh" type="button" @click="docs.loadLibrary()">刷新</button>
    </div>
    <div class="blackhole library-panel">
      <div class="doc-list">
        <div v-if="!docs.library.length" class="doc-empty">暂无本地文档。让 Agent 生成报告并保存后，会出现在这里。</div>
        <div v-for="d in docs.library.slice(0, 8)" :key="d.id" class="doc-item" role="button" @click="docs.openViewer(d.id)">
          <span class="doc-icon">§</span>
          <span class="doc-name" :title="d.title || ''">{{ d.title || '未命名文档' }}</span>
          <span class="doc-chunks">v{{ d.latest_version || 0 }}</span>
          <span class="doc-ingest" title="重新入库 RAG" @click.stop="docs.ingest(d.id)">↻</span>
        </div>
      </div>
    </div>

    <!-- 近期对话 -->
    <div class="sec-label">近期对话</div>
    <div class="recent">
      <div v-if="!sessions.sessions.length" class="sess-empty">暂无对话记录</div>
      <div v-for="s in sessions.sessions" :key="s.id"
           class="session-item" :class="{ active: s.id === sessions.currentId }"
           @click="switchTo(s.id)">
        <span class="sess-dot">▹</span>
        <div class="sess-info">
          <div class="sess-title">{{ s.title }}</div>
          <div class="sess-meta">{{ fmtDate(s.ts) }}</div>
        </div>
        <div class="sess-del" title="删除" @click.stop="del(s.id)">✕</div>
      </div>
    </div>

    <button class="new-chat-btn" @click="newChat">＋ 新建对话</button>

    <div v-if="auth.loggedIn" class="user-bar" style="display:flex">
      <span class="uname">{{ auth.username }}</span>
      <button class="logout-btn" type="button" @click="logout">登出</button>
    </div>
  </aside>
</template>

<script setup>
import { ref } from 'vue'
import { useDocs } from '../stores/docs'
import { useSessions } from '../stores/sessions'
import { useAuth } from '../stores/auth'
import { useChat } from '../stores/chat'

const docs = useDocs()
const sessions = useSessions()
const auth = useAuth()
const chat = useChat()
const fileInput = ref(null)
const dragging = ref(false)

async function onPick(e) { for (const f of e.target.files) await docs.uploadFile(f); e.target.value = '' }
async function onDrop(e) { dragging.value = false; for (const f of e.dataTransfer.files) await docs.uploadFile(f) }
function docTitle(d) { return `${d.name}${d.pages ? ' · ' + d.pages + ' 页' : ''}${d.textChars ? ' · ' + d.textChars + ' 字' : ''}${d.parser ? ' · ' + d.parser : ''}` }
async function removeDoc(d) { if (confirm('确定删除「' + d.name + '」？')) await docs.deleteDoc(d.docHash) }
function fmtDate(ts) { return new Date(ts).toLocaleDateString('zh', { month: 'short', day: 'numeric' }) }
function switchTo(id) { chat.abortInflight(); sessions.switchSession(id) }
function del(id) { sessions.deleteSession(id) }
function newChat() { chat.abortInflight(); sessions.newSession() }
function logout() { chat.abortInflight(); auth.logout() }
</script>
