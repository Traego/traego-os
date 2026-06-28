<script setup>
import { computed, ref } from 'vue'
const props = defineProps({
  data: { type: Array, default: () => [] },
  color: { type: String, default: 'var(--brand)' },
  height: { type: Number, default: 38 },
  fill: { type: Boolean, default: true },
  suffix: { type: String, default: '' } // appended in the hover tooltip, e.g. "tok/s"
})
const W = 100

// One pass: each point's horizontal fraction (0..1) and vertical pixel position.
const coords = computed(() => {
  const d = props.data
  if (d.length < 2) return []
  const min = Math.min(...d), max = Math.max(...d), range = max - min || 1
  return d.map((v, i) => ({
    xFrac: i / (d.length - 1),
    yPx: props.height - 4 - ((v - min) / range) * (props.height - 8),
    v
  }))
})
const pts = computed(() => {
  const c = coords.value
  if (c.length < 2) return { line: '', area: '' }
  const line = c.map((p, i) => (i ? 'L' : 'M') + (p.xFrac * W).toFixed(1) + ' ' + p.yPx.toFixed(1)).join(' ')
  return { line, area: line + ` L${W} ${props.height} L0 ${props.height} Z` }
})
const uid = Math.random().toString(36).slice(2, 8)

// Hover: find the nearest point and surface its value.
const wrap = ref(null)
const hover = ref(null)
function onMove(e) {
  const c = coords.value
  if (c.length < 2 || !wrap.value) return
  const rect = wrap.value.getBoundingClientRect()
  const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width))
  const p = c[Math.round(frac * (c.length - 1))]
  hover.value = { xPx: p.xFrac * rect.width, yPx: p.yPx, v: p.v }
}
function fmt(v) {
  const n = Math.round(v).toLocaleString()
  return props.suffix ? `${n} ${props.suffix}` : n
}
</script>

<template>
  <div class="spark" ref="wrap" :style="{ height: height + 'px' }" @mousemove="onMove" @mouseleave="hover = null">
    <svg :viewBox="`0 0 ${W} ${height}`" preserveAspectRatio="none" :style="{width:'100%',height:height+'px',display:'block'}">
      <defs>
        <linearGradient :id="'g'+uid" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" :stop-color="color" stop-opacity="0.32" />
          <stop offset="100%" :stop-color="color" stop-opacity="0" />
        </linearGradient>
      </defs>
      <path v-if="fill" :d="pts.area" :fill="'url(#g'+uid+')'" />
      <path :d="pts.line" fill="none" :stroke="color" stroke-width="1.8" stroke-linejoin="round" stroke-linecap="round" vector-effect="non-scaling-stroke" />
    </svg>

    <template v-if="hover">
      <div class="sl-guide" :style="{ left: hover.xPx + 'px' }" />
      <div class="sl-dot" :style="{ left: hover.xPx + 'px', top: hover.yPx + 'px', background: color }" />
      <div class="sl-tip" :class="{ edgeL: hover.xPx < 36, edgeR: hover.xPx > 0 && wrap && hover.xPx > wrap.clientWidth - 36 }"
           :style="{ left: hover.xPx + 'px', top: (hover.yPx - 8) + 'px' }">{{ fmt(hover.v) }}</div>
    </template>
  </div>
</template>

<style scoped>
.spark { position: relative; width: 100%; }
.sl-guide { position: absolute; top: 0; bottom: 0; width: 1px; background: rgba(255,255,255,.18); transform: translateX(-0.5px); pointer-events: none; }
.sl-dot { position: absolute; width: 7px; height: 7px; border-radius: 50%; transform: translate(-50%,-50%); box-shadow: 0 0 0 2px var(--bg-1); pointer-events: none; }
.sl-tip { position: absolute; transform: translate(-50%, -100%); background: var(--bg-3); border: 1px solid var(--line-strong);
  border-radius: 6px; padding: 2px 7px; font-size: 11px; font-weight: 600; color: var(--tx-0); white-space: nowrap;
  pointer-events: none; font-family: var(--mono); z-index: 2; }
.sl-tip.edgeL { transform: translate(0, -100%); }
.sl-tip.edgeR { transform: translate(-100%, -100%); }
</style>
