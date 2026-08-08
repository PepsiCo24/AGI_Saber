<template>
  <div class="doc-viewer-backdrop open" @click.self="$emit('close')">
    <section class="doc-viewer" role="dialog" aria-modal="true">
      <div class="doc-viewer-head">
        <div class="doc-viewer-title-wrap">
          <div class="doc-viewer-kicker">Skill 广场</div>
          <div class="doc-viewer-title">办公技能 · 安装并开启后主循环自动调度</div>
          <div class="doc-viewer-meta"></div>
        </div>
        <div class="doc-viewer-actions">
          <button class="doc-viewer-close" type="button" aria-label="关闭" @click="$emit('close')">×</button>
        </div>
      </div>
      <div class="doc-viewer-body">
        <div class="skill-section-title">已安装（开关控制是否参与主循环）</div>
        <div class="skill-grid">
          <div v-if="!skills.installed.length" class="s-meta">还没有安装任何 skill，去下面的广场安装吧。</div>
          <div v-for="s in skills.installed" :key="s.id" class="skill-card">
            <div class="s-name">{{ s.name }}</div>
            <div class="s-desc">{{ s.description || '' }}</div>
            <div class="s-foot">
              <label class="skill-switch">
                <input type="checkbox" :checked="s.enabled" @change="skills.toggle(s.id, $event.target.checked)" />
                <span class="track"></span>
              </label>
              <button class="s-btn secondary" @click="skills.uninstall(s.id)">卸载</button>
            </div>
          </div>
        </div>

        <div class="picker-divider"></div>
        <div class="skill-section-title">官方精选</div>
        <div class="skill-grid">
          <div v-if="skills.loadingMarket" class="s-meta">加载中…</div>
          <div v-for="m in skills.featured" :key="m.id" class="skill-card">
            <div class="s-name">{{ m.name }}</div>
            <div class="s-desc">{{ m.description || '' }}</div>
            <div class="s-foot">
              <span class="s-meta">官方内置</span>
              <button class="s-btn" @click="install(m.id, $event)">安装</button>
            </div>
          </div>
        </div>

        <div class="picker-divider"></div>
        <div class="skill-section-title">GitHub 热门（按 star 排序 Top 20）</div>
        <div class="skill-grid">
          <template v-if="skills.hubStatus === 'ok'">
            <div v-for="m in skills.github" :key="m.id" class="skill-card">
              <div class="s-name">{{ m.name }}</div>
              <div class="s-desc">{{ m.description || '' }}</div>
              <div class="s-foot">
                <span class="s-meta">★ {{ m.stars || 0 }} · <a :href="m.source_url" target="_blank" rel="noopener">GitHub</a></span>
                <button class="s-btn" @click="install(m.id, $event)">安装</button>
              </div>
            </div>
          </template>
          <div v-else-if="skills.hubStatus === 'disabled'" class="s-meta">GitHub 广场已关闭（可在 config.skillhub.enabled 开启）</div>
          <div v-else class="s-meta">GitHub 热门暂不可用（限流或网络问题），稍后重试</div>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup>
import { onMounted } from 'vue'
import { useSkills } from '../stores/skills'

defineEmits(['close'])
const skills = useSkills()

async function install(id, e) {
  const btn = e.target
  btn.disabled = true; btn.textContent = '安装中…'
  const ok = await skills.install(id)
  if (ok) btn.textContent = '已安装'
  else { btn.disabled = false; btn.textContent = '安装'; alert('安装失败') }
}

onMounted(() => { skills.loadInstalled(); skills.loadMarketplace() })
</script>
