<template>
  <div>
    <!-- Tags display -->
    <div class="flex flex-wrap gap-1.5 rounded-lg border border-gray-200 bg-white p-2 dark:border-dark-600 dark:bg-dark-800 min-h-[2.5rem]">
      <span
        v-for="(model, idx) in models"
        :key="idx"
        class="inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-sm"
        :class="getPlatformTagClass(props.platform || '')"
      >
        {{ model }}
        <button
          type="button"
          @click="removeModel(idx)"
          class="ml-0.5 rounded-full p-0.5 hover:bg-primary-200 dark:hover:bg-primary-800"
        >
          <Icon name="x" size="xs" />
        </button>
      </span>
      <input
        ref="inputRef"
        v-model="inputValue"
        type="text"
        autocomplete="off"
        role="combobox"
        aria-autocomplete="list"
        aria-haspopup="listbox"
        :aria-expanded="isListVisible"
        :aria-controls="listId"
        :aria-activedescendant="activeOptionId"
        class="flex-1 min-w-[120px] border-none bg-transparent text-sm outline-none placeholder:text-gray-400 dark:text-white"
        :placeholder="models.length === 0 ? placeholder : ''"
        @keydown.enter="handleEnter"
        @keydown.tab="handleTab"
        @keydown.backspace="handleBackspace"
        @keydown.down="handleArrowDown"
        @keydown.up="handleArrowUp"
        @keydown.esc="handleEscape"
        @paste="handlePaste"
        @focus="handleFocus"
        @blur="handleBlur"
      />
    </div>
    <p class="mt-1 text-xs text-gray-400">
      {{ hintText }}
    </p>
    <Teleport to="body">
      <div
        v-if="isListVisible"
        :id="listId"
        ref="listRef"
        class="fixed z-[100] max-h-64 overflow-y-auto rounded-lg border border-gray-200 bg-white py-1 shadow-lg dark:border-dark-600 dark:bg-dark-800"
        :style="listStyle"
        role="listbox"
        @mousedown.prevent
      >
        <button
          v-for="(item, idx) in filteredSuggestions"
          :id="optionId(idx)"
          :key="item"
          type="button"
          role="option"
          :aria-selected="idx === activeIndex"
          :data-active="idx === activeIndex"
          class="block w-full truncate px-3 py-1.5 text-left text-sm text-gray-700 hover:bg-primary-50 dark:text-gray-200 dark:hover:bg-dark-700"
          :class="{ 'bg-primary-50 dark:bg-dark-700': idx === activeIndex }"
          @click="selectSuggestion(item)"
        >
          {{ item }}
        </button>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { getPlatformTagClass } from './types'

const { t } = useI18n()

const props = defineProps<{
  models: string[]
  placeholder?: string
  platform?: string
  /** Optional candidate models to pick from; typing still accepts free-form names. */
  suggestions?: string[]
}>()

const emit = defineEmits<{
  'update:models': [models: string[]]
  'request-suggestions': []
}>()

// Cap the rendered list so large pricing catalogs stay responsive.
const SUGGESTION_LIMIT = 100

const listId = `model-tag-list-${useId()}`

const inputValue = ref('')
const inputRef = ref<HTMLInputElement>()
const listRef = ref<HTMLElement>()
const isOpen = ref(false)
const activeIndex = ref(-1)
const listStyle = ref<Record<string, string>>({})

const filteredSuggestions = computed(() => {
  const query = inputValue.value.trim().toLowerCase()
  const selected = new Set(props.models)
  return (props.suggestions ?? [])
    .filter(model => !selected.has(model))
    .filter(model => !query || model.toLowerCase().includes(query))
    .slice(0, SUGGESTION_LIMIT)
})

const isListVisible = computed(() => isOpen.value && filteredSuggestions.value.length > 0)

const activeOptionId = computed(() =>
  isListVisible.value && activeIndex.value >= 0 ? optionId(activeIndex.value) : undefined,
)

const hintText = computed(() =>
  (props.suggestions?.length ?? 0) > 0
    ? t(
        'admin.channels.form.modelInputHintSelectable',
        'Press Enter to add, paste for batch import, or pick from the list below.',
      )
    : t('admin.channels.form.modelInputHint', 'Press Enter to add, supports paste for batch import.'),
)

function optionId(index: number) {
  return `${listId}-option-${index}`
}

function addModel() {
  const val = inputValue.value.trim()
  if (!val) return
  if (!props.models.includes(val)) {
    emit('update:models', [...props.models, val])
  }
  inputValue.value = ''
  activeIndex.value = -1
}

function updateListPosition() {
  const el = inputRef.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const width = Math.max(rect.width, 240)
  const left = Math.min(rect.left, Math.max(8, window.innerWidth - width - 8))
  const maxHeight = 256
  const spaceBelow = window.innerHeight - rect.bottom
  const openUpward = spaceBelow < maxHeight && rect.top > spaceBelow
  listStyle.value = openUpward
    ? { left: `${left}px`, bottom: `${window.innerHeight - rect.top + 4}px`, width: `${width}px` }
    : { left: `${left}px`, top: `${rect.bottom + 4}px`, width: `${width}px` }
}

