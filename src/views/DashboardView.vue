<script setup>
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useController } from '../stores/controller'
import Icon from '../components/Icon.vue'
import Sparkline from '../components/Sparkline.vue'

// LIVE overview — wired to the real controller (no mock data).
const ctl = useController()
const router = useRouter()

const tps = computed(() => ctl.activity?.tokens_per_sec ?? 0)
const jobs = computed(() => ctl.activity?.active_jobs ?? 0)
const history = computed(() => ctl.activity?.history ?? [])
const load1 = computed(() => ctl.system?.load1 ?? 0)

// Installable components and their icons.
const COMPONENTS = {
  controller: { icon: 'cpu', color: 'var(--brand)', label: 'Controller' },
  'ai-controller': { icon: 'sparkles', color: 'var(--ai)', label: 'AI Controller' }
}

// Every machine in the mesh: the controller itself + adopted nodes, each with
// live metrics and the components running on it.
const nodeList = computed(() => {
  const out = []
  const s = ctl.system
  if (s) {
    const comps = ['controller']
    if (ctl.aiInstalled) comps.push('ai-controller')
    out.push({
      id: 'self', name: s.hostname || 'controller', self: true, online: true, components: comps,
      m: { cpu: s.cpu_percent, memU: s.mem_used_gb, memT: s.mem_total_gb, diskU: s.disk_used_gb, diskT: s.disk_total_gb, cores: s.cores }
    })
  }
  for (const n of ctl.nodes) {
    const comps = []
    if (n.role === 'inference') comps.push('ai-controller')
    const mm = n.metrics || {}
    out.push({
      id: n.id, name: n.name, self: false, online: n.state === 'online', pending: n.state === 'pending',
      secured: n.secured, components: comps,
      m: { cpu: mm.cpu_percent || 0, memU: mm.mem_used_gb || 0, memT: mm.mem_total_gb || 0, diskU: mm.disk_used_gb || 0, diskT: mm.disk_total_gb || 0, cores: n.specs?.cpu_cores }
    })
  }
  return out
})
function pct(u, t) { return t ? Math.round((u / t) * 100) : 0 }
function clampPct(v) { return Math.min(100, Math.max(0, Math.round(v || 0))) }
function compactGB(u, t) { return `${Math.round(u || 0)}/${Math.round(t || 0)}G` }

const online = computed(() => ctl.nodes.filter((n) => n.state === 'online'))
const poolCores = computed(() => online.value.reduce((a, n) => a + (n.specs?.cpu_cores || 0), 0))
const sys = computed(() => ctl.system)

// The controller is itself a machine in the deployment — count it.
const ctlIsMachine = computed(() => (ctl.connected ? 1 : 0))
const machineCount = computed(() => ctl.nodes.length + ctlIsMachine.value)
const onlineMachines = computed(() => online.value.length + ctlIsMachine.value)
const poolCoresTotal = computed(() => poolCores.value + (ctl.system?.cores || 0))
const poolGpuTotal = computed(() => (ctl.poolGpuGB || 0) + (ctl.system?.gpu_vram_gb || 0))

function memPct(m) { return m && m.mem_total_gb ? Math.round((m.mem_used_gb / m.mem_total_gb) * 100) : 0 }
function diskPct(m) { return m && m.disk_total_gb ? Math.round((m.disk_used_gb / m.disk_total_gb) * 100) : 0 }
function fmtUptime(sec) {
  if (!sec) return '—'
  const d = Math.floor(sec / 86400), h = Math.floor((sec % 86400) / 3600)
  return d ? `${d}d ${h}h` : `${h}h`
}
function specLine(s) {
  if (!s) return ''
  let p = (s.cpu_cores || 0) + ' cores'
  if (s.gpu_vram_gb) p += ' · ' + s.gpu_vram_gb + ' GB GPU'
  return p
}
</script>

