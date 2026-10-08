import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export interface AccountFingerprint {
  ClientID: string
  UserAgent: string
  StainlessLang: string
  StainlessPackageVersion: string
  StainlessOS: string
  StainlessArch: string
  StainlessRuntime: string
  StainlessRuntimeVersion: string
}

export interface FingerprintAccount {
  id: number
  name: string
  fingerprint_id: number | null
}

export interface FingerprintRecord {
  id: number
  source: 'request' | 'cache'
  fingerprint: AccountFingerprint
  incoming_headers: Record<string, string>
  client_id_origin: 'client' | 'generated' | 'cache'
  source_account_id: number | null
  request_count: number
  first_seen_at: string
  last_seen_at: string
  bound_accounts: FingerprintAccount[]
}

export async function listFingerprints(page = 1, search = '', source = '') {
  return (await apiClient.get<PaginatedResponse<FingerprintRecord>>('/admin/account-fingerprints', {
    params: { page, page_size: 20, search, source }
  })).data
}

export async function listFingerprintAccounts(page = 1, search = '') {
  return (await apiClient.get<PaginatedResponse<FingerprintAccount>>('/admin/account-fingerprints/accounts', {
    params: { page, page_size: 20, search }
  })).data
}

export async function getFingerprintBinding(accountId: number) {
  return (await apiClient.get<{ fingerprint: FingerprintRecord | null; applied: false }>(`/admin/accounts/${accountId}/fingerprint-binding`)).data
}

export async function bindFingerprint(accountId: number, fingerprintId: number | null) {
  return (await apiClient.put(`/admin/accounts/${accountId}/fingerprint-binding`, { fingerprint_id: fingerprintId })).data
}
