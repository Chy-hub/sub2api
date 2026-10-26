// Keep untrusted model/account/request names as text in spreadsheet readers.
// Numeric cells remain numeric; JSON exports keep their original values.
export function spreadsheetCell(value: unknown): unknown {
  const text = typeof value === 'string' ? value.replace(/^[\s\p{Cc}]+/u, '') : ''
  return typeof value === 'string' && (/^[=+\-@]/.test(text) || /^[\t\r\n]/.test(value)) ? `'${value}` : value
}
export function csvCell(value: unknown): string {
  return `"${String(spreadsheetCell(value) ?? '').replace(/"/g, '""')}"`
}