<template>
  <div class="page">
    <div class="page-head between">
      <div>
        <h2 class="page-title">Overview</h2>
        <p class="muted" style="margin-top:5px">
          {{ ctl.apiBase ? 'Controller at ' : 'This controller' }}<span class="mono">{{ ctl.apiBase || '' }}</span>
        </p>
      </div>
      <span class="pill" :class="ctl.connected ? 'ok' : 'crit'">
        <span class="dot pulse" :class="ctl.connected ? 'ok' : 'crit'"/>
        {{ ctl.connected ? 'Controller online' : 'Controller offline' }}
      </span>
    </div>

    <!-- offline -->
    <div v-if="!ctl.connected && ctl.loaded" class="card card-pad offline">
      <Icon name="alert" :size="18" style="color:var(--crit)"/>
      <div>
        <div style="font-weight:650;color:var(--tx-0)">Can't reach the controller{{ ctl.error ? ' (' + ctl.error + ')' : '' }}</div>
        <div class="muted" style="font-size:12.5px;margin-top:3px">Start it and this fills in automatically.</div>
      </div>
    </div>

    <template v-if="ctl.connected">
      <!-- pending adoption nudge -->
      <div v-if="ctl.pending.length" class="card card-pad nudge" role="button" tabindex="0"
        @click="router.push('/hardware')" @keydown.enter.prevent="router.push('/hardware')" @keydown.space.prevent="router.push('/hardware')">
        <span class="ndot"><span class="dot brand pulse"/></span>
        <div style="flex:1">
          <b style="color:var(--tx-0)">{{ ctl.pending.length }} machine{{ ctl.pending.length>1?'s':'' }} waiting to be adopted.</b>
          <span class="muted"> Review and bring {{ ctl.pending.length>1?'them':'it' }} online.</span>
        </div>
        <button class="btn btn-primary btn-sm" @click.stop="router.push('/hardware')">Adopt <Icon name="chevron" :size="13"/></button>
      </div>

      <!-- KPI tiles -->
      <div class="grid kpis">
        <div class="card card-pad"><span class="eyebrow">Machines</span><div class="stat-num">{{ machineCount }}</div><span class="muted" style="font-size:12px">{{ onlineMachines }} online · incl. controller</span></div>
        <div class="card card-pad"><span class="eyebrow">Pool CPU</span><div class="stat-num">{{ poolCoresTotal }}<span class="u">cores</span></div><span class="muted" style="font-size:12px">{{ Math.round(sys?.cpu_percent || 0) }}% busy now</span></div>
        <div class="card card-pad"><span class="eyebrow">Pool GPU</span><div class="stat-num">{{ poolGpuTotal }}<span class="u">GB</span></div><span class="muted" style="font-size:12px">{{ sys?.os === 'darwin' ? 'unified memory' : 'VRAM available' }}</span></div>
        <div class="card card-pad"><span class="eyebrow">Security</span>
          <div class="row gap-2" style="margin:6px 0 2px"><Icon name="shield" :size="18" style="color:var(--ok)"/><span class="stat-num" style="font-size:20px;color:var(--ok)">mTLS</span></div>
          <span class="muted" style="font-size:12px">CA active · {{ ctl.securedCount }}/{{ ctl.nodes.length }} nodes encrypted</span></div>
      </div>

      <!-- live inference activity -->
      <div class="section-title"><h2>Inference activity</h2><span class="rule"/>
        <span class="faint" style="font-size:11px">live · measured from generations</span>
      </div>
      <div class="card card-pad actcard">
        <div class="between" style="margin-bottom:6px">
          <div class="col">
            <span class="eyebrow">Tokens / sec</span>
            <div class="row" style="align-items:baseline;gap:7px;margin-top:5px">
              <span class="stat-num" style="font-size:32px;color:var(--ai)">{{ Math.round(tps).toLocaleString() }}</span>
              <span class="muted">tok/s</span>
            </div>
          </div>
          <div class="row gap-4">
            <div class="actstat"><span class="stat-num" style="font-size:22px">{{ jobs }}</span><span class="muted">running jobs</span></div>
            <div class="actstat"><span class="stat-num" style="font-size:22px">{{ load1 }}</span><span class="muted">load (1m)</span></div>
          </div>
        </div>
        <Sparkline :data="history" color="var(--ai)" :height="64" suffix="tok/s" />
      </div>

      <!-- nodes (the controller itself + adopted machines) -->
      <div class="section-title"><h2>Nodes</h2><span class="rule"/>
        <span class="faint" style="font-size:11px">{{ nodeList.length }} machine{{ nodeList.length === 1 ? '' : 's' }}</span>
      </div>
      <div class="nodecards">
        <div v-for="n in nodeList" :key="n.id" class="card card-pad nodecard" :class="{self: n.self, pending: n.pending}">
          <div class="nhead">
            <span class="dot" :class="n.pending ? 'brand pulse' : (n.online ? 'ok' : 'idle')" :style="n.online ? 'animation:pulse 2s infinite' : ''"/>
            <span class="nname mono" :title="n.name">{{ n.name }}</span>
            <span class="nicons">
              <span v-for="c in n.components" :key="c" class="cicon" :style="{'--cc': COMPONENTS[c].color}" :title="COMPONENTS[c].label">
                <Icon :name="COMPONENTS[c].icon" :size="15"/>
              </span>
            </span>
          </div>
          <div class="nmeta">
            <span v-if="!n.pending" class="faint" style="font-size:11px">{{ n.m.cores || '?' }} cores · {{ n.online ? 'online' : 'offline' }}</span>
            <span v-if="n.self" class="pill brand" style="height:18px">this node</span>
            <span v-if="n.secured" class="pill ai" style="height:18px"><Icon name="shield" :size="10"/> mTLS</span>
            <span v-if="!n.pending && !n.self && !n.components.length" class="faint" style="font-size:11px">worker</span>
          </div>
          <RouterLink v-if="n.pending" to="/hardware" class="pendlink">
            <span class="pill brand" style="height:20px">waiting for adoption</span>
            <span class="faint" style="font-size:11px">adopt on the Hardware page →</span>
          </RouterLink>
          <div v-if="!n.pending" class="bars3">
            <div class="vbar" role="meter" :aria-valuenow="clampPct(n.m.cpu)" aria-valuemin="0" aria-valuemax="100" :aria-label="'CPU usage on ' + n.name">
              <div class="vtrack"><div class="vfill" :style="{height: clampPct(n.m.cpu)+'%', background:'var(--brand)'}"/></div>
              <span class="vpct mono">{{ Math.round(n.m.cpu) }}%</span>
              <span class="vlbl">CPU</span>
              <span class="vdet">{{ n.m.cores || '?' }} cores</span>
            </div>
            <div class="vbar" role="meter" :aria-valuenow="pct(n.m.memU, n.m.memT)" aria-valuemin="0" aria-valuemax="100" :aria-label="'Memory usage on ' + n.name">
              <div class="vtrack"><div class="vfill" :style="{height: pct(n.m.memU, n.m.memT)+'%', background:'var(--ok)'}"/></div>
              <span class="vpct mono">{{ pct(n.m.memU, n.m.memT) }}%</span>
              <span class="vlbl">Memory</span>
              <span class="vdet">{{ compactGB(n.m.memU, n.m.memT) }}</span>
            </div>
            <div class="vbar" role="meter" :aria-valuenow="pct(n.m.diskU, n.m.diskT)" aria-valuemin="0" aria-valuemax="100" :aria-label="'Storage usage on ' + n.name">
              <div class="vtrack"><div class="vfill" :style="{height: pct(n.m.diskU, n.m.diskT)+'%', background:'var(--ai)'}"/></div>
              <span class="vpct mono">{{ pct(n.m.diskU, n.m.diskT) }}%</span>
              <span class="vlbl">Storage</span>
              <span class="vdet">{{ compactGB(n.m.diskU, n.m.diskT) }}</span>
            </div>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.u { font-size: 13px; color: var(--tx-3); font-weight: 500; margin-left: 3px; }
