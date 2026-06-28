<script setup>
import { computed } from 'vue'
import { useController } from '../stores/controller'
import Icon from '../components/Icon.vue'

// LIVE mesh view: sites → machines → components, and the controller install
// lifecycle. Multi-site aware from day one (one local site for now).
const ctl = useController()

const COMPONENTS = {
  controller: { label: 'Controller', icon: 'cpu', color: '#38bdf8', desc: 'Control plane' },
  'ai-controller': { label: 'AI Controller', icon: 'sparkles', color: '#a78bfa', desc: 'Local inference' }
}

// Compose the fleet of machines from live controller data.
const machines = computed(() => {
  const list = []
  if (ctl.connected && ctl.system) {
    const comps = [{ type: 'controller', role: 'primary', status: 'running' }]
    if (ctl.backendHealthy) comps.push({ type: 'ai-controller', status: 'running' })
    list.push({
      id: 'self', name: ctl.system.hostname || 'controller', self: true,
      cores: ctl.system.cores, memGB: ctl.system.mem_total_gb,
      cpu: ctl.system.cpu_percent, online: true, components: comps
    })
  }
  for (const n of ctl.nodes) {
    const comps = []
    if (n.role === 'inference') comps.push({ type: 'ai-controller', status: n.state })
    list.push({
      id: n.id, name: n.name, cores: n.specs?.cpu_cores, memGB: n.specs?.memory_gb,
      gpu: n.specs?.gpu_vram_gb, online: n.state === 'online', secured: n.secured, components: comps
    })
  }
  return list
})

const controllerCount = computed(() => machines.value.filter(m => m.components.some(c => c.type === 'controller')).length)
const haProtected = computed(() => controllerCount.value >= 2)
// machines that could host a replica controller (have none, and aren't the primary)
const replicaCandidates = computed(() => machines.value.filter(m => m.online && !m.components.some(c => c.type === 'controller')))

function compMeta(t) { return COMPONENTS[t] || { label: t, icon: 'grid', color: '#64748b' } }
</script>

<template>
  <div class="page">
    <div class="page-head between">
      <div>
        <h1><Icon name="server" :size="22" style="color:var(--brand)"/> Hardware</h1>
        <p class="muted" style="margin-top:5px">The machines in your mesh and the components running on each.</p>
      </div>
      <div class="row gap-2">
        <span class="pill" :class="ctl.connected ? 'ok' : 'crit'"><span class="dot" :class="ctl.connected?'ok':'crit'"/>{{ ctl.connected ? 'mesh online' : 'offline' }}</span>
        <span class="pill" :class="haProtected ? 'ok' : 'warn'">
          <Icon name="shield" :size="12"/> {{ haProtected ? 'HA protected' : 'single controller' }}
        </span>
      </div>
    </div>

    <div v-if="!ctl.connected" class="card card-pad offline">
      <Icon name="alert" :size="18" style="color:var(--crit)"/>
      <div><div style="font-weight:650;color:var(--tx-0)">No controller reachable</div>
        <div class="muted" style="font-size:12.5px;margin-top:3px">A mesh needs a controller on its first node. Install <span class="mono">traegod</span> and bootstrap a controller to begin.</div></div>
    </div>

    <template v-else>
      <!-- lifecycle guidance -->
      <div class="card card-pad lifecycle" :class="haProtected ? 'ok' : 'warn'">
        <span class="lico"><Icon :name="haProtected ? 'check' : 'shield'" :size="16"/></span>
        <div style="flex:1">
          <template v-if="machines.length === 1">
            <b style="color:var(--tx-0)">This is the first node of your mesh.</b>
            <span class="muted"> It runs the primary controller. Add a second node, then install a replica controller for high availability — a controller can't be doubled up on one node.</span>
          </template>
          <template v-else-if="!haProtected">
            <b style="color:var(--tx-0)">Add a replica controller for high availability.</b>
            <span class="muted"> You have {{ machines.length }} machines but one controller. Install a replica on another node so the mesh survives a failure.</span>
          </template>
          <template v-else>
            <b style="color:var(--tx-0)">High availability is active.</b>
            <span class="muted"> {{ controllerCount }} controllers across separate nodes — the mesh survives a node loss.</span>
          </template>
        </div>
        <button v-if="!haProtected" class="btn btn-sm" :disabled="!replicaCandidates.length"
          :title="replicaCandidates.length ? '' : 'Add a second node first'">
          {{ replicaCandidates.length ? 'Install replica controller' : 'Needs a 2nd node' }}
        </button>
      </div>

      <!-- site -->
      <div class="section-title">
        <Icon name="link" :size="15" style="color:var(--tx-3)"/>
        <h2 style="text-transform:none;font-size:14px;color:var(--tx-0);letter-spacing:-.01em">Local site</h2>
        <span class="rule"/>
        <span class="faint" style="font-size:11px">{{ machines.length }} machine{{ machines.length===1?'':'s' }} · more sites can be added</span>
      </div>

      <div class="grid machines">
        <div v-for="m in machines" :key="m.id" class="card card-pad machine" :class="{primary: m.self}">
          <div class="between" style="margin-bottom:12px">
            <div class="row gap-2">
              <span class="micon"><Icon name="server" :size="16"/></span>
              <span class="dot" :class="m.online ? 'ok' : 'idle'" :style="m.online?'animation:pulse 2s infinite':''"/>
            </div>
            <span v-if="m.self" class="pill brand" style="height:18px">this node</span>
          </div>
          <span class="mname mono">{{ m.name }}</span>
          <span class="faint" style="font-size:11.5px;display:block;margin-top:2px">
            {{ m.cores || '?' }} cores · {{ m.memGB || '?' }} GB{{ m.gpu ? ' · ' + m.gpu + ' GB GPU' : '' }}
            <template v-if="m.self"> · {{ Math.round(m.cpu||0) }}% CPU</template>
          </span>

          <div class="comps">
            <div v-if="!m.components.length" class="faint" style="font-size:11.5px;padding:8px 0">traegod worker · no components installed</div>
            <div v-for="c in m.components" :key="c.type" class="comp" :style="{'--cc': compMeta(c.type).color}">
              <span class="cdot"/>
              <Icon :name="compMeta(c.type).icon" :size="13"/>
              <span class="clabel">{{ compMeta(c.type).label }}</span>
              <span v-if="c.role" class="pill" style="height:16px;font-size:10px">{{ c.role }}</span>
              <span class="cstat">{{ c.status }}</span>
            </div>
            <!-- install AI controller where absent -->
            <button v-if="m.online && !m.components.some(c=>c.type==='ai-controller')" class="install">
              <Icon name="plus" :size="12"/> Install AI Controller
            </button>
          </div>
        </div>
      </div>

      <!-- add a machine -->
      <div class="card card-pad addnode">
        <span class="aico"><Icon name="plus" :size="16"/></span>
        <div style="flex:1">
          <div style="font-weight:600;color:var(--tx-0);font-size:13.5px">Add a machine to the mesh</div>
          <div class="muted" style="font-size:12px;margin-top:2px">Install the worker on any Linux box; it discovers this controller and waits to be adopted:</div>
          <code class="cmd">curl -fsSL https://get.traego.io | sh -s -- join</code>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.offline { display: flex; align-items: center; gap: 14px; border-color: rgba(251,113,133,.3); background: linear-gradient(90deg, rgba(251,113,133,.07), var(--bg-1)); }
