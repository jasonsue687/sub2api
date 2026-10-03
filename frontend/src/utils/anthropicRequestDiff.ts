export type DiffKind = 'same' | 'add' | 'del' | 'chg'

export interface AlignedLine {
  id: string
  indent: number
  left: string | null
  right: string | null
  kind: DiffKind
}

export interface FormatLabels {
  omitted: (len: number, hash?: string) => string
  omittedItems: (count: number, breakdown: string) => string
}

const missing = Symbol('missing')

interface Redacted {
  _redacted: true
  len: number
  sha256?: string
}

export function formatDuration(ms: number): string {
  return (ms / 1000).toFixed(2) + 's'
}

export function shortId(id: string): string {
  if (!id) return '—'
  return id.length > 8 ? id.slice(0, 8) + '…' : id
}

export function splitLocalTime(iso: string): { date: string; time: string } {
  const date = new Date(iso)
  const pad = (value: number) => String(value).padStart(2, '0')
  return {
    date: `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`,
    time: `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
  }
}

export function storedDocument(headers: unknown, body: unknown): string {
  return JSON.stringify({ headers: headers ?? null, body: body ?? null }, null, 2)
}

export function alignDocuments(left: unknown, right: unknown, labels: FormatLabels): AlignedLine[] {
  return alignValue(left ?? missing, right ?? missing, '', 0, labels)
}

export function presentDocument(value: unknown, labels: FormatLabels, side: 'left' | 'right'): AlignedLine[] {
  return collect(value ?? null, '', 0, labels).map(line => ({
    id: line.id,
    indent: line.indent,
    left: side === 'left' ? line.text : null,
    right: side === 'right' ? line.text : null,
    kind: 'same' as const
  }))
}

export function alignHeaders(left: unknown, right: unknown): AlignedLine[] {
  const toMap = (value: unknown) => {
    const map = new Map<string, string>()
    if (!Array.isArray(value)) return map
    for (const item of value) {
      if (item && typeof item === 'object' && 'name' in item) {
        const name = String((item as { name: unknown }).name)
        const raw = (item as { value?: unknown }).value
        map.set(name, raw == null ? '' : String(raw))
      }
    }
    return map
  }
  const leftMap = toMap(left)
  const rightMap = toMap(right)
  const names: string[] = []
  const seen = new Set<string>()
  for (const name of leftMap.keys()) {
    names.push(name)
    seen.add(name.toLowerCase())
  }
  for (const name of rightMap.keys()) {
    if (!seen.has(name.toLowerCase())) names.push(name)
  }
  return names.map(name => {
    const hasLeft = [...leftMap.keys()].some(key => key.toLowerCase() === name.toLowerCase())
    const hasRight = [...rightMap.keys()].some(key => key.toLowerCase() === name.toLowerCase())
    const leftText = hasLeft ? headerLine(name, leftMap.get(matchingKey(leftMap, name)) ?? '') : null
    const rightText = hasRight ? headerLine(name, rightMap.get(matchingKey(rightMap, name)) ?? '') : null
    return { id: 'header.' + name.toLowerCase(), indent: 0, left: leftText, right: rightText, kind: kindOf(leftText, rightText) }
  })
}

function matchingKey(map: Map<string, string>, name: string): string {
  for (const key of map.keys()) {
    if (key.toLowerCase() === name.toLowerCase()) return key
  }
  return name
}

function headerLine(name: string, value: string): string {
  return name + ': ' + value
}

function kindOf(left: string | null, right: string | null): DiffKind {
  if (left == null) return 'add'
  if (right == null) return 'del'
  return left === right ? 'same' : 'chg'
}

function alignValue(left: unknown, right: unknown, path: string, indent: number, labels: FormatLabels): AlignedLine[] {
  if (left === missing) return oneSide(right, path, indent, labels, 'add')
  if (right === missing) return oneSide(left, path, indent, labels, 'del')
  if (isTools(path) && Array.isArray(left) && Array.isArray(right)) {
    const leftText = collapseTools(left, labels)
    const rightText = collapseTools(right, labels)
    return [{ id: path || '$', indent, left: leftText, right: rightText, kind: leftText === rightText ? 'same' : 'chg' }]
  }
  if (!sameShape(left, right)) {
    const leftText = compact(left, labels)
    const rightText = compact(right, labels)
    return [{ id: path || '$', indent, left: leftText, right: rightText, kind: leftText === rightText ? 'same' : 'chg' }]
  }
  if (isRedacted(left) && isRedacted(right)) {
    const leftText = labels.omitted(left.len, left.sha256)
    const rightText = labels.omitted(right.len, right.sha256)
    return [{ id: path || '$', indent, left: leftText, right: rightText, kind: leftText === rightText ? 'same' : 'chg' }]
  }
  if (Array.isArray(left) && Array.isArray(right)) return alignArray(left, right, path, indent, labels)
  if (isPlain(left) && isPlain(right)) return alignObject(left, right, path, indent, labels)
  const leftText = compact(left, labels)
  const rightText = compact(right, labels)
  return [{ id: path || '$', indent, left: leftText, right: rightText, kind: leftText === rightText ? 'same' : 'chg' }]
}

function alignObject(left: Record<string, unknown>, right: Record<string, unknown>, path: string, indent: number, labels: FormatLabels): AlignedLine[] {
  const keys: string[] = []
  const seen = new Set<string>()
  for (const key of Object.keys(left)) {
    keys.push(key)
    seen.add(key)
  }
  for (const key of Object.keys(right)) {
    if (!seen.has(key)) keys.push(key)
  }
  const lines: AlignedLine[] = [{ id: path + ':{', indent, left: '{', right: '{', kind: 'same' }]
  keys.forEach((key, index) => {
    const child = path ? path + '.' + key : key
    const comma = index < keys.length - 1
    const nested = alignValue(
      Object.prototype.hasOwnProperty.call(left, key) ? left[key] : missing,
      Object.prototype.hasOwnProperty.call(right, key) ? right[key] : missing,
      child,
      indent + 1,
      labels
    )
    pushKeyed(lines, key, nested, comma)
  })
  lines.push({ id: path + ':}', indent, left: '}', right: '}', kind: 'same' })
  return lines
}

function alignArray(left: unknown[], right: unknown[], path: string, indent: number, labels: FormatLabels): AlignedLine[] {
  const lines: AlignedLine[] = [{ id: path + ':[', indent, left: '[', right: '[', kind: 'same' }]
  const count = Math.max(left.length, right.length)
  for (let index = 0; index < count; index++) {
    const child = path ? path + '.' + index : String(index)
    const nested = alignValue(index < left.length ? left[index] : missing, index < right.length ? right[index] : missing, child, indent + 1, labels)
    const comma = index < count - 1
    appendComma(lines, nested, comma)
  }
  lines.push({ id: path + ':]', indent, left: ']', right: ']', kind: 'same' })
  return lines
}

function pushKeyed(lines: AlignedLine[], key: string, nested: AlignedLine[], comma: boolean) {
  if (nested.length === 1) {
    const line = nested[0]
    lines.push({
      ...line,
      indent: line.indent,
      left: line.left == null ? null : jsonKey(key) + ': ' + line.left + (comma ? ',' : ''),
      right: line.right == null ? null : jsonKey(key) + ': ' + line.right + (comma ? ',' : '')
    })
    return
  }
  const [first, ...rest] = nested
  lines.push({
    ...first,
    left: first.left == null ? null : jsonKey(key) + ': ' + first.left,
    right: first.right == null ? null : jsonKey(key) + ': ' + first.right
  })
  appendComma(lines, rest, comma)
}

function appendComma(lines: AlignedLine[], nested: AlignedLine[], comma: boolean) {
  nested.forEach((line, index) => {
    const last = index === nested.length - 1
    lines.push({
      ...line,
      left: line.left == null ? null : line.left + (last && comma ? ',' : ''),
      right: line.right == null ? null : line.right + (last && comma ? ',' : '')
    })
  })
}

function oneSide(value: unknown, path: string, indent: number, labels: FormatLabels, kind: 'add' | 'del'): AlignedLine[] {
  return collect(value, path, indent, labels).map(line => ({
    id: line.id,
    indent: line.indent,
    left: kind === 'del' ? line.text : null,
    right: kind === 'add' ? line.text : null,
    kind
  }))
}

function collect(value: unknown, path: string, indent: number, labels: FormatLabels): Array<{ id: string; indent: number; text: string }> {
  if (isRedacted(value)) return [{ id: path || '$', indent, text: labels.omitted(value.len, value.sha256) }]
  if (isTools(path) && Array.isArray(value)) return [{ id: path, indent, text: collapseTools(value, labels) }]
  if (Array.isArray(value)) {
    const lines = [{ id: path + ':[', indent, text: '[' }]
    value.forEach((item, index) => {
      const child = path ? path + '.' + index : String(index)
      const nested = collect(item, child, indent + 1, labels)
      const comma = index < value.length - 1
      if (nested.length === 1) {
        lines.push({ id: nested[0].id, indent: nested[0].indent, text: nested[0].text + (comma ? ',' : '') })
      } else {
        nested.forEach((line, lineIndex) => {
          lines.push({ ...line, text: line.text + (lineIndex === nested.length - 1 && comma ? ',' : '') })
        })
      }
    })
    lines.push({ id: path + ':]', indent, text: ']' })
    return lines
  }
  if (isPlain(value)) {
    const entries = Object.entries(value)
    const lines = [{ id: path + ':{', indent, text: '{' }]
    entries.forEach(([key, child], index) => {
      const childPath = path ? path + '.' + key : key
      const nested = collect(child, childPath, indent + 1, labels)
      const comma = index < entries.length - 1
      if (nested.length === 1) {
        lines.push({ id: childPath, indent: indent + 1, text: jsonKey(key) + ': ' + nested[0].text + (comma ? ',' : '') })
      } else {
        const [first, ...rest] = nested
        lines.push({ id: first.id, indent: indent + 1, text: jsonKey(key) + ': ' + first.text })
        rest.forEach((line, lineIndex) => {
          lines.push({ ...line, text: line.text + (lineIndex === rest.length - 1 && comma ? ',' : '') })
        })
      }
    })
    lines.push({ id: path + ':}', indent, text: '}' })
    return lines
  }
  return [{ id: path || '$', indent, text: compact(value, labels) }]
}

function compact(value: unknown, labels: FormatLabels): string {
  if (isRedacted(value)) return labels.omitted(value.len, value.sha256)
  if (typeof value === 'string') return JSON.stringify(value)
  if (typeof value === 'number' || typeof value === 'boolean' || value == null) return JSON.stringify(value)
  return JSON.stringify(value)
}

function collapseTools(items: unknown[], labels: FormatLabels): string {
  const counts = new Map<string, number>()
  for (const item of items) {
    const type = item && typeof item === 'object' && 'type' in item ? String((item as { type?: unknown }).type || 'unknown') : 'unknown'
    counts.set(type, (counts.get(type) || 0) + 1)
  }
  const breakdown = [...counts.entries()].map(([type, count]) => 'type: ' + type + ' ×' + count).join(', ')
  return labels.omittedItems(items.length, breakdown)
}

function isTools(path: string): boolean {
  return path === 'tools' || path.endsWith('.tools')
}

function isRedacted(value: unknown): value is Redacted {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const record = value as { _redacted?: unknown; len?: unknown; sha256?: unknown }
  return record._redacted === true && typeof record.len === 'number' && (record.sha256 == null || typeof record.sha256 === 'string')
}

function isPlain(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value) && !isRedacted(value)
}

function sameShape(left: unknown, right: unknown): boolean {
  if (isRedacted(left) || isRedacted(right)) return isRedacted(left) && isRedacted(right)
  if (Array.isArray(left) || Array.isArray(right)) return Array.isArray(left) && Array.isArray(right)
  if (isPlain(left) || isPlain(right)) return isPlain(left) && isPlain(right)
  return true
}

function jsonKey(key: string): string {
  return JSON.stringify(key)
}
