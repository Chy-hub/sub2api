import { describe, expect, it } from 'vitest'
import { usageGenerationTPS, usageOverallTPS, type SpeedRecord } from '../tokenSpeed'
import { csvCell, spreadsheetCell } from '../spreadsheet'

const record = (changes: Partial<SpeedRecord> = {}): SpeedRecord => ({ output_tokens: 2, duration_ms: 1000, first_token_ms: 200, stream: true, image_count: 0, billing_mode: 'token', ...changes } as SpeedRecord)

describe('shared usage and export speed calculations', () => {
  it('uses the same formula as benchmark analysis, including tail time', () => {
    expect(usageGenerationTPS(record())).toBe(2.5)
    expect(usageOverallTPS(record())).toBe(2)
    expect(usageGenerationTPS(record({ stream: false }))).toBeNull()
    expect(usageOverallTPS(record({ stream: false }))).toBe(2)
  })
  it.each([0, 1, NaN, Infinity])('does not invent TPS for invalid/short output %s', (output_tokens) => {
    expect(usageGenerationTPS(record({ output_tokens }))).toBeNull()
    expect(usageOverallTPS(record({ output_tokens }))).toBeNull()
  })
  it('keeps invalid timing, image calls and unobserved generation speed unavailable', () => {
    expect(usageGenerationTPS(record({ first_token_ms: null }))).toBeNull()
    expect(usageGenerationTPS(record({ first_token_ms: 1000 }))).toBeNull()
    expect(usageGenerationTPS(record({ first_token_ms: -1 }))).toBeNull()
    expect(usageOverallTPS(record({ duration_ms: 0 }))).toBeNull()
    expect(usageOverallTPS(record({ image_count: 1, billing_mode: 'image' }))).toBeNull()
  })
})

describe('spreadsheet exports', () => {
  it.each(['=1+1', '+cmd', '-formula', '@SUM(A1)', ' \t=SUM(A1)', '\tplain', '\nplain'])('escapes formula-like text %j', (value) => {
    expect(spreadsheetCell(value)).toBe(`'${value}`)
    expect(csvCell(value)).toBe(`"'${value}"`)
  })
  it('preserves numeric cells, normal model names and CSV quoting', () => {
    expect(spreadsheetCell(-3.5)).toBe(-3.5)
    expect(spreadsheetCell('model-a')).toBe('model-a')
    expect(csvCell('model,"a"')).toBe('"model,""a"""')
  })
})
