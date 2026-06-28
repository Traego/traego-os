<script setup>
import { computed } from 'vue'
import { useController } from '../stores/controller'
import Icon from './Icon.vue'
import Sparkline from './Sparkline.vue'

const ctl = useController()

const nav = [
  { to: '/', icon: 'dashboard', label: 'Dashboard' },
  { to: '/hardware', icon: 'server', label: 'Hardware' },
  { to: '/ai', icon: 'sparkles', label: 'AI' }
]

const tps = computed(() => Math.round(ctl.activity?.tokens_per_sec ?? 0))
const history = computed(() => ctl.activity?.history ?? [])
</script>

<template>
  <aside class="sb">
    <div class="brand">
      <div class="logo"><Icon name="bolt" :size="17" /></div>
      <div class="col" style="line-height:1.1">
        <span class="bname">traego</span>
        <span class="bsub mono">control plane</span>
      </div>
    </div>

    <nav class="nav">
      <RouterLink v-for="i in nav" :key="i.to" :to="i.to" class="navi" active-class="on" :class="{exact: i.to==='/'}">
        <Icon :name="i.icon" :size="17" /><span>{{ i.label }}</span>
      </RouterLink>
    </nav>

    <div class="sb-foot">
      <!-- live token usage chart -->
      <div class="token-card">
        <div class="between" style="margin-bottom:8px">
          <span class="eyebrow">Token usage</span>
          <span class="mono tval">{{ tps.toLocaleString() }} <span class="faint">tok/s</span></span>
        </div>
        <Sparkline :data="history" color="var(--ai)" :height="44" suffix="tok/s" />
      </div>

      <div class="mesh-card">
        <div class="between" style="margin-bottom:8px">
          <span class="eyebrow">Mesh</span>
          <span class="dot" :class="ctl.connected ? 'ok' : 'crit'" />
        </div>
        <div class="row gap-2">
          <span class="mono" style="font-size:12px;color:var(--tx-1)">
            {{ ctl.connected ? (ctl.nodes.length + 1) + ' machine' + (ctl.nodes.length ? 's' : '') : 'offline' }}
          </span>
          <span class="faint" style="font-size:11px">· {{ ctl.connected ? 'controller online' : 'no controller' }}</span>
        </div>
      </div>
    </div>
  </aside>
</template>

<style scoped>
.sb {
  position: fixed; top: 0; left: 0; bottom: 0;
  width: var(--sidebar-w);
  overflow: hidden;
  background: linear-gradient(180deg, #0c1019, #090c12);
  border-right: 1px solid var(--line);
  display: flex; flex-direction: column;
  padding: 16px 12px;
}
.brand { display: flex; align-items: center; gap: 11px; padding: 6px 8px 18px; }
.logo {
  width: 34px; height: 34px; border-radius: 10px; display: grid; place-items: center;
  color: #04121c; background: linear-gradient(150deg, var(--brand), var(--brand-2));
  box-shadow: 0 6px 18px -6px var(--brand-glow);
}
.bname { font-weight: 750; letter-spacing: -0.02em; font-size: 17px; color: var(--tx-0); }
.bsub { font-size: 10.5px; color: var(--tx-3); }

.nav { display: flex; flex-direction: column; gap: 2px; flex: 1; min-height: 0; }
.navi {
  display: flex; align-items: center; gap: 11px;
  padding: 9px 11px; border-radius: 9px;
  color: var(--tx-2); font-size: 13.5px; font-weight: 530;
  transition: all .12s ease; position: relative;
}
.navi:hover { background: rgba(255,255,255,0.04); color: var(--tx-0); }
.navi.on { background: linear-gradient(90deg, rgba(56,189,248,0.16), rgba(56,189,248,0.04)); color: #fff; }
.navi.on::before { content:''; position:absolute; left:-12px; top:8px; bottom:8px; width:3px; border-radius:0 3px 3px 0; background: var(--brand); box-shadow: 0 0 10px var(--brand-glow); }

.sb-foot { display: flex; flex-direction: column; gap: 10px; padding-top: 12px; flex: none; }
.token-card, .mesh-card { background: var(--bg-1); border: 1px solid var(--line); border-radius: 12px; padding: 12px; }
.token-card { background: linear-gradient(180deg, rgba(167,139,250,0.06), var(--bg-1)); border-color: rgba(167,139,250,0.18); }
.tval { font-size: 12px; color: var(--ai); font-weight: 600; }
</style>
