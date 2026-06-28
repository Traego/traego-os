<script setup>
defineProps({ name: String, size: { type: [Number, String], default: 18 } })
// Stroke-based 24x24 icon set. Keys map to the module catalog + nav.
const P = {
  dashboard: '<rect x="3" y="3" width="7" height="9" rx="1.5"/><rect x="14" y="3" width="7" height="5" rx="1.5"/><rect x="14" y="12" width="7" height="9" rx="1.5"/><rect x="3" y="16" width="7" height="5" rx="1.5"/>',
  server: '<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>',
  cpu: '<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 2v3M15 2v3M9 19v3M15 19v3M2 9h3M2 15h3M19 9h3M19 15h3"/><rect x="9.5" y="9.5" width="5" height="5" rx="1"/>',
  sparkles: '<path d="M12 3l1.8 4.6L18.5 9.5 13.8 11.3 12 16 10.2 11.3 5.5 9.5 10.2 7.6 12 3z"/><path d="M5 16l.8 2 2 .8-2 .8L5 22l-.8-2.4-2-.8 2-.8L5 16z"/>',
  database: '<ellipse cx="12" cy="5" rx="8" ry="3"/><path d="M4 5v6c0 1.7 3.6 3 8 3s8-1.3 8-3V5"/><path d="M4 11v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/>',
  grid: '<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
  layers: '<path d="M12 3l9 5-9 5-9-5 9-5z"/><path d="M3 13l9 5 9-5"/>',
  agent: '<rect x="5" y="8" width="14" height="11" rx="3"/><path d="M12 8V4M12 4a1.5 1.5 0 100-3 1.5 1.5 0 000 3z"/><path d="M2 13v3M22 13v3"/><circle cx="9.5" cy="13" r="1.2"/><circle cx="14.5" cy="13" r="1.2"/>',
  update: '<path d="M21 12a9 9 0 11-2.6-6.4"/><path d="M21 4v5h-5"/>',
  bolt: '<path d="M13 2L4 14h7l-1 8 9-12h-7l1-8z"/>',
  storage: '<rect x="3" y="4" width="18" height="6" rx="1.5"/><rect x="3" y="14" width="18" height="6" rx="1.5"/><path d="M7 7h.01M7 17h.01M11 7h6M11 17h6"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  x: '<path d="M6 6l12 12M18 6L6 18"/>',
  check: '<path d="M20 6L9 17l-5-5"/>',
  alert: '<path d="M12 9v4M12 17h.01"/><path d="M10.3 3.9L2 18a2 2 0 001.7 3h16.6A2 2 0 0022 18L13.7 3.9a2 2 0 00-3.4 0z"/>',
  chevron: '<path d="M9 6l6 6-6 6"/>',
  gauge: '<path d="M12 13l4-4"/><path d="M3.5 18a9 9 0 1117 0"/><circle cx="12" cy="13" r="1.5"/>',
  users: '<circle cx="9" cy="8" r="3.2"/><path d="M3 20c0-3.3 2.7-5 6-5s6 1.7 6 5"/><path d="M16 5.5a3 3 0 010 5.8M21 20c0-2.6-1.6-4.2-4-4.7"/>',
  shield: '<path d="M12 3l8 3v6c0 4.5-3.2 7.5-8 9-4.8-1.5-8-4.5-8-9V6l8-3z"/>',
  power: '<path d="M12 3v9M7.5 6.5a7 7 0 109 0"/>',
  pulse: '<path d="M3 12h4l2-6 4 12 2-6h6"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  link: '<path d="M9 12h6"/><path d="M10 7H8a5 5 0 000 10h2M14 7h2a5 5 0 010 10h-2"/>',
  settings: '<circle cx="12" cy="12" r="3"/><path d="M19.4 13a7.8 7.8 0 000-2l2-1.5-2-3.4-2.3 1a7.8 7.8 0 00-1.7-1l-.4-2.5h-4l-.4 2.5a7.8 7.8 0 00-1.7 1l-2.3-1-2 3.4 2 1.5a7.8 7.8 0 000 2l-2 1.5 2 3.4 2.3-1a7.8 7.8 0 001.7 1l.4 2.5h4l.4-2.5a7.8 7.8 0 001.7-1l2.3 1 2-3.4-2-1.5z"/>',
  thermo: '<path d="M10 13.5V5a2 2 0 114 0v8.5a4 4 0 11-4 0z"/>',
  trend: '<path d="M3 17l6-6 4 4 8-8"/><path d="M21 7v5h-5"/>'
}
</script>

<template>
  <svg :width="size" :height="size" viewBox="0 0 24 24" fill="none"
       stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"
       v-html="P[name] || ''" />
</template>
