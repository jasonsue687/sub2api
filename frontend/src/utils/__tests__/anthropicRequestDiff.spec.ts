import { describe, expect, it } from 'vitest'
import { alignDocuments, alignHeaders, formatDuration, type FormatLabels } from '../anthropicRequestDiff'

const labels: FormatLabels = {
  omitted: (len, hash) => hash ? `[已省略 ${len} 字符 sha256:${hash.slice(0, 4)}…]` : `[已省略 ${len} 字符]`,
  omittedItems: (count, breakdown) => `[已省略 ${count} 项 · ${breakdown}]`
}
const line = (rows: ReturnType<typeof alignDocuments>, id: string) => rows.find(row => row.id === id)

describe('anthropic request diff', () => {
  it('formats duration in seconds', () => {
    expect(formatDuration(2410)).toBe('2.41s')
    expect(formatDuration(20)).toBe('0.02s')
  })

  it('shows redacted prompts and tool names without recording the name', () => {
    const rows = alignDocuments(
      { messages: [{ content: { _redacted: true, len: 12, sha256: 'abcd1234ffff' } }], tool_choice: { name: { _redacted: true, len: 4 } } },
      { messages: [{ content: { _redacted: true, len: 12, sha256: 'abcd1234ffff' } }], tool_choice: { name: { _redacted: true, len: 9 } } },
      labels
    )
    expect(line(rows, 'messages.0.content')?.left).toBe('"content": [已省略 12 字符 sha256:abcd…]')
    expect(line(rows, 'messages.0.content')?.kind).toBe('same')
    expect(line(rows, 'tool_choice.name')?.kind).toBe('chg')
    expect(line(rows, 'tool_choice.name')?.right).toBe('"name": [已省略 9 字符]')
    expect(JSON.stringify(rows)).not.toContain('sha256:abcd1234')
  })

  it('collapses tool arrays and classifies add, delete, and change', () => {
    const rows = alignDocuments(
      { model: 'claude-sonnet-4-5', max_tokens: 100, tools: [{ type: 'custom' }, { type: 'custom' }], drop: true },
      { model: 'claude-sonnet-4-5-20250929', tools: [{ type: 'custom' }, { type: 'custom' }, { type: 'custom' }], added: 1 },
      labels
    )
    expect(line(rows, 'model')).toMatchObject({ kind: 'chg' })
    expect(line(rows, 'max_tokens')).toMatchObject({ kind: 'del', left: '"max_tokens": 100,', right: null })
    expect(line(rows, 'added')).toMatchObject({ kind: 'add', left: null, right: '"added": 1' })
    expect(line(rows, 'tools')).toMatchObject({
      kind: 'chg',
      left: '"tools": [已省略 2 项 · type: custom ×2],',
      right: '"tools": [已省略 3 项 · type: custom ×3],'
    })
    expect(rows.filter(row => row.kind === 'same').map(row => row.left)).toEqual(expect.arrayContaining(['{', '}']))
  })

  it('keeps the billing block verbatim and aligns headers by name', () => {
    const rows = alignDocuments(
      { system: [{ type: 'text', text: 'x-anthropic-billing-header: cc_version=1' }, { type: 'text', text: { _redacted: true, len: 3, sha256: 'eeee' } }] },
      { system: [{ type: 'text', text: 'x-anthropic-billing-header: cc_version=1' }, { type: 'text', text: { _redacted: true, len: 3, sha256: 'eeee' } }] },
      labels
    )
    expect(line(rows, 'system.0.text')?.left).toContain('x-anthropic-billing-header:')
    expect(line(rows, 'system.0.text')?.kind).toBe('same')
    const headers = alignHeaders(
      [{ name: 'x-api-key', value: '***' }, { name: 'anthropic-version', value: '2023-06-01' }],
      [{ name: 'anthropic-version', value: '2023-06-01' }, { name: 'x-api-key', value: 'Bearer ***' }]
    )
    expect(headers.find(row => row.id === 'header.x-api-key')).toMatchObject({ kind: 'chg' })
    expect(headers.find(row => row.id === 'header.anthropic-version')?.kind).toBe('same')
  })
})
