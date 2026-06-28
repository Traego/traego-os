import { createRouter, createWebHashHistory } from 'vue-router'

const routes = [
  { path: '/', name: 'dashboard', component: () => import('../views/DashboardView.vue'), meta: { title: 'Dashboard' } },
  { path: '/hardware', name: 'hardware', component: () => import('../views/HardwareView.vue'), meta: { title: 'Hardware' } },
  { path: '/ai', name: 'ai', component: () => import('../views/AIView.vue'), meta: { title: 'AI' } },
  { path: '/chat', name: 'chat', component: () => import('../views/ChatView.vue'), meta: { title: 'Chat', bare: true } },
  // anything else -> dashboard
  { path: '/:pathMatch(.*)*', redirect: '/' }
]

export default createRouter({
  history: createWebHashHistory(),
  routes,
  scrollBehavior() { return { top: 0 } }
})
