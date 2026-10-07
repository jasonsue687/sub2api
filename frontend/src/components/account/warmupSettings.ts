export interface WarmupSettings {
  mode: 'mock' | 'forward'
  protocol: 'openai' | 'anthropic'
  baseURL: string
  apiKey: string
  model: string
  timeoutSeconds: number
  hasAPIKey: boolean
}

export function readWarmupSettings(credentials: Record<string, unknown> = {}, status: Record<string, boolean> = {}): WarmupSettings {
  return {
    mode: credentials.warmup_mode === 'forward' ? 'forward' : 'mock',
    protocol: credentials.warmup_protocol === 'anthropic' ? 'anthropic' : 'openai',
    baseURL: String(credentials.warmup_base_url ?? ''),
    apiKey: '',
    model: String(credentials.warmup_model ?? ''),
    timeoutSeconds: Number(credentials.warmup_timeout_seconds ?? 30),
    hasAPIKey: status.has_warmup_api_key === true
  }
}

export function applyWarmupSettings(credentials: Record<string, unknown>, settings: WarmupSettings): void {
  credentials.warmup_mode = settings.mode
  credentials.warmup_protocol = settings.protocol
  credentials.warmup_base_url = settings.baseURL.trim()
  credentials.warmup_model = settings.model.trim()
  credentials.warmup_timeout_seconds = settings.timeoutSeconds
  // Omission preserves the stored secret; never send a masked placeholder.
  if (settings.apiKey.trim()) credentials.warmup_api_key = settings.apiKey.trim()
}
