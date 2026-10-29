import { describe, expect, it } from 'vitest'
import { parseReverseCredentialImport, parseReverseTaskHours } from '../reverseAccount'

describe('reverse account import and schedules', () => {
  it('imports a nested plugin export and only returns editable fields', () => {
    expect(parseReverseCredentialImport(JSON.stringify({ vendor: 'workbuddycn', auth: { accessToken: 'access', refreshToken: 'refresh', realm: 'cn' }, account: { uid: 'u1', enterpriseId: 'e1' }, device_token: 'device', unrecognized: 'ignored' }), 'workbuddy_cn')).toEqual({ access_token: 'access', refresh_token: 'refresh', uid: 'u1', enterprise_id: 'e1', device_token: 'device' })
  })
  it('validates schedule hours before JSON encoding', () => {
    expect(parseReverseTaskHours({ growth: '9,21', travel: ' 9 21 9 ', activity: '' })).toEqual({ growth: [9, 21], travel: [9, 21] })
    for (const raw of ['24', '-1', '9.5', 'nine', '9,']) expect(() => parseReverseTaskHours({ growth: raw })).toThrow('invalidTaskHours')
  })
})
