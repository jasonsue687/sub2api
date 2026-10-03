import type { AnthropicAudit } from '@/api/admin/anthropicRequests'

// Compare presence as well as value: a missing stream flag is not false.
export function comparisonFields(left: AnthropicAudit, right: AnthropicAudit) {
  const flatten = (audit: AnthropicAudit): Record<string, string | number | boolean | undefined> => ({
    endpoint: audit.endpoint, source: audit.origin.source, mimic: audit.origin.mimic, mock: audit.mock,
    body_state: audit.body_state, cc_entrypoint: audit.cc_entrypoint, cc_version: audit.cc_version,
    device_hash: audit.device_hash, account_hash: audit.account_hash, session_hash: audit.session_hash,
    ...Object.fromEntries(Object.entries(audit.headers).map(([key, value]) => [`headers.${key}`, value])),
    ...Object.fromEntries(Object.entries(audit.parameters).map(([key, value]) => [`parameters.${key}`, value]))
  })
  const a = flatten(left), b = flatten(right)
  return [...new Set([...Object.keys(a), ...Object.keys(b)])].sort().map(key => ({ key, values: [a[key], b[key]], different: a[key] !== b[key] }))
}
