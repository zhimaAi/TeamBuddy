<script setup lang="ts">
import skillIcon from '@/assets/icons/onboarding-spark.svg'
/**
 * Left navigation menu icon (line style, color follows currentColor)
 * Shared by the sidebar and tool center to keep icons consistent.
 * A single root <span> carries the class passed in by the parent; the svg has a built-in 18x18 default size,
 * overridable by the parent via class (e.g. .nav-icon).
 */
defineProps<{ name: string }>()
</script>

<template>
  <span class="menu-icon">
    <img v-if="name === 'skills'" :src="skillIcon" width="18" height="18" alt="" aria-hidden="true" />
    <!-- Workbench -->
    <svg v-else-if="name === 'dashboard'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <rect x="2" y="2" width="6" height="6" rx="1.2" stroke="currentColor" stroke-width="1.3"/>
      <rect x="10" y="2" width="6" height="6" rx="1.2" stroke="currentColor" stroke-width="1.3"/>
      <rect x="2" y="10" width="6" height="6" rx="1.2" stroke="currentColor" stroke-width="1.3"/>
      <rect x="10" y="10" width="6" height="6" rx="1.2" stroke="currentColor" stroke-width="1.3"/>
    </svg>
    <!-- Workflow -->
    <svg v-else-if="name === 'workflow'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <path d="M2.5 9a6.5 6.5 0 0 1 11-4.6M15.5 9a6.5 6.5 0 0 1-11 4.6" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
      <path d="M13.5 2v3.4h-3.4M4.5 16v-3.4h3.4" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
    </svg>
    <!-- Command (terminal) -->
    <svg v-else-if="name === 'command'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <rect x="2.2" y="3.2" width="13.6" height="11.6" rx="1.8" stroke="currentColor" stroke-width="1.3"/>
      <path d="M5.2 6.6l2.4 2.2-2.4 2.2M8.8 11.2h4" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
    </svg>
    <!-- Task notifications -->
    <svg v-else-if="name === 'task'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <path d="M5 3.2h8a1.8 1.8 0 0 1 1.8 1.8v10H5A1.8 1.8 0 0 1 3.2 13.2V5A1.8 1.8 0 0 1 5 3.2Z" stroke="currentColor" stroke-width="1.3"/>
      <path d="M6.2 7h5.6M6.2 10h4.2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
    </svg>
    <!-- Agent pipeline -->
    <svg v-else-if="name === 'agent'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <circle cx="4" cy="9" r="2" stroke="currentColor" stroke-width="1.3"/>
      <circle cx="14" cy="4" r="2" stroke="currentColor" stroke-width="1.3"/>
      <circle cx="14" cy="14" r="2" stroke="currentColor" stroke-width="1.3"/>
      <path d="M6 8.4 12 4.7M6 9.6l6 3.7" stroke="currentColor" stroke-width="1.3"/>
    </svg>
    <!-- Local projects -->
    <svg v-else-if="name === 'project'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <path d="M2.2 5.4h5l1.3 1.5h7.3v6.8a1.6 1.6 0 0 1-1.6 1.6H3.8a1.6 1.6 0 0 1-1.6-1.6V5.4Z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>
      <path d="M2.7 5.4V4.2a1.5 1.5 0 0 1 1.5-1.5h3l1.3 1.5h5a1.5 1.5 0 0 1 1.5 1.5v1.2" stroke="currentColor" stroke-width="1.3"/>
    </svg>
    <!-- Knowledge base -->
    <svg v-else-if="name === 'book'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <path d="M9 4.1C7.3 2.7 5.1 2.3 2.5 2.3v11.9c2.6 0 4.8.4 6.5 1.8 1.7-1.4 3.9-1.8 6.5-1.8V2.3c-2.6 0-4.8.4-6.5 1.8Z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>
      <path d="M9 4.1v11.9" stroke="currentColor" stroke-width="1.3"/>
    </svg>
    <!-- API management -->
    <svg v-else-if="name === 'remote'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <path d="M9 10.3v5M6.7 15.3h4.6M5.3 6.7a5 5 0 0 0 0 4.2m7.4-4.2a5 5 0 0 1 0 4.2M3 4.4a8.3 8.3 0 0 0 0 8.8m12-8.8a8.3 8.3 0 0 1 0 8.8" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
      <circle cx="9" cy="8.4" r="1.4" fill="currentColor"/>
    </svg>
    <svg v-else-if="name === 'api'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <path d="M3.2 7.2h4l1.6-2.7 1.8 5 1.6-2.3h2.6" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
      <circle cx="3.2" cy="7.2" r="1.2" stroke="currentColor" stroke-width="1.3"/>
      <circle cx="14.8" cy="7.2" r="1.2" stroke="currentColor" stroke-width="1.3"/>
      <circle cx="8.8" cy="10.6" r="1.2" stroke="currentColor" stroke-width="1.3"/>
    </svg>
    <!-- Tool center -->
    <svg v-else-if="name === 'toolbox'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <path d="M14.8 7.2v-2.2a2.2 2.2 0 0 0-2.2-2.2h-7.2a2.2 2.2 0 0 0-2.2 2.2v2.2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
      <rect x="1.4" y="7.2" width="15.2" height="7.6" rx="1.6" stroke="currentColor" stroke-width="1.3"/>
      <path d="M7.2 3.2v2.2M10.8 3.2v2.2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
    </svg>
    <!-- Config center -->
    <svg v-else-if="name === 'settings'" width="18" height="18" viewBox="0 0 18 18" fill="none">
      <circle cx="9" cy="9" r="2.4" stroke="currentColor" stroke-width="1.3"/>
      <path d="M9 2v2.2M9 13.8V16M2 9h2.2M13.8 9H16M3.9 3.9l1.6 1.6M12.5 12.5l1.6 1.6M14.1 3.9l-1.6 1.6M5.5 12.5l-1.6 1.6" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
    </svg>
    <!-- Fallback placeholder for unknown icons -->
    <svg v-else width="18" height="18" viewBox="0 0 18 18" fill="none">
      <circle cx="9" cy="9" r="6.8" stroke="currentColor" stroke-width="1.3" stroke-dasharray="2 2"/>
    </svg>
  </span>
</template>

<style scoped>
.menu-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  line-height: 1;
  vertical-align: middle;
}

.menu-icon svg,
.menu-icon img {
  display: block;
}
</style>
