import { defineStore } from 'pinia'

// Live connection to the real Traego controller API. Unlike the `system` store
// (which is mock data for the design prototype), this talks to a running
// controller — the v1 wiring of the designed UI to the real backend.

const BASE_KEY = 'traego_api_base'
const ADMIN_KEY = 'traego_admin_key'

// When the SPA is served by the controller, the API is same-origin (''). During
// Vite dev (port 5173) we point at the local controller on :8443.
function defaultBase() {
  if (typeof window !== 'undefined' && window.location.port === '5173') return 'http://localhost:8443'
  return ''
}

export const useController = defineStore('controller', {
  state: () => ({
    apiBase: localStorage.getItem(BASE_KEY) ?? defaultBase(),
    adminKey: localStorage.getItem(ADMIN_KEY) || 'dev-admin-key',
    nodes: [],
    system: null,
    activity: null,
    models: [],
    modelEnabled: '',
    backendHealthy: false,
    machineMemGB: 0,
    connected: false,
    error: null,
    loaded: false
  }),

  getters: {
    pending: (s) => s.nodes.filter((n) => n.state === 'pending'),
    adopted: (s) => s.nodes.filter((n) => n.state !== 'pending'),
    onlineCount: (s) => s.nodes.filter((n) => n.state === 'online').length,
    poolGpuGB: (s) => s.nodes.filter((n) => n.state === 'online').reduce((a, n) => a + (n.specs?.gpu_vram_gb || 0), 0),
    securedCount: (s) => s.nodes.filter((n) => n.secured).length
  },

  actions: {
    setApiBase(v) { this.apiBase = v; localStorage.setItem(BASE_KEY, v) },
    setAdminKey(v) { this.adminKey = v; localStorage.setItem(ADMIN_KEY, v) },
    _headers() { return { Authorization: 'Bearer ' + this.adminKey, 'Content-Type': 'application/json' } },

    async refresh() {
      try {
        const [nodesR, sysR, actR] = await Promise.all([
          fetch(this.apiBase + '/api/v1/nodes', { headers: this._headers() }),
          fetch(this.apiBase + '/api/v1/system', { headers: this._headers() }),
          fetch(this.apiBase + '/api/v1/activity', { headers: this._headers() })
        ])
        if (!nodesR.ok) { this.connected = false; this.error = 'http ' + nodesR.status; return }
        this.nodes = (await nodesR.json()).nodes || []
        if (sysR.ok) this.system = await sysR.json()
        if (actR.ok) this.activity = await actR.json()
        this.fetchModels() // keep model/backend state current everywhere (Hardware needs it)
        this.connected = true
        this.error = null
        this.loaded = true
      } catch (e) {
        this.connected = false
        this.error = 'unreachable'
      }
    },

    async adopt(id, code, role) {
      const r = await fetch(this.apiBase + '/api/v1/nodes/' + id + '/adopt', {
        method: 'POST', headers: this._headers(),
        body: JSON.stringify({ pairing_code: code, role })
      })
      if (!r.ok) throw new Error('adopt failed (' + r.status + ')')
      await this.refresh()
    },

    // ---- Local AI / models ----
    async fetchModels() {
      try {
        const r = await fetch(this.apiBase + '/api/v1/models', { headers: this._headers() })
        if (!r.ok) return
        const d = await r.json()
        this.models = d.models || []
        this.modelEnabled = d.enabled || ''
        this.backendHealthy = !!d.backend_healthy
        this.machineMemGB = d.machine_mem_gb || 0
      } catch (e) { /* controller unreachable; ignore */ }
    },
    async deployModel(id) {
      await fetch(this.apiBase + '/api/v1/models/' + encodeURIComponent(id) + '/deploy', { method: 'POST', headers: this._headers() })
      await this.fetchModels()
    },
    async enableModel(id) {
      const r = await fetch(this.apiBase + '/api/v1/models/' + encodeURIComponent(id) + '/enable', { method: 'POST', headers: this._headers() })
      if (!r.ok) { const e = await r.json().catch(() => ({})); throw new Error(e.error || 'enable failed (' + r.status + ')') }
      await this.fetchModels()
    },
    async disableModel(id) {
      const r = await fetch(this.apiBase + '/api/v1/models/' + encodeURIComponent(id) + '/disable', { method: 'POST', headers: this._headers() })
      if (!r.ok) { const e = await r.json().catch(() => ({})); throw new Error(e.error || 'disable failed (' + r.status + ')') }
      await this.fetchModels()
    },
    // public — the models a chat user can pick from (deployed) + the default
    async fetchChatModels() {
      try {
        const r = await fetch(this.apiBase + '/api/v1/chat/models')
        if (!r.ok) return { models: [], default: '' }
        return await r.json()
      } catch (e) { return { models: [], default: '' } }
    },
    // public — no admin key needed. `messages` is the full conversation
    // ([{role:'user'|'assistant', content}]); `model` optionally selects which
    // deployed model to use ('' = enabled default).
    async chat(messages, model = '') {
      const r = await fetch(this.apiBase + '/api/v1/chat', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ messages, model })
      })
      const d = await r.json().catch(() => ({}))
      if (!r.ok) throw new Error(d.error || 'chat failed (' + r.status + ')')
      return d.reply
    }
  }
})
