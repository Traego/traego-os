<script setup>
import { computed, ref } from 'vue'
import { useController } from '../stores/controller'
import Icon from '../components/Icon.vue'

// LIVE mesh view: sites → machines → components, and the controller + AI module
// install lifecycle. Multi-site aware from day one.
const ctl = useController()

// AI module install: a soft VRAM reservation (a managed target for the DevOps
// agent), set aside from the node's GPU/unified memory.
const totalVram = computed(() => Math.round(ctl.system?.gpu_vram_gb || ctl.system?.mem_total_gb || 8))
const vramTarget = ref(0)
const installing = ref(false)
const installError = ref('')
function defaultVram() { return Math.max(1, Math.round(totalVram.value * 0.75)) }
async function installAI() {
  installError.value = ''
  installing.value = true
  try { await ctl.installAI(vramTarget.value || defaultVram()) }
  catch (e) { installError.value = e.message }
  finally { installing.value = false }
}
// Two-step inline confirm — no native dialog inside the custom UI.
const confirmingUninstall = ref(false)
async function uninstallAI() {
  if (!confirmingUninstall.value) { confirmingUninstall.value = true; return }
  confirmingUninstall.value = false
  await ctl.uninstallAI()
}

const COMPONENTS = {
  controller: { label: 'Controller', icon: 'cpu', color: 'var(--brand)', desc: 'Control plane' },
  'ai-controller': { label: 'AI Controller', icon: 'sparkles', color: 'var(--ai)', desc: 'Local inference' }
}

// Adoption: the operator types the pairing code shown in the traegod output on
// the machine — proof they control that box — and assigns it a role.
const ROLES = [
  { id: 'app', label: 'App — general workloads' },
  { id: 'inference', label: 'Inference — runs AI models' },
  { id: 'storage', label: 'Storage — holds data' },
  { id: 'controller', label: 'Controller — replica control plane' }
]
const adoptForms = ref({}) // id -> { code, role, busy, error }
function adoptForm(id) {
  if (!adoptForms.value[id]) adoptForms.value[id] = { code: '', role: 'app', busy: false, error: '' }
  return adoptForms.value[id]
}
async function adoptNode(n) {
  const f = adoptForm(n.id)
  f.error = ''
  if (!f.code.trim()) { f.error = 'Enter the pairing code from the machine.'; return }
  f.busy = true
  try { await ctl.adopt(n.id, f.code.trim().toUpperCase(), f.role) }
  catch (e) { f.error = e.message === 'pairing code mismatch' ? 'That code doesn’t match. Check the traegod output on the machine.' : e.message }
  finally { f.busy = false }
}
async function rejectNode(n) {
  const f = adoptForm(n.id)
  f.busy = true
  try { await ctl.removeNode(n.id) }
  catch (e) { f.error = e.message }
  finally { f.busy = false }
}

