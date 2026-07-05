import { defineStore } from 'pinia'

// Shell UI state. The sidebar is fixed on desktop; below the `--bp-shell`
// breakpoint (880px) it becomes an off-canvas drawer driven by this flag.
export const useUI = defineStore('ui', {
  state: () => ({ sidebarOpen: false }),
  actions: {
    toggleSidebar() { this.sidebarOpen = !this.sidebarOpen },
    closeSidebar() { this.sidebarOpen = false }
  }
})