function scrollActiveIntoView() {
  const el = listRef.value?.querySelector<HTMLElement>('[data-active="true"]')
  // jsdom and older browsers may not implement scrollIntoView.
  el?.scrollIntoView?.({ block: 'nearest' })
}

function openList() {
  if (!props.suggestions?.length) return
  isOpen.value = true
  activeIndex.value = -1
  startListening()
  updateListPosition()
  void nextTick(updateListPosition)
}

function closeList() {
  isOpen.value = false
  activeIndex.value = -1
  stopListening()
}

function handleFocus() {
  // Let the parent lazily fetch candidates the first time the field is used.
  emit('request-suggestions')
  openList()
}

function handleBlur() {
  addModel()
  closeList()
}

function selectSuggestion(item: string) {
  if (!props.models.includes(item)) {
    emit('update:models', [...props.models, item])
  }
  inputValue.value = ''
  activeIndex.value = -1
  inputRef.value?.focus()
}

function handleEnter(event: KeyboardEvent) {
  if (event.isComposing) return
  event.preventDefault()
  if (isListVisible.value && activeIndex.value >= 0) {
    const item = filteredSuggestions.value[activeIndex.value]
    if (item) {
      selectSuggestion(item)
      return
    }
  }
  addModel()
}

function handleTab(event: KeyboardEvent) {
  if (event.isComposing) return
  if (isListVisible.value && activeIndex.value >= 0) {
    const item = filteredSuggestions.value[activeIndex.value]
    if (item) {
      event.preventDefault()
      selectSuggestion(item)
      return
    }
  }
  if (!inputValue.value.trim()) return
  event.preventDefault()
  addModel()
}

function handleEscape(event: KeyboardEvent) {
  // Only consume Escape while the list is open; otherwise let dialogs handle it.
  if (!isOpen.value) return
  event.preventDefault()
  event.stopPropagation()
  closeList()
}

function handleArrowDown(event: KeyboardEvent) {
  if (event.isComposing) return
  if (!isOpen.value) {
    if (!props.suggestions?.length) return
    event.preventDefault()
    openList()
    return
  }
  event.preventDefault()
  const max = filteredSuggestions.value.length - 1
  if (max < 0) return
  activeIndex.value = activeIndex.value >= max ? 0 : activeIndex.value + 1
  void nextTick(scrollActiveIntoView)
}

function handleArrowUp(event: KeyboardEvent) {
  if (event.isComposing) return
  if (!isOpen.value) {
    if (!props.suggestions?.length) return
    event.preventDefault()
    openList()
    return
  }
  event.preventDefault()
  const max = filteredSuggestions.value.length - 1
  if (max < 0) return
  activeIndex.value = activeIndex.value <= 0 ? max : activeIndex.value - 1
  void nextTick(scrollActiveIntoView)
}

function removeModel(idx: number) {
  const newModels = [...props.models]
  newModels.splice(idx, 1)
  activeIndex.value = -1
  emit('update:models', newModels)
}

function handleBackspace(event: KeyboardEvent) {
  if (event.isComposing) return
  if (inputValue.value === '' && props.models.length > 0) {
    removeModel(props.models.length - 1)
  }
}

function handlePaste(e: ClipboardEvent) {
  e.preventDefault()
  const text = e.clipboardData?.getData('text') || ''
  const items = text.split(/[,\n;]+/).map(s => s.trim()).filter(Boolean)
  if (items.length === 0) return
  const unique = [...new Set([...props.models, ...items])]
  emit('update:models', unique)
  inputValue.value = ''
  activeIndex.value = -1
}

watch(inputValue, () => {
  activeIndex.value = -1
  if (isOpen.value) {
    void nextTick(updateListPosition)
  } else if (document.activeElement === inputRef.value) {
    // Suggestions may arrive after focus; open as soon as they are available.
    openList()
  }
})

watch(
  () => props.models,
  () => {
    // Adding or removing tags can rewrap the flex container and move the input.
    activeIndex.value = -1
    if (isOpen.value) void nextTick(updateListPosition)
  },
  { deep: true },
)

watch(
  () => props.suggestions,
  suggestions => {
    if (!suggestions?.length) {
      closeList()
    } else if (document.activeElement === inputRef.value) {
      openList()
    }
  },
)

watch(filteredSuggestions, list => {
  if (activeIndex.value >= list.length) {
    activeIndex.value = list.length - 1
  }
  if (isOpen.value) void nextTick(updateListPosition)
})

function handleViewportChange() {
  if (isOpen.value) updateListPosition()
}

let listening = false

function startListening() {
  if (listening) return
  listening = true
  window.addEventListener('scroll', handleViewportChange, true)
  window.addEventListener('resize', handleViewportChange)
}

function stopListening() {
  if (!listening) return
  listening = false
  window.removeEventListener('scroll', handleViewportChange, true)
  window.removeEventListener('resize', handleViewportChange)
}

onBeforeUnmount(() => {
  stopListening()
})
</script>
