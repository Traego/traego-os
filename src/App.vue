<script setup>
import { onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { useController } from './stores/controller'
import AppSidebar from './components/AppSidebar.vue'
import AppTopbar from './components/AppTopbar.vue'

const route = useRoute()
const controller = useController()
let poll = null
onMounted(() => {
  controller.refresh()
  poll = setInterval(() => controller.refresh(), 2500)
})
onUnmounted(() => clearInterval(poll))
</script>

<template>
  <!-- bare pages (e.g. the public /chat) render without the admin shell -->
  <router-view v-if="route.meta.bare" />

  <div v-else class="shell">
    <AppSidebar />
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
   column by its width. */
.shell { height: 100vh; }
.main { margin-left: var(--sidebar-w); height: 100vh; display: flex; flex-direction: column; min-width: 0; }
.content { flex: 1; overflow-y: auto; }
</style>