// Compose the fleet of machines from live controller data.
const machines = computed(() => {
  const list = []
  if (ctl.connected && ctl.system) {
    const comps = [{ type: 'controller', role: 'primary', status: 'running' }]
    if (ctl.aiInstalled) comps.push({ type: 'ai-controller', status: 'running' })
    list.push({
      id: 'self', name: ctl.system.hostname || 'controller', self: true,
      cores: ctl.system.cores, memGB: ctl.system.mem_total_gb,
      cpu: ctl.system.cpu_percent, online: true, components: comps
    })
  }
  for (const n of ctl.adopted) {
    const comps = []
    if (n.role === 'inference') comps.push({ type: 'ai-controller', status: n.state })
    list.push({
      id: n.id, name: n.name, cores: n.specs?.cpu_cores, memGB: n.specs?.memory_gb,
      gpu: n.specs?.gpu_vram_gb, online: n.state === 'online', secured: n.secured, role: n.role, components: comps
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
        <h2 class="page-title"><Icon name="server" :size="22" style="color:var(--brand)"/> Hardware</h2>
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
          :title="replicaCandidates.length ? '' : (ctl.pending.length ? 'Adopt the pending node below first' : 'Add a second node first')">
          {{ replicaCandidates.length ? 'Install replica controller' : (ctl.pending.length ? 'Adopt a node first' : 'Needs a 2nd node') }}
        </button>
      </div>

      <!-- pending adoption -->
      <template v-if="ctl.pending.length">
        <div class="section-title">
          <Icon name="alert" :size="15" style="color:var(--brand)"/>
          <h2 style="text-transform:none;font-size:14px;color:var(--tx-0);letter-spacing:-.01em">Waiting for adoption</h2>
          <span class="rule"/>
          <span class="faint" style="font-size:11px">{{ ctl.pending.length }} machine{{ ctl.pending.length===1?'':'s' }}</span>
        </div>
        <div class="grid machines">
          <div v-for="n in ctl.pending" :key="n.id" class="card card-pad machine pending-card">
            <div class="between" style="margin-bottom:12px">
              <div class="row gap-2">
                <span class="micon"><Icon name="server" :size="16"/></span>
                <span class="dot brand" style="animation:pulse 2s infinite"/>
              </div>
              <span class="pill brand" style="height:18px">waiting for adoption</span>
            </div>
            <span class="mname mono">{{ n.name }}</span>
            <span class="faint" style="font-size:11.5px;display:block;margin-top:2px">
              {{ n.specs?.cpu_cores || '?' }} cores · {{ n.specs?.memory_gb || '?' }} GB{{ n.specs?.gpu_vram_gb ? ' · ' + n.specs.gpu_vram_gb + ' GB GPU' : '' }}
            </span>
            <div class="adopt-form">
              <label class="adopt-label" :for="'code-'+n.id">Pairing code — shown in the <span class="mono">traegod</span> output on this machine</label>
              <input :id="'code-'+n.id" v-model="adoptForm(n.id).code" class="code-input mono" placeholder="XXXX-XXXX"
                spellcheck="false" autocomplete="off" :disabled="adoptForm(n.id).busy"
                @keyup.enter="adoptNode(n)" @input="adoptForm(n.id).error = ''"/>
              <label class="adopt-label" :for="'role-'+n.id">Role</label>
              <select :id="'role-'+n.id" v-model="adoptForm(n.id).role" class="role-select" :disabled="adoptForm(n.id).busy">
                <option v-for="r in ROLES" :key="r.id" :value="r.id">{{ r.label }}</option>
              </select>
              <div class="row gap-2" style="margin-top:10px">
                <button class="btn btn-primary btn-sm" @click="adoptNode(n)" :disabled="adoptForm(n.id).busy">
                  <Icon name="check" :size="12"/> {{ adoptForm(n.id).busy ? 'Adopting…' : 'Adopt' }}
                </button>
                <button class="btn btn-sm btn-ghost" @click="rejectNode(n)" :disabled="adoptForm(n.id).busy">Remove</button>
              </div>
              <div v-if="adoptForm(n.id).error" class="adopt-error" role="alert">{{ adoptForm(n.id).error }}</div>
            </div>
          </div>
        </div>
      </template>

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
          </div>
        </div>
      </div>

      <!-- AI module install: explicit, with a soft VRAM reservation -->
      <div class="card card-pad aimod">
        <span class="aico" style="--cc:#a78bfa"><Icon name="sparkles" :size="16"/></span>
        <div style="flex:1">
          <template v-if="!ctl.aiInstalled">
            <div style="font-weight:600;color:var(--tx-0);font-size:13.5px">Install the AI module</div>
            <div class="muted" style="font-size:12px;margin:2px 0 10px">Runs local inference on this node. Set aside VRAM for it — a soft target the DevOps agent manages.</div>
            <div class="vrow">
              <input type="range" min="1" :max="totalVram" :value="vramTarget || defaultVram()" @input="vramTarget = +$event.target.value" class="vslider"/>
              <span class="mono vval">{{ vramTarget || defaultVram() }} <span class="faint">/ {{ totalVram }} GB{{ ctl.system?.os==='darwin' ? ' unified' : ' VRAM' }}</span></span>
            </div>
            <button class="btn btn-primary btn-sm" style="margin-top:10px" @click="installAI" :disabled="installing">
              <Icon name="plus" :size="12"/> {{ installing ? 'Installing…' : 'Install AI module' }}
            </button>
            <span v-if="installError" class="faint" style="color:var(--crit);margin-left:10px;font-size:12px">{{ installError }}</span>
          </template>
          <template v-else>
            <div style="font-weight:600;color:var(--tx-0);font-size:13.5px">AI module installed
              <span class="pill ai" style="height:18px;margin-left:6px">{{ ctl.aiVramGB }} GB reserved</span>
            </div>
            <div class="muted" style="font-size:12px;margin-top:2px">Soft reservation, managed by the DevOps agent.</div>
            <div class="row gap-2" style="margin-top:10px">
              <RouterLink to="/ai" class="btn btn-sm btn-ghost">Manage models</RouterLink>
              <button class="btn btn-sm" :class="confirmingUninstall ? 'btn-danger' : 'btn-ghost'"
                @click="uninstallAI" @blur="confirmingUninstall = false">
                {{ confirmingUninstall ? 'Click again to uninstall' : 'Uninstall' }}
              </button>
              <span v-if="confirmingUninstall" class="faint" style="font-size:11.5px">models stay on disk; the VRAM reservation is released</span>
            </div>
          </template>
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
.pending-card { border-style: dashed; border-color: rgba(56,189,248,.4); background: linear-gradient(135deg, rgba(56,189,248,.06), var(--bg-1)); }
.adopt-form { margin-top: 12px; padding-top: 12px; border-top: 1px dashed var(--line); display: flex; flex-direction: column; }
.adopt-label { font-size: 11px; color: var(--tx-2); margin: 8px 0 4px; }
.code-input { width: 100%; padding: 8px 10px; border-radius: var(--radius-sm); border: 1px solid var(--line-strong); background: var(--bg-inset); color: var(--tx-0); font-size: 14px; letter-spacing: .12em; text-transform: uppercase; }
.code-input::placeholder { color: var(--tx-3); letter-spacing: .12em; }
.role-select { width: 100%; padding: 8px 10px; border-radius: var(--radius-sm); border: 1px solid var(--line-strong); background: var(--bg-inset); color: var(--tx-0); font-size: 12.5px; }
.adopt-error { margin-top: 8px; font-size: 12px; color: var(--crit); }
.machine.primary { border-color: rgba(56,189,248,.3); background: linear-gradient(135deg, rgba(56,189,248,.05), var(--bg-1)); }
.micon { width: 32px; height: 32px; border-radius: var(--radius-sm); display: grid; place-items: center; color: var(--brand); background: rgba(56,189,248,.12); border: 1px solid rgba(56,189,248,.3); }
.mname { font-size: 14px; font-weight: 650; color: var(--tx-0); }
.comps { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--line); display: flex; flex-direction: column; gap: 7px; }
.comp { display: flex; align-items: center; gap: 8px; font-size: 12px; color: var(--tx-1); }
.comp :deep(svg) { color: var(--cc); }
.cdot { width: 7px; height: 7px; border-radius: 50%; background: var(--cc); box-shadow: 0 0 7px -1px var(--cc); flex: none; }
.clabel { font-weight: 600; color: var(--tx-0); }
.cstat { margin-left: auto; font-size: 10.5px; color: var(--tx-3); font-family: var(--mono); }
.addnode { display: flex; align-items: flex-start; gap: 14px; margin-top: 18px; border-style: dashed; }
.aico { width: 34px; height: 34px; flex: none; border-radius: 10px; display: grid; place-items: center; color: var(--tx-2); border: 1px solid var(--line-strong); }
.aimod { display: flex; align-items: flex-start; gap: 14px; margin-top: 14px; }
.aimod .aico { color: var(--cc); border-color: color-mix(in srgb, var(--cc) 40%, transparent); }
.vrow { display: flex; align-items: center; gap: 12px; max-width: 420px; }
.vslider { flex: 1; accent-color: var(--ai); }
.vval { font-size: 13px; color: var(--tx-0); white-space: nowrap; }
.cmd { display: inline-block; margin-top: 8px; font-family: var(--mono); font-size: 11.5px; color: var(--brand); background: var(--bg-inset); border: 1px solid var(--line); border-radius: 7px; padding: 6px 10px; }

@media (max-width: 1000px) { .machines { grid-template-columns: 1fr 1fr; } }
@media (max-width: 640px) {
  .machines { grid-template-columns: 1fr; }
  .lifecycle, .aimod, .addnode { flex-direction: column; align-items: flex-start; gap: 10px; }
  .page-head.between { flex-direction: column; align-items: flex-start; gap: 10px; }
  .page-head .row { flex-wrap: wrap; }
}
</style>
