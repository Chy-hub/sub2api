<template>
  <Teleport to="body">
    <div v-if="content" ref="panel" role="tooltip" tabindex="0" data-testid="hourly-tooltip"
      class="fixed z-[60] flex flex-col overflow-hidden rounded-xl border border-gray-200 bg-white text-gray-700 shadow-xl dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200"
      :style="style" @mouseenter="hold" @mouseleave="reset" @focusin="hold" @focusout="reset">
      <div class="flex shrink-0 items-center justify-between gap-4 border-b border-gray-100 px-3 py-2.5 dark:border-dark-600">
        <strong class="text-xs font-semibold">{{ content.title }}</strong>
        <span class="whitespace-nowrap text-[11px] text-gray-400">{{ t('admin.ustcSpeed.hourly.models', { count: content.rows.length }) }}</span>
      </div>
      <div class="min-h-0 overflow-y-auto overscroll-contain px-3 py-1" data-testid="hourly-tooltip-scroll">
        <div v-for="row in content.rows" :key="row.key" class="border-b border-gray-100 py-2 last:border-0 dark:border-dark-700" data-testid="hourly-tooltip-row">
          <div class="flex items-start gap-2">
            <span class="mt-1 h-2 w-2 shrink-0 rounded-full" :style="{ backgroundColor: row.color }" />
            <span class="min-w-0 flex-1 break-all text-xs font-medium leading-4">{{ row.label }}</span>
            <span class="shrink-0 whitespace-nowrap text-xs font-semibold tabular-nums">{{ row.value }}</span>
          </div>
          <p class="ml-4 mt-0.5 text-[11px] text-gray-400">{{ row.detail }}</p>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getFloatingPanelPosition } from '@/utils/floatingPanel'

type Row = { key: string; label: string; value: string; detail: string; color: string }
const { t } = useI18n()
const panel = ref<HTMLElement | null>(null)
const content = ref<{ x: number; y: number; title: string; rows: Row[] } | null>(null)
let hideTimer: ReturnType<typeof setTimeout> | undefined
let held = false
const style = computed(() => {
  if (!content.value) return {}
  const { x, y } = content.value
  const position = getFloatingPanelPosition({ top: y, bottom: y, right: x }, window.innerWidth, window.innerHeight, { maxWidth: 460, viewportPadding: 12, gap: 10, maxHeightRatio: 0.75 })
  return { left: `${position.left}px`, top: position.top == null ? undefined : `${position.top}px`, bottom: position.bottom == null ? undefined : `${position.bottom}px`, width: `${position.width}px`, maxHeight: `${position.maxHeight}px` }
})
function clearHideTimer() { if (hideTimer !== undefined) { clearTimeout(hideTimer); hideTimer = undefined } }
function show(x: number, y: number, title: string, rows: Row[]) {
  clearHideTimer()
  held = false
  content.value = rows.length ? { x, y, title, rows } : null
}
function hold() { clearHideTimer(); held = true }
function reset() { clearHideTimer(); held = false; content.value = null }
function hide() {
  if (held) return
  clearHideTimer()
  // Allow moving from the canvas into the scrollable tooltip.
  hideTimer = setTimeout(reset, 180)
}
function onScroll(event: Event) {
  if (event.target instanceof Node && panel.value?.contains(event.target)) return
  reset()
}
function onKeydown(event: KeyboardEvent) { if (event.key === 'Escape') reset() }
onMounted(() => {
  window.addEventListener('scroll', onScroll, true)
  window.addEventListener('resize', reset)
  window.addEventListener('keydown', onKeydown)
})
onUnmounted(() => {
  clearHideTimer()
  window.removeEventListener('scroll', onScroll, true)
  window.removeEventListener('resize', reset)
  window.removeEventListener('keydown', onKeydown)
})
defineExpose({ show, hide, reset })
</script>
