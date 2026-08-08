// stores/skills.js — Skill 广场（已安装 + marketplace）。
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

export const useSkills = defineStore('skills', {
  state: () => ({
    installed: [],
    featured: [],
    github: [],
    hubStatus: '',
    loadingMarket: false,
  }),
  getters: {
    enabledCount: (s) => s.installed.filter(x => x.enabled).length,
  },
  actions: {
    async loadInstalled() {
      try {
        const data = await apiFetch('/api/skills/installed').then(r => r.json())
        this.installed = (data && data.skills) || []
      } catch { this.installed = [] }
    },
    async loadMarketplace() {
      this.loadingMarket = true
      try {
        const data = await apiFetch('/api/skills/marketplace').then(r => r.json())
        this.featured = data.featured || []
        this.github = data.hub_status === 'ok' ? (data.github || []) : []
        this.hubStatus = data.hub_status || ''
      } catch {
        this.featured = []; this.github = []; this.hubStatus = 'error'
      } finally { this.loadingMarket = false }
    },
    async install(skillId) {
      const res = await apiFetch('/api/skills/install', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ skill_id: skillId }),
      }).then(r => r.json())
      if (res.ok) await this.loadInstalled()
      return res.ok
    },
    async uninstall(skillId) {
      await apiFetch('/api/skills/uninstall', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ skill_id: skillId }),
      })
      await this.loadInstalled()
    },
    async toggle(skillId, enabled) {
      await apiFetch('/api/skills/toggle', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ skill_id: skillId, enabled }),
      })
      await this.loadInstalled()
    },
    reset() { this.installed = []; this.featured = []; this.github = []; this.hubStatus = '' },
  },
})
