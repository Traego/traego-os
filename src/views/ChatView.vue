<script setup>
import { ref, onMounted } from 'vue'
import { nextTick } from 'vue'
import { useController } from '../stores/controller'
import Icon from '../components/Icon.vue'

// Public, end-user chat. No admin console around it.
const ctl = useController()
const input = ref('')
const sending = ref(false)
const messages = ref([]) // {role:'user'|'ai', text}
const scroller = ref(null)

// selectable models
const models = ref([])       // [{id, name}]
const selectedModel = ref('') // '' = controller's enabled default

onMounted(async () => {
  const r = await ctl.fetchChatModels()
  models.value = r.models || []
  selectedModel.value = r.default || (models.value[0]?.id || '')
})

const suggestions = [
  'Explain what an API is in one sentence.',
  'Write a haiku about on-prem servers.',
  'What can you help me with?'
]

async function send(text) {
  const prompt = (text ?? input.value).trim()
  if (!prompt || sending.value) return
  input.value = ''
  messages.value.push({ role: 'user', text: prompt })
  sending.value = true
  await scrollDown()
  try {
    // send the whole conversation so the model remembers context
    const history = messages.value
      .filter((m) => !m.error)
      .map((m) => ({ role: m.role === 'ai' ? 'assistant' : 'user', content: m.text }))
    const reply = await ctl.chat(history, selectedModel.value)
    messages.value.push({ role: 'ai', text: reply })
  } catch (e) {
    messages.value.push({ role: 'ai', text: '', error: e.message })
  } finally {
    sending.value = false
    await scrollDown()
  }
}
async function scrollDown() {
  await nextTick()
  if (scroller.value) scroller.value.scrollTop = scroller.value.scrollHeight
}
</script>

<template>
  <div class="chatpage">
    <header class="chead">
      <div class="brand">
        <div class="logo"><Icon name="bolt" :size="16"/></div>
        <div class="col" style="line-height:1.15">
          <span class="bname">Traego AI</span>
          <span class="bsub">Private · runs on-prem</span>
        </div>
      </div>
      <div class="row gap-3">
        <label v-if="models.length" class="modelpick">
          <Icon name="sparkles" :size="13" />
          <select v-model="selectedModel" aria-label="Model">
            <option v-for="m in models" :key="m.id" :value="m.id">{{ m.name }}</option>
          </select>
        </label>
        <a href="#/" class="exit">Admin console <Icon name="chevron" :size="13"/></a>
      </div>
    </header>

    <main class="conv" ref="scroller">
      <div v-if="!messages.length" class="empty">
        <div class="bigicon"><Icon name="sparkles" :size="30"/></div>
        <h2>Ask me anything</h2>
        <p class="muted">Answers come from a model running on your own hardware — nothing leaves the building.</p>
        <div class="suggs">
          <button v-for="s in suggestions" :key="s" class="sg" @click="send(s)">{{ s }}</button>
        </div>
      </div>

      <div v-for="(m,i) in messages" :key="i" class="msg" :class="m.role">
        <div class="avatar" :class="m.role"><Icon :name="m.role==='ai' ? 'sparkles' : 'users'" :size="14"/></div>
        <div class="bubble" :class="{err: m.error}">
          <span v-if="m.error && /no model/i.test(m.error)">⚠ No model is available yet — ask your admin to enable one in the <a href="#/ai" class="errlink">admin console</a>.</span>
          <span v-else-if="m.error">⚠ {{ m.error }}</span>
          <span v-else style="white-space:pre-wrap">{{ m.text }}</span>
        </div>
      </div>

      <div v-if="sending" class="msg ai">
        <div class="avatar ai"><Icon name="sparkles" :size="14"/></div>
        <div class="bubble thinking"><span/><span/><span/></div>
      </div>
    </main>

    <footer class="composer">
      <div class="cbar">
        <input v-model="input" placeholder="Send a message…" aria-label="Message" @keyup.enter="send()" :disabled="sending" />
        <button class="send" aria-label="Send message" :disabled="sending || !input.trim()" @click="send()"><Icon name="chevron" :size="18"/></button>
      </div>
      <p class="hint faint">Traego runs this model locally. Responses may be imperfect.</p>
    </footer>
  </div>
</template>

<style scoped>
.chatpage { position: fixed; inset: 0; display: flex; flex-direction: column;
  background: radial-gradient(900px 500px at 80% -10%, rgba(167,139,250,.10), transparent 60%), var(--bg-0); }
