<script setup>
import { onMounted, onUnmounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useController } from './stores/controller'
import { useUI } from './stores/ui'
import AppSidebar from './components/AppSidebar.vue'
import AppTopbar from './components/AppTopbar.vue'

const route = useRoute()
const controller = useController()
const ui = useUI()
let poll = null
onMounted(() => {
  controller.refresh()
  poll = setInterval(() => controller.refresh(), 2500)
})
onUnmounted(() => clearInterval(poll))
// navigating closes the mobile drawer
watch(() => route.fullPath, () => ui.closeSidebar())
</script>

<template>
  <!-- bare pages (e.g. the public /chat) render without the admin shell -->
  <router-view v-if="route.meta.bare" />

  <div v-else class="shell">
    <AppSidebar />
    <div v-if="ui.sidebarOpen" class="scrim" @click="ui.closeSidebar()" aria-hidden="true"/>
    <div class="main">
      <AppTopbar />
      <main class="content">
        <router-view v-slot="{ Component }">
          <transition name="fade" mode="out-in">
            <component :is="Component" />
          </transition>
        </router-view>
      </main>
    </div>
  </div>
</template>

<style scoped>
/* The sidebar is position:fixed (full height, never scrolls); offset the main
   column by its width. Below the shell breakpoint it's an off-canvas drawer. */
.shell { height: 100vh; }
.main { margin-left: var(--sidebar-w); height: 100vh; display: flex; flex-direction: column; min-width: 0; }
.content { flex: 1; overflow-y: auto; }
.scrim { display: none; }

@media (max-width: 880px) {
  .main { margin-left: 0; }
  .scrim { display: block; position: fixed; inset: 0; z-index: 40; background: rgba(4,6,10,.6); backdrop-filter: blur(2px); }
}
</style>
