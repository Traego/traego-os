<script setup>
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useController } from '../stores/controller'
import { useUI } from '../stores/ui'
import Icon from './Icon.vue'

const ctl = useController()
const ui = useUI()
const route = useRoute()
const title = computed(() => route.meta.title || 'Traego')

const keyState = computed(() => {
  if (!ctl.adminKey) return { label: 'set admin key', cls: 'crit' }
  if (ctl.unauthorized) return { label: 'admin key rejected', cls: 'crit' }
  return { label: 'admin key set', cls: 'ok' }
})

function editAdminKey() {
  const v = window.prompt('Admin key (TRAEGO_ADMIN_KEY of the controller):', '')
  if (v === null) return
  ctl.setAdminKey(v.trim())
  ctl.refresh()
}
</script>

<template>
  <header class="topbar">
    <div class="row gap-3">
      <button class="menu-btn" @click="ui.toggleSidebar()" aria-label="Toggle navigation">
        <Icon name="menu" :size="18"/>
      </button>
      <h1 style="font-size:18px">{{ title }}</h1>
      <span class="pill" :class="ctl.connected ? 'ok' : 'crit'" :title="ctl.connected ? 'controller online' : 'controller offline'">
        <span class="dot pulse" :class="ctl.connected ? 'ok' : 'crit'" />
        <span class="pill-label">{{ ctl.connected ? 'controller online' : 'controller offline' }}</span>
      </span>
    </div>

    <div class="row gap-3">
      <button class="pill keybtn" :class="keyState.cls" @click="editAdminKey" :title="'Click to change the admin key'">
        <span class="dot" :class="keyState.cls" />
        <span class="pill-label">{{ keyState.label }}</span>
      </button>
      <a href="#/chat" class="chatlink" target="_blank">Open chat ↗</a>
      <div class="avatar">P</div>
    </div>
  </header>
</template>

<style scoped>
.topbar {
  height: var(--topbar-h); flex: none;
  display: flex; align-items: center; justify-content: space-between;
  padding: 0 26px;
  border-bottom: 1px solid var(--line);
  background: rgba(10,13,19,0.72);
  backdrop-filter: blur(14px);
  position: sticky; top: 0; z-index: 20;
}
.menu-btn { display: none; align-items: center; justify-content: center; width: 38px; height: 38px;
  border-radius: var(--radius-sm); border: 1px solid var(--line); background: transparent; color: var(--tx-1); }
@media (max-width: 880px) { .menu-btn { display: inline-flex; } .topbar { padding: 0 16px; } }
@media (max-width: 560px) { .pill-label { display: none; } }
.keybtn { cursor: pointer; border: 1px solid var(--line); background: transparent; font: inherit; }
.chatlink { font-size: 12.5px; color: var(--tx-2); padding: 10px 6px; }
.chatlink:hover { color: var(--tx-0); }
.avatar { width: 34px; height: 34px; border-radius: 50%; display: grid; place-items: center; font-weight: 650; font-size: 13px; color: #04121c; background: linear-gradient(150deg, #e2e8f0, #94a3b8); }
</style>
