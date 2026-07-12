<script setup>
import { onMounted, onUnmounted, ref } from 'vue'
import { useController } from '../stores/controller'
import Icon from '../components/Icon.vue'

// LIVE: manages the controller's local AI models via Ollama.
const ctl = useController()
const busy = ref('')
const rowError = ref({ id: '', msg: '' }) // inline, next to the row that failed
let poll = null

onMounted(() => { ctl.fetchModels(); poll = setInterval(() => ctl.fetchModels(), 1500) })
onUnmounted(() => clearInterval(poll))

async function run(m, fn) {
  busy.value = m.id
  rowError.value = { id: '', msg: '' }
  try { await fn() } catch (e) { rowError.value = { id: m.id, msg: e.message } } finally { busy.value = '' }
}
const deploy = (m) => run(m, () => ctl.deployModel(m.id))
const enable = (m) => run(m, () => ctl.enableModel(m.id))
const disable = (m) => run(m, () => ctl.disableModel(m.id))
</script>

<template>
  <div class="page">
    <div v-if="!ctl.aiInstalled" class="card card-pad" style="display:flex;align-items:center;gap:14px">
      <Icon name="sparkles" :size="20" style="color:var(--ai)"/>
      <div style="flex:1">
        <div style="font-weight:600;color:var(--tx-0)">The AI module isn't installed</div>
        <div class="muted" style="font-size:12.5px;margin-top:2px">Install it on the Hardware page (with a VRAM reservation) to deploy and manage models.</div>
      </div>
      <RouterLink to="/hardware" class="btn btn-primary btn-sm">Go to Hardware</RouterLink>
    </div>
    <template v-if="ctl.aiInstalled">
    <div class="page-head between">
      <div>
        <h2 class="page-title"><Icon name="sparkles" :size="22" style="color:var(--ai)"/> AI Inference</h2>
        <p class="muted" style="margin-top:5px">
          Private local models on this machine ·
          <span class="mono">{{ ctl.machineMemGB ? Math.round(ctl.machineMemGB) + ' GB RAM' : '—' }}</span>
        </p>
      </div>
      <div class="row gap-2">
        <span class="pill" :class="ctl.backendHealthy ? 'ok' : 'crit'">
          <span class="dot" :class="ctl.backendHealthy ? 'ok' : 'crit'"/>
          {{ ctl.backendHealthy ? 'Ollama online' : 'Ollama offline' }}
        </span>
        <RouterLink to="/chat" class="btn btn-primary btn-sm" target="_blank"><Icon name="sparkles" :size="14"/> Open chat</RouterLink>
      </div>
    </div>

    <div v-if="!ctl.backendHealthy" class="card card-pad warnbar">
      <Icon name="alert" :size="18" style="color:var(--warn)"/>
      <div>
        <div style="font-weight:650;color:var(--tx-0)">The inference backend isn't reachable</div>
        <div class="muted" style="font-size:12.5px;margin-top:3px">Start Ollama on this machine: <span class="mono">ollama serve</span> — then models can be deployed.</div>
      </div>
    </div>

    <div class="section-title"><h2>Model catalog</h2><span class="rule"/>
      <span class="faint" style="font-size:11px">only models that fit {{ Math.round(ctl.machineMemGB) }} GB are deployable</span>
    </div>

    <div v-if="!ctl.models.length" class="card card-pad catalog-empty">
      <Icon name="sparkles" :size="22" style="color:var(--tx-3)"/>
      <div>
        <div style="font-weight:600;color:var(--tx-0)">No models in the catalog yet</div>
        <div class="muted" style="font-size:12.5px;margin-top:3px">The catalog fills in once the controller can reach its inference backend.</div>
      </div>
    </div>

    <div class="grid models">
      <div v-for="m in ctl.models" :key="m.id" class="card card-pad model" :class="{enabled: m.enabled, dim: !m.fits && !m.deployed}">
        <div class="row gap-3" style="flex:1;min-width:0">
          <span class="mico"><Icon name="sparkles" :size="16"/></span>
          <div class="col" style="min-width:0">
            <div class="row gap-2">
              <span class="mname">{{ m.name }}</span>
              <span class="pill" style="height:18px">{{ m.params }}</span>
              <span v-if="m.enabled" class="pill ai" style="height:18px"><Icon name="check" :size="10"/> enabled{{ m.default ? ' · default' : '' }}</span>
            </div>
            <span class="muted" style="font-size:12px">{{ m.blurb }}</span>
            <span class="faint mono" style="font-size:10.5px;margin-top:2px">{{ m.size_gb }} GB download · needs ~{{ m.min_mem_gb }} GB RAM</span>
          </div>
        </div>

        <div class="actions">
          <!-- deploying -->
          <div v-if="m.deploying" class="deploying">
            <div class="between" style="font-size:11px;margin-bottom:4px"><span class="muted">{{ m.status || 'pulling' }}</span><span class="mono">{{ Math.round(m.progress) }}%</span></div>
            <div class="meter" style="width:160px"><span class="a" :style="{width: m.progress+'%'}"/></div>
          </div>
          <!-- not fitting -->
          <span v-else-if="!m.fits && !m.deployed" class="pill warn">Too large for this machine</span>
          <!-- deployed -->
          <template v-else-if="m.deployed">
            <span class="pill ok"><Icon name="check" :size="11"/> deployed</span>
            <button v-if="!m.enabled" class="btn btn-sm" :disabled="busy===m.id || !ctl.backendHealthy"
              title="Users can pick this model in the chat" @click="enable(m)">Enable in chat</button>
            <button v-else class="btn btn-sm btn-ghost" :disabled="busy===m.id" @click="disable(m)">Disable</button>
          </template>
          <!-- available to deploy -->
          <button v-else class="btn btn-sm" :disabled="busy===m.id || !ctl.backendHealthy" @click="deploy(m)">
            <Icon name="update" :size="13"/> Deploy
          </button>
          <span v-if="rowError.id===m.id" class="rowerr" role="alert">{{ rowError.msg }}</span>
        </div>
      </div>
    </div>
    </template>
  </div>
</template>

<style scoped>
.warnbar { display: flex; align-items: center; gap: 14px; margin-bottom: 18px; border-color: rgba(251,191,36,.3); background: linear-gradient(90deg, rgba(251,191,36,.07), var(--bg-1)); }
.models { grid-template-columns: 1fr; gap: 10px; }
.model { display: flex; align-items: center; gap: 16px; }
.model.enabled { border-color: rgba(167,139,250,.4); background: linear-gradient(90deg, rgba(167,139,250,.06), var(--bg-1)); }
.model.dim { opacity: .55; }
.mico { width: 36px; height: 36px; flex: none; border-radius: 10px; display: grid; place-items: center; color: var(--ai); background: color-mix(in srgb, var(--ai) 13%, #0c1016); border: 1px solid color-mix(in srgb, var(--ai) 28%, transparent); }
.mname { font-size: 14px; font-weight: 650; color: var(--tx-0); }
.actions { display: flex; align-items: center; gap: 10px; flex: none; }
.deploying { min-width: 170px; }
.rowerr { font-size: 12px; color: var(--crit); max-width: 220px; }
.catalog-empty { display: flex; align-items: center; gap: 14px; border-style: dashed; }
@media (max-width: 640px) { .model { flex-direction: column; align-items: flex-start; gap: 10px; } .actions { flex-wrap: wrap; } }
</style>
