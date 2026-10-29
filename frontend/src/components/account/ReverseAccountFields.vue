<template>
  <div class="space-y-4">
    <div class="flex items-center gap-2 text-sm font-medium text-gray-800 dark:text-gray-200">
      <span class="rounded bg-gray-100 px-2 py-0.5 text-xs dark:bg-dark-600">
        {{ providerLabel }}
      </span>
      <span>{{ t('admin.accounts.reverse.title') }}</span>
    </div>

    <div v-if="!editing" class="flex gap-2">
      <button
        type="button"
        class="flex-1 rounded-lg px-3 py-2 text-sm font-medium transition"
        :class="tab === 'login'
          ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-400'
          : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400'"
        @click="tab = 'login'"
      >
        {{ t('admin.accounts.reverse.loginTab') }}
      </button>
      <button
        type="button"
        class="flex-1 rounded-lg px-3 py-2 text-sm font-medium transition"
        :class="tab === 'manual'
          ? 'bg-primary-100 text-primary-700 dark:bg-primary-900/30 dark:text-primary-400'
          : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-dark-600 dark:text-gray-400'"
        @click="selectManual"
      >
        {{ t('admin.accounts.reverse.manualTab') }}
      </button>
    </div>

    <div v-if="tab === 'login' && !editing" class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.accounts.reverse.loginHint') }}
      </p>
      <div class="flex flex-wrap items-center gap-2">
        <button type="submit" class="btn btn-primary" :disabled="loginBusy" @click.prevent="startLogin">
          {{ loginURL ? t('admin.accounts.reverse.restartLogin') : t('admin.accounts.reverse.startLogin') }}
        </button>
        <a
          v-if="loginURL"
          :href="loginURL"
          target="_blank"
          rel="noopener noreferrer"
          class="btn btn-secondary"
        >
          {{ t('admin.accounts.reverse.openLoginPage') }}
        </a>
      </div>
      <p v-if="loginURL && !loginDone" class="text-xs text-amber-600 dark:text-amber-400">
        {{ t('admin.accounts.reverse.waiting') }}
      </p>
      <p v-if="loginDone" class="text-xs text-emerald-600 dark:text-emerald-400">
        {{ t('admin.accounts.reverse.loginSuccess') }}
        <span v-if="profileLabel">— {{ profileLabel }}</span>
      </p>
      <p v-if="loginError" class="text-xs text-red-600 dark:text-red-400">{{ loginError }}</p>
    </div>

    <div v-if="tab === 'manual' || editing" class="space-y-3">
	  <details class="rounded-lg border border-gray-200 p-3 text-sm dark:border-dark-600">
	    <summary class="cursor-pointer">{{ t('admin.accounts.reverse.importCredentials') }}</summary>
	    <p class="input-hint mt-2">{{ t('admin.accounts.reverse.importCredentialsHint') }}</p>
	    <textarea v-model="credentialJSON" class="input mt-2 font-mono text-xs" rows="4" autocomplete="off" spellcheck="false" />
	    <button type="button" class="btn btn-secondary mt-2" @click="importCredentials">{{ t('admin.accounts.reverse.applyCredentials') }}</button>
	    <p v-if="importError" class="mt-2 text-xs text-red-600" role="alert">{{ importError }}</p>
	  </details>
      <p v-if="!editing" class="text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.reverse.manualHint') }}
      </p>
      <div>
        <label class="input-label">{{ t('admin.accounts.reverse.accessToken') }}</label>
        <input
          v-model="form.access_token"
          type="password"
          class="input font-mono"
          :placeholder="savedPlaceholder('access_token')"
          :required="!editing && tab === 'manual'"
        />
      </div>
      <div>
        <label class="input-label">{{ t('admin.accounts.reverse.refreshToken') }}</label>
        <input
          v-model="form.refresh_token"
          type="password"
          class="input font-mono"
          :placeholder="savedPlaceholder('refresh_token')"
        />
      </div>
    </div>

    <div class="rounded-lg border border-gray-200 dark:border-dark-600">
      <button
        type="button"
        class="flex w-full items-center justify-between px-4 py-3 text-left text-sm font-medium text-gray-700 dark:text-gray-300"
        @click="showAdvanced = !showAdvanced"
      >
        <span>{{ t('admin.accounts.reverse.advanced') }}</span>
        <span class="text-xs text-gray-400">{{ showAdvanced ? '−' : '+' }}</span>
      </button>
      <div v-if="showAdvanced" class="space-y-3 border-t border-gray-200 px-4 py-4 dark:border-dark-600">
        <p class="text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.reverse.advancedHint') }}
        </p>
		<div>
		  <label class="input-label">{{ t('admin.accounts.reverse.desktopVersion') }}</label>
		  <input v-model="form.desktop_version" type="text" class="input font-mono" :placeholder="provider === 'qoder' ? '0.4.8' : '5.8.3'" />
		</div>
		<p v-if="provider === 'qoder'" class="input-hint">{{ t('admin.accounts.reverse.machinePairHint') }}</p>
		<button v-if="provider === 'qoder' && editing && credentialsStatus?.has_machine_token" type="button" class="text-xs text-red-600" @click="clearMachineIdentity">{{ t(form.clear_machine_identity ? 'admin.accounts.reverse.machineIdentityCleared' : 'admin.accounts.reverse.clearMachineIdentity') }}</button>
        <div v-if="provider === 'qoder'" class="grid gap-3 sm:grid-cols-2">
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.machineToken') }}</label>
            <input v-model="form.machine_token" type="password" class="input font-mono" :placeholder="savedPlaceholder('machine_token')" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.machineType') }}</label>
            <input v-model="form.machine_type" type="text" class="input font-mono" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.machineCode') }}</label>
            <input v-model="form.machine_code" type="text" class="input font-mono" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.machineID') }}</label>
            <input v-model="form.machine_id" type="text" class="input font-mono" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.uid') }}</label>
            <input v-model="form.uid" type="text" class="input font-mono" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.userType') }}</label>
            <input v-model="form.user_type" type="text" class="input font-mono" placeholder="personal_standard" />
          </div>
        </div>
        <div v-else class="grid gap-3 sm:grid-cols-2">
		  <div>
		    <label class="input-label">{{ t('admin.accounts.reverse.machineSalt') }}</label>
		    <input v-model="form.machine_salt" type="password" class="input font-mono" :placeholder="savedPlaceholder('machine_salt')" />
		  </div>
		  <div v-for="field in clientFields" :key="field.key">
		    <label class="input-label">{{ t(`admin.accounts.reverse.${field.label}`) }}</label>
		    <input v-model="form[field.key]" type="text" class="input font-mono" :placeholder="field.placeholder" />
		  </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.deviceToken') }}</label>
            <input v-model="form.device_token" type="password" class="input font-mono" :placeholder="savedPlaceholder('device_token')" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.uid') }}</label>
            <input v-model="form.uid" type="text" class="input font-mono" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.machineID') }}</label>
            <input v-model="form.machine_id" type="text" class="input font-mono" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.sessionID') }}</label>
            <input v-model="form.session_id" type="text" class="input font-mono" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.enterpriseID') }}</label>
            <input v-model="form.enterprise_id" type="text" class="input font-mono" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.nickname') }}</label>
            <input v-model="form.nickname" type="text" class="input" />
          </div>
          <div>
            <label class="input-label">{{ t('admin.accounts.reverse.domain') }}</label>
            <input v-model="form.domain" type="text" class="input font-mono" />
          </div>
        </div>
      </div>
    </div>

    <div v-if="provider === 'workbuddy'" class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
	  <div class="flex items-center justify-between gap-4">
	    <div><label class="input-label mb-0">{{ t('admin.accounts.reverse.sanitizeFingerprints') }}</label><p class="input-hint">{{ t('admin.accounts.reverse.sanitizeHint') }}</p></div>
	    <Toggle v-model="form.sanitize_fingerprints" :aria-label="t('admin.accounts.reverse.sanitizeFingerprints')" />
	  </div>
	  <div class="flex items-center justify-between gap-4">
	    <div><label class="input-label mb-0">{{ t('admin.accounts.reverse.passthroughIP') }}</label><p class="input-hint">{{ t('admin.accounts.reverse.passthroughIPHint') }}</p></div>
	    <Toggle v-model="form.passthrough_ip" :aria-label="t('admin.accounts.reverse.passthroughIP')" />
	  </div>
      <div>
        <label class="input-label">{{ t('admin.accounts.reverse.promptMode') }}</label>
        <select v-model="form.prompt_mode" class="input">
          <option value="passthrough">{{ t('admin.accounts.reverse.promptModePassthrough') }}</option>
          <option value="custom">{{ t('admin.accounts.reverse.promptModeCustom') }}</option>
          <option value="append">{{ t('admin.accounts.reverse.promptModeAppend') }}</option>
        </select>
      </div>
      <div v-if="form.prompt_mode !== 'passthrough'">
        <label class="input-label">{{ t('admin.accounts.reverse.promptText') }}</label>
        <textarea v-model="form.prompt_text" rows="4" class="input" />
        <p class="input-hint">{{ t('admin.accounts.reverse.promptTextHint') }}</p>
      </div>
    </div>

    <div class="flex items-center justify-between gap-4 rounded-lg border border-gray-200 px-4 py-3 dark:border-dark-600">
      <div>
        <label class="input-label mb-0">{{ t('admin.accounts.reverse.autoCheckin') }}</label>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.reverse.autoCheckinHint') }}
        </p>
      </div>
      <Toggle v-model="form.auto_checkin" :aria-label="t('admin.accounts.reverse.autoCheckin')" />
    </div>

    <div v-if="checkinState" class="rounded-lg bg-gray-50 px-4 py-3 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-400">
      <p class="font-medium">{{ t('admin.accounts.reverse.lastCheckin') }}</p>
      <p class="mt-1">{{ checkinSummary }}</p>
    </div>
	<div v-if="provider === 'workbuddy'" class="flex items-center justify-between gap-4 rounded-lg border border-gray-200 px-4 py-3 dark:border-dark-600">
	  <div><label class="input-label mb-0">{{ t('admin.accounts.reverse.autoTasks') }}</label><p class="input-hint">{{ t('admin.accounts.reverse.autoTasksHint') }}</p></div>
	  <Toggle v-model="form.auto_tasks" :aria-label="t('admin.accounts.reverse.autoTasks')" />
	</div>
	<div v-if="provider === 'workbuddy' && form.auto_tasks" class="space-y-2 rounded-lg bg-gray-50 p-3 dark:bg-dark-700">
	  <p class="input-hint">{{ t('admin.accounts.reverse.scheduleHint') }}</p>
	  <div v-for="task in scheduledTasks" :key="task.kind" class="flex flex-wrap items-center gap-3 text-xs">
	    <label class="flex min-w-28 items-center gap-2"><input v-model="form.task_kinds" type="checkbox" :value="task.kind" />{{ t(`admin.accounts.reverse.taskKinds.${task.kind}`) }}</label>
	    <span v-if="task.afterCheckin" class="text-gray-500">{{ t('admin.accounts.reverse.afterCheckin') }}</span>
	    <input v-else v-model="form.task_hours[task.kind]" class="input max-w-40 font-mono" type="text" :placeholder="task.hours" />
	  </div>
	</div>
	<div class="flex items-center justify-between gap-4 rounded-lg border border-gray-200 px-4 py-3 dark:border-dark-600">
	  <div><label class="input-label mb-0">{{ t('admin.accounts.reverse.autoRefresh') }}</label><p class="input-hint">{{ t('admin.accounts.reverse.autoRefreshHint') }}</p></div>
	  <Toggle v-model="form.auto_refresh" :aria-label="t('admin.accounts.reverse.autoRefresh')" />
	</div>
	<ReverseAccountOperations v-if="editing && accountId" :key="accountId" :account-id="accountId" :platform="platform" />

    <div v-if="editing" class="flex flex-wrap items-center gap-2">
      <button type="button" class="btn btn-secondary" :disabled="loginBusy" @click.prevent="startLogin">
        {{ t('admin.accounts.reverse.reLogin') }}
      </button>
      <a
        v-if="loginURL"
        :href="loginURL"
        target="_blank"
        rel="noopener noreferrer"
        class="btn btn-secondary"
      >
        {{ t('admin.accounts.reverse.openLoginPage') }}
      </a>
      <span v-if="loginURL && !loginDone" class="text-xs text-amber-600 dark:text-amber-400">
        {{ t('admin.accounts.reverse.waiting') }}
      </span>
      <span v-if="loginDone" class="text-xs text-emerald-600 dark:text-emerald-400">
        {{ t('admin.accounts.reverse.loginSuccess') }}
      </span>
      <span v-if="loginError" class="text-xs text-red-600 dark:text-red-400">{{ loginError }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import ReverseAccountOperations from './ReverseAccountOperations.vue'
import { reverseProviderOf } from '@/constants/platformCatalog'
import { pollReverseLogin, startReverseLogin } from '@/api/admin/accounts'
import type { ReverseLoginProfile } from '@/api/admin/accounts'
import { parseReverseCredentialImport } from '@/utils/reverseAccount'

export interface ReverseFormModel {
	desktop_version: string
	machine_salt: string
	clear_machine_identity: boolean
	user_agent: string
	client_version: string
	cli_version: string
	client_name: string
	sanitize_fingerprints: boolean
	passthrough_ip: boolean
	auto_tasks: boolean
	auto_refresh: boolean
	task_kinds: string[]
	task_hours: Record<string, string>
  access_token: string
  refresh_token: string
  uid: string
  machine_id: string
  machine_token: string
  machine_type: string
  machine_code: string
  user_type: string
  device_token: string
  session_id: string
  enterprise_id: string
  nickname: string
  domain: string
  auto_checkin: boolean
  prompt_mode: 'passthrough' | 'custom' | 'append'
  prompt_text: string
}

const props = defineProps<{
  platform: string
  accountId?: number
  proxyId?: number | null
  editing?: boolean
  credentialsStatus?: Record<string, boolean> | null
  checkin?: Record<string, unknown> | null
}>()

const form = defineModel<ReverseFormModel>({ required: true })
const emit = defineEmits<{
  (event: 'login-success', payload: { ticket: string; profile?: ReverseLoginProfile }): void
  (event: 'relogin-success'): void
  (event: 'login-reset'): void
}>()

const { t } = useI18n()
const clientFields = [
  { key: 'user_agent', label: 'userAgent', placeholder: '' },
  { key: 'client_version', label: 'clientVersion', placeholder: '5.8.3' },
  { key: 'cli_version', label: 'cliVersion', placeholder: '2.161.0' },
  { key: 'client_name', label: 'clientName', placeholder: 'WorkBuddy' }
] as const
const tab = ref<'login' | 'manual'>('login')
const showAdvanced = ref(false)
const loginURL = ref('')
const loginBusy = ref(false)
const loginDone = ref(false)
const loginError = ref('')
const credentialJSON = ref('')
const importError = ref('')
const profile = ref<ReverseLoginProfile | null>(null)
let pollTimer: ReturnType<typeof setTimeout> | null = null
let pollGeneration = 0

const provider = computed(() => reverseProviderOf(props.platform) || 'qoder')
const scheduledTasks = computed(() => props.platform.endsWith('_global')
  ? [{ kind: 'dailybonus', hours: '8,12,16,20', afterCheckin: false }]
  : [
      { kind: 'growth', hours: '9,21', afterCheckin: false },
      { kind: 'travel', hours: '9,21', afterCheckin: false },
      { kind: 'activity', hours: '10', afterCheckin: false },
      { kind: 'keepalive', hours: '22', afterCheckin: false },
      { kind: 'blackcat', hours: '23', afterCheckin: false },
      { kind: 'school', hours: '', afterCheckin: true },
      { kind: 'streak', hours: '', afterCheckin: true }
    ])
const providerLabel = computed(() => {
  const labels: Record<string, string> = { qoder: 'Qoder', workbuddy: 'WorkBuddy' }
  return labels[provider.value] || provider.value
})
const profileLabel = computed(() => {
  const value = profile.value
  if (!value) return ''
  return value.nickname || value.email || value.uid || ''
})
const checkinState = computed(() => props.checkin || null)
const checkinSummary = computed(() => {
  const state = checkinState.value
  if (!state) return t('admin.accounts.reverse.neverCheckin')
  const parts: string[] = []
  if (typeof state.last_attempt_at === 'string') parts.push(state.last_attempt_at)
  if (typeof state.status === 'string') parts.push(state.status)
  if (typeof state.reward === 'string' && state.reward) parts.push(state.reward)
  if (typeof state.message === 'string' && state.message) parts.push(state.message)
  return parts.join(' · ') || t('admin.accounts.reverse.neverCheckin')
})

function savedPlaceholder(key: string): string {
	if (key==='machine_token' && form.value.clear_machine_identity) return ''
  return props.credentialsStatus?.[`has_${key}`] ? '••••••••' : ''
}

function clearMachineIdentity() {
  form.value.machine_token=''
  form.value.machine_type=''
  form.value.machine_code=''
  form.value.clear_machine_identity=true
}

function importCredentials() {
  importError.value = ''
  try {
    const fields = parseReverseCredentialImport(credentialJSON.value, props.platform)
    Object.assign(form.value, fields)
	emit('login-reset')
    credentialJSON.value = ''
    stopPolling()
    loginDone.value = false
    loginURL.value = ''
    loginBusy.value = false
    showAdvanced.value = true
  } catch (error) {
    const key = error instanceof Error && error.message === 'credentialPlatformMismatch' ? 'credentialPlatformMismatch' : 'invalidCredentialJSON'
    importError.value = t(`admin.accounts.reverse.${key}`)
  }
}

function selectManual() {
  tab.value = 'manual'
  stopPolling()
  loginBusy.value = false
  loginDone.value = false
  loginURL.value = ''
  emit('login-reset')
}

function stopPolling() {
	pollGeneration += 1
  if (pollTimer) {
    clearTimeout(pollTimer)
    pollTimer = null
  }
}

function poll(sessionId: string) {
	const generation=pollGeneration
  pollTimer = setTimeout(async () => {
    try {
      const result = await pollReverseLogin(sessionId)
	  if (generation!==pollGeneration) return
      if (result.pending) {
        poll(sessionId)
        return
      }
      loginBusy.value = false
      loginDone.value = true
      if (result.profile) profile.value = result.profile
      if (result.login_ticket) {
        emit('login-success', { ticket: result.login_ticket, profile: result.profile })
      }
      if (result.account) {
        emit('relogin-success')
      }
    } catch (error) {
	  if (generation!==pollGeneration) return
      loginBusy.value = false
      loginError.value = error instanceof Error ? error.message : String(error)
    }
  }, 2000)
}

async function startLogin() {
  stopPolling()
  const generation = pollGeneration
  emit('login-reset')
  loginBusy.value = true
  loginDone.value = false
  loginError.value = ''
  loginURL.value = ''
  try {
    const result = await startReverseLogin({
      platform: props.platform,
      account_id: props.accountId,
      proxy_id: props.proxyId ?? undefined
    })
    if (generation !== pollGeneration) return
    loginURL.value = result.login_url
    window.open(result.login_url, '_blank', 'noopener,noreferrer')
    poll(result.session_id)
  } catch (error) {
    if (generation !== pollGeneration) return
    loginBusy.value = false
    loginError.value = error instanceof Error ? error.message : String(error)
  }
}

onBeforeUnmount(stopPolling)
</script>
