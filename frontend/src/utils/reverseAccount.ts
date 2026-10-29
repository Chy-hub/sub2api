// Canonical account fields shared with the reverse-provider persistence layer.
const credentialAliases = {
  access_token: ['access_token', 'accessToken', 'token', 'personal_token', 'personalToken', 'qoder_token', 'pat', 'api_key'],
  refresh_token: ['refresh_token', 'refreshToken'],
  uid: ['uid', 'user_id', 'userId', 'qoder_account_id'],
  user_type: ['user_type', 'qoder_user_type', 'userType'],
  device_token: ['device_token', 'deviceToken', 'workbuddy_device_token'],
  machine_token: ['machine_token', 'machineToken', 'qoder_machine_token'],
  machine_type: ['machine_type', 'machineType', 'qoder_machine_type'],
  machine_code: ['machine_code', 'machineCode', 'qoder_machine_code'],
  machine_id: ['machine_id', 'machineId', 'machineID'],
  machine_salt: ['machine_salt', 'machineSalt'],
  session_id: ['session_id', 'sessionId'],
  enterprise_id: ['enterprise_id', 'enterpriseId'],
  desktop_version: ['desktop_version', 'qoder_desktop_version'],
  user_agent: ['user_agent', 'workbuddy_user_agent'],
  client_version: ['client_version', 'workbuddy_client_version'],
  cli_version: ['cli_version', 'workbuddy_cli_version'],
  client_name: ['client_name', 'workbuddy_client_name'],
  nickname: ['nickname'],
  domain: ['domain']
} as const

function object(value: unknown): Record<string, unknown> | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : null
}

export function parseReverseCredentialImport(source: string, platform: string) {
  const parsed = object(JSON.parse(source))
  if (!parsed) throw new Error('invalidCredentialJSON')
  const flat: Record<string, unknown> = { ...object(parsed.auth), ...object(parsed.account), ...parsed }
  const vendor = typeof flat.vendor === 'string' ? flat.vendor.toLowerCase() : ''
  const family = platform.startsWith('qoder_') ? 'qoder' : 'workbuddy'
  const realm = platform.endsWith('_global') ? 'global' : 'cn'
  if (vendor && (!vendor.startsWith(family) || !vendor.endsWith(realm))) throw new Error('credentialPlatformMismatch')
  if (typeof flat.realm === 'string' && flat.realm.toLowerCase() !== realm) throw new Error('credentialPlatformMismatch')
  if (typeof flat.secret === 'string' && flat.secret.trim()) {
    try {
      const nested = object(JSON.parse(flat.secret))
      if (nested) for (const [key, value] of Object.entries(nested)) if (!flat[key]) flat[key] = value
    } catch { if (!flat.access_token) flat.access_token = flat.secret.trim() }
  }
  const result: Partial<Record<keyof typeof credentialAliases, string>> = {}
  for (const [key, aliases] of Object.entries(credentialAliases)) {
    const value = aliases.map(alias => flat[alias]).find(value => typeof value === 'string' && value.trim())
    if (typeof value === 'string') result[key as keyof typeof credentialAliases] = value.trim()
  }
  if (!result.access_token && result.device_token) result.access_token = result.device_token
  if (!result.access_token) throw new Error('invalidCredentialJSON')
  return result
}

export function parseReverseTaskHours(values: Record<string, string>): Record<string, number[]> {
  const result: Record<string, number[]> = {}
  for (const [kind, value] of Object.entries(values)) {
    if (!value.trim()) continue
    const tokens = value.trim().split(/[,，\s]+/)
    if (tokens.some(token => !/^\d{1,2}$/.test(token))) throw new Error('invalidTaskHours')
    const hours = tokens.map(Number)
    if (hours.some(hour => !Number.isInteger(hour) || hour < 0 || hour > 23)) throw new Error('invalidTaskHours')
    result[kind] = [...new Set(hours)]
  }
  return result
}