.lifecycle { display: flex; align-items: center; gap: 14px; margin-bottom: 8px; }
.lifecycle.warn { border-color: rgba(251,191,36,.3); background: linear-gradient(90deg, rgba(251,191,36,.06), var(--bg-1)); }
.lifecycle.ok { border-color: rgba(52,211,153,.28); background: linear-gradient(90deg, rgba(52,211,153,.06), var(--bg-1)); }
.lico { width: 34px; height: 34px; flex: none; border-radius: 10px; display: grid; place-items: center; color: var(--warn); background: rgba(251,191,36,.12); border: 1px solid rgba(251,191,36,.3); }
.lifecycle.ok .lico { color: var(--ok); background: rgba(52,211,153,.12); border-color: rgba(52,211,153,.3); }

.machines { grid-template-columns: repeat(3, 1fr); }
.machine.primary { border-color: rgba(56,189,248,.3); background: linear-gradient(135deg, rgba(56,189,248,.05), var(--bg-1)); }
.micon { width: 32px; height: 32px; border-radius: 9px; display: grid; place-items: center; color: var(--brand); background: rgba(56,189,248,.12); border: 1px solid rgba(56,189,248,.3); }
.mname { font-size: 14px; font-weight: 650; color: var(--tx-0); }
.comps { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--line); display: flex; flex-direction: column; gap: 7px; }
.comp { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--tx-1); }
.comp :deep(svg) { color: var(--cc); }
.cdot { width: 7px; height: 7px; border-radius: 50%; background: var(--cc); box-shadow: 0 0 7px -1px var(--cc); flex: none; }
.clabel { font-weight: 600; color: var(--tx-0); }
.cstat { margin-left: auto; font-size: 10.5px; color: var(--tx-3); font-family: var(--mono); }
.install { display: inline-flex; align-items: center; gap: 5px; align-self: flex-start; margin-top: 2px; padding: 5px 9px; border-radius: 7px; border: 1px dashed var(--line-strong); background: transparent; color: var(--tx-2); font-size: 11.5px; }
.install:hover { border-color: var(--ai); color: var(--ai); }

.addnode { display: flex; align-items: flex-start; gap: 14px; margin-top: 18px; border-style: dashed; }
.aico { width: 34px; height: 34px; flex: none; border-radius: 10px; display: grid; place-items: center; color: var(--tx-2); border: 1px solid var(--line-strong); }
.cmd { display: inline-block; margin-top: 8px; font-family: var(--mono); font-size: 11.5px; color: var(--brand); background: var(--bg-inset); border: 1px solid var(--line); border-radius: 7px; padding: 6px 10px; }

@media (max-width: 1000px) { .machines { grid-template-columns: 1fr 1fr; } }
</style>
