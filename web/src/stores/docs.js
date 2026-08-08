// stores/docs.js — 个人上传文件 + 本地文档库 + 文档查看器状态。
import { defineStore } from 'pinia'
import { apiFetch, fetchJSON } from '../api/client'

export const useDocs = defineStore('docs', {
  state: () => ({
    uploaded: [],
    library: [],
    viewer: { open: false, id: null, title: '', meta: '', content: '', ingesting: false },
  }),
  actions: {
    async uploadFile(file) {
      const form = new FormData()
      form.append('file', file)
      const res = await apiFetch('/api/upload', { method: 'POST', body: form }).then(r => r.json())
      const doc = {
        name: file.name, chunks: res.chunk_count, indexed: res.indexed_count || 0,
        parser: res.parser || '', pages: res.pages || 0, textChars: res.text_chars || 0,
        needsOCR: !!res.needs_ocr, docHash: res.doc_hash, ts: Date.now(),
      }
      this.uploaded = this.uploaded.filter(d => d.name !== file.name)
      this.uploaded.unshift(doc)
    },
    async deleteDoc(docHash) {
      const res = await apiFetch('/api/docs/delete', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ doc_hash: docHash }),
      })
      if (res.ok) this.uploaded = this.uploaded.filter(d => d.docHash !== docHash)
    },
    async loadLibrary() {
      try {
        const res = await fetchJSON('/api/documents')
        this.library = res.documents || []
      } catch { this.library = [] }
    },
    async openViewer(id) {
      try {
        const res = await fetchJSON('/api/documents/' + encodeURIComponent(id))
        const doc = res.document || {}, ver = res.version || {}
        const version = doc.latest_version || ver.version || 0
        this.viewer = {
          open: true, id: doc.id || null, title: doc.title || '本地文档',
          meta: `v${version} · ${doc.doc_type || 'document'} · ${doc.source || 'local'}`,
          content: ver.content_md || '（空文档）', ingesting: false,
        }
      } catch (e) {
        this.viewer = { open: true, id: null, title: '读取文档失败', meta: '', content: e.message, ingesting: false }
      }
    },
    closeViewer() { this.viewer.open = false; this.viewer.id = null },
    async ingest(id) {
      const res = await fetchJSON('/api/documents/' + encodeURIComponent(id) + '/ingest', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}',
      })
      this.loadLibrary()
      return res
    },
    async ingestViewer() {
      if (!this.viewer.id) return
      this.viewer.ingesting = true
      try {
        const res = await this.ingest(this.viewer.id)
        this.viewer.meta += ` · 已入库 ${res.indexed_count || 0}/${res.chunk_count || 0}`
      } catch (e) {
        this.viewer.meta += ` · 入库失败：${e.message}`
      } finally { this.viewer.ingesting = false }
    },
    reset() { this.uploaded = []; this.library = [] },
  },
})