.chead { display: flex; align-items: center; justify-content: space-between; padding: 14px 22px; border-bottom: 1px solid var(--line); }
.brand { display: flex; align-items: center; gap: 11px; }
.logo { width: 32px; height: 32px; border-radius: 9px; display: grid; place-items: center; color: #04121c; background: linear-gradient(150deg, var(--ai), var(--brand-2)); }
.bname { font-weight: 700; color: var(--tx-0); font-size: 15px; letter-spacing: -.01em; }
.bsub { font-size: 11px; color: var(--tx-3); }
.exit { font-size: 12.5px; color: var(--tx-2); display: inline-flex; align-items: center; gap: 3px; }
.exit:hover { color: var(--tx-0); }
.modelpick { display: inline-flex; align-items: center; gap: 6px; padding: 5px 10px; border-radius: 9px; border: 1px solid var(--line); background: var(--bg-2); color: var(--ai); }
.modelpick select { background: transparent; border: 0; color: var(--tx-1); font-size: 12.5px; font-family: inherit; cursor: pointer; max-width: 160px; }
.modelpick:focus-within { border-color: rgba(167,139,250,.5); }
.modelpick select option { background: var(--bg-2); color: var(--tx-1); }

.conv { flex: 1; overflow-y: auto; padding: 26px 0; }
.empty { max-width: 560px; margin: 8vh auto 0; text-align: center; padding: 0 20px; }
.bigicon { width: 64px; height: 64px; border-radius: 18px; margin: 0 auto 16px; display: grid; place-items: center; color: var(--ai);
  background: rgba(167,139,250,.12); border: 1px solid rgba(167,139,250,.3); box-shadow: 0 0 40px -10px var(--ai-glow); }
.empty h2 { font-size: 20px; margin-bottom: 8px; }
.suggs { display: flex; flex-direction: column; gap: 8px; margin-top: 22px; }
.sg { padding: 12px 14px; border-radius: 11px; border: 1px solid var(--line); background: var(--bg-2); color: var(--tx-1); font-size: 13px; text-align: left; transition: border-color .14s, color .14s; }
.sg:hover { border-color: rgba(167,139,250,.4); color: var(--tx-0); }

.msg { max-width: 720px; margin: 0 auto; display: flex; gap: 12px; padding: 10px 20px; }
.msg.user { flex-direction: row-reverse; }
.avatar { width: 30px; height: 30px; flex: none; border-radius: 9px; display: grid; place-items: center; }
.avatar.ai { color: var(--tx-0); background: linear-gradient(150deg, var(--ai), #6d28d9); }
.avatar.user { color: var(--tx-1); background: var(--bg-3); border: 1px solid var(--line); }
.bubble { padding: 11px 15px; border-radius: 13px; font-size: 14px; line-height: 1.55; color: var(--tx-0); }
.msg.user .bubble { background: rgba(56,189,248,.1); border: 1px solid rgba(56,189,248,.25); }
.msg.ai .bubble { background: linear-gradient(180deg, var(--bg-2), var(--bg-1)); border: 1px solid var(--line); }
.bubble.err { color: var(--warn); border-color: rgba(251,191,36,.3); }
.errlink { color: var(--brand); text-decoration: underline; }
.thinking { display: inline-flex; gap: 5px; align-items: center; }
.thinking span { width: 7px; height: 7px; border-radius: 50%; background: var(--ai); animation: bob 1.1s infinite; }
.thinking span:nth-child(2) { animation-delay: .15s; } .thinking span:nth-child(3) { animation-delay: .3s; }
@keyframes bob { 0%,80%,100% { opacity: .3; transform: translateY(0); } 40% { opacity: 1; transform: translateY(-3px); } }

.composer { padding: 14px 20px 18px; border-top: 1px solid var(--line); }
.cbar { max-width: 720px; margin: 0 auto; display: flex; gap: 10px; align-items: center; background: var(--bg-2); border: 1px solid var(--line-strong); border-radius: 14px; padding: 7px 8px 7px 16px; }
.cbar:focus-within { border-color: rgba(167,139,250,.5); }
.cbar input { flex: 1; background: transparent; border: 0; outline: none; color: var(--tx-0); font-size: 14.5px; font-family: inherit; } /* focus shown by .cbar:focus-within */
.send { width: 38px; height: 38px; flex: none; border: 0; border-radius: 10px; display: grid; place-items: center; color: var(--tx-0); background: linear-gradient(150deg, var(--ai), #6d28d9); }
.send:disabled { opacity: .4; }
.hint { max-width: 720px; margin: 8px auto 0; text-align: center; font-size: 11px; }
</style>
