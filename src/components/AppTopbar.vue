<script setup>
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { useController } from '../stores/controller'

const ctl = useController()
const route = useRoute()
const title = computed(() => route.meta.title || 'Traego')
</script>

<template>
  <header class="topbar">
    <div class="row gap-3">
      <h1 style="font-size:18px">{{ title }}</h1>
      <span class="pill" :class="ctl.connected ? 'ok' : 'crit'">
        <span class="dot pulse" :class="ctl.connected ? 'ok' : 'crit'" />
        {{ ctl.connected ? 'controller online' : 'controller offline' }}
      </span>
    </div>

    <div class="row gap-3">
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
.chatlink { font-size: 12.5px; color: var(--tx-2); }
.chatlink:hover { color: var(--tx-0); }
.avatar { width: 34px; height: 34px; border-radius: 50%; display: grid; place-items: center; font-weight: 650; font-size: 13px; color: #04121c; background: linear-gradient(150deg, #e2e8f0, #94a3b8); }
</style>
