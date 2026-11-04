<template>
  <span class="skill-icon" :style="iconStyle" aria-hidden="true">
    <img v-if="imageURL && !imageFailed" :src="imageURL" alt="" @error="imageFailed = true" />
    <component :is="presetIcon" v-else-if="presetIcon" />
    <span v-else class="skill-initial">{{ firstCharacter }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { BulbOutlined, ThunderboltOutlined, StarOutlined, CalendarOutlined, WarningOutlined, ClockCircleOutlined } from '@ant-design/icons-vue'
import { SKILL_ICON_PRESETS, safeSkillURL } from '@/views/skills/icons'
import { THEME_COLORS } from '@/config/theme'

const props = defineProps<{ icon: string; name: string }>()
const imageFailed = ref(false)
watch(() => props.icon, () => { imageFailed.value = false })
const preset = computed(() => SKILL_ICON_PRESETS.find(item => item.value === props.icon))
const imageURL = computed(() => preset.value?.image || safeSkillURL(props.icon))
// 已保存的旧预设仍按原图标展示，新建技能使用 SVG 预设。
const legacyPresets = [
  { value: 'teambuddy:skill:spark', color: '#3157E2', icon: BulbOutlined },
  { value: 'teambuddy:skill:bolt', color: '#16A34A', icon: ThunderboltOutlined },
  { value: 'teambuddy:skill:star', color: '#F58A00', icon: StarOutlined },
  { value: 'teambuddy:skill:calendar', color: '#722ED1', icon: CalendarOutlined },
  { value: 'teambuddy:skill:alert', color: '#FB363F', icon: WarningOutlined },
  { value: 'teambuddy:skill:clock', color: '#13B8B8', icon: ClockCircleOutlined },
]
const legacyPreset = computed(() => legacyPresets.find(item => item.value === props.icon))
const presetIcon = computed(() => legacyPreset.value?.icon)
const firstCharacter = computed(() => {
  const character = Array.from(props.name.trim())[0] || '✧'
  return /^[a-z]$/.test(character) ? character.toUpperCase() : character
})
const iconStyle = computed(() => ({
  color: legacyPreset.value ? '#FFFFFF' : THEME_COLORS.primary,
  background: preset.value ? 'transparent' : legacyPreset.value?.color || THEME_COLORS.primarySoft,
}))
</script>

<style scoped>
.skill-icon { display: inline-flex; align-items: center; justify-content: center; width: 52px; height: 52px; flex: 0 0 52px; border-radius: 12px; overflow: hidden; font-size: 26px; font-weight: 500; }
.skill-icon img { width: 100%; height: 100%; object-fit: cover; }
.skill-initial { font-size: 16px; }
</style>