.offline { display: flex; align-items: center; gap: 14px; border-color: rgba(251,113,133,.3); background: linear-gradient(90deg, rgba(251,113,133,.07), var(--bg-1)); }
.nudge { display: flex; align-items: center; gap: 13px; cursor: pointer; border-color: rgba(56,189,248,.28); background: linear-gradient(90deg, rgba(56,189,248,.07), var(--bg-1)); margin-bottom: 18px; }
.nudge:hover { border-color: var(--brand); }
.ndot { width: 26px; display: grid; place-items: center; }

.kpis { grid-template-columns: repeat(4,1fr); margin-bottom: 8px; }
.kpis .stat-num { margin: 6px 0 2px; }

.actcard { background: linear-gradient(120deg, rgba(167,139,250,.06), var(--bg-1) 60%); border-color: rgba(167,139,250,.2); }
.actstat { display: flex; flex-direction: column; align-items: flex-end; }
.actstat .muted { font-size: 11px; }

.nodecards { display: grid; grid-template-columns: repeat(auto-fill, minmax(240px, 1fr)); gap: 12px; align-items: start; }
.nhead { display: flex; align-items: center; gap: 8px; min-width: 0; }
.nhead .nname { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.nicons { display: flex; gap: 6px; flex: none; }
.nmeta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin: 6px 0 2px; }
.nodecard.self { border-color: rgba(56,189,248,.28); background: linear-gradient(120deg, rgba(56,189,248,.05), var(--bg-1) 55%); }
.nname { font-size: 14px; font-weight: 650; color: var(--tx-0); }
.cicon { width: 30px; height: 30px; border-radius: 9px; display: grid; place-items: center; color: var(--cc);
  background: color-mix(in srgb, var(--cc) 14%, #0c1016); border: 1px solid color-mix(in srgb, var(--cc) 30%, transparent); }
.bars3 { display: flex; gap: 8px; padding-top: 14px; border-top: 1px solid var(--line); }
.vbar { flex: 1; min-width: 0; display: flex; flex-direction: column; align-items: center; gap: 5px; }
.vtrack { width: 16px; height: 60px; border-radius: 8px; background: var(--bg-inset); border: 1px solid var(--line); position: relative; overflow: hidden; }
.vfill { position: absolute; bottom: 0; left: 0; right: 0; transition: height .6s cubic-bezier(.2,.7,.3,1); }
.vpct { font-size: 13px; font-weight: 700; color: var(--tx-0); }
.vlbl { font-size: 10.5px; color: var(--tx-2); text-transform: uppercase; letter-spacing: .04em; }
.vdet { font-size: 10.5px; color: var(--tx-3); white-space: nowrap; }

.nodecard.pending { border-style: dashed; border-color: rgba(56,189,248,.4); }
.pendlink { display: flex; align-items: center; gap: 8px; margin-top: 10px; padding-top: 12px; border-top: 1px dashed var(--line); }
.pendlink:hover .faint { color: var(--brand); }

@media (max-width: 920px) { .kpis { grid-template-columns: repeat(2,1fr); } }
@media (max-width: 640px) { .kpis { grid-template-columns: 1fr; } }
</style>
