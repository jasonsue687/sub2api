export default {
  fingerprints: {
    title: 'Account fingerprints',
    description: 'Register identities from Anthropic requests, count repeated fingerprints and retain identities imported from account caches.',
    stagedNotice: 'Registration and bindings only. Bindings are not applied to outgoing requests; existing fingerprint selection remains unchanged.',
    importCache: 'Import cached identities',
    importResult: '{imported} accounts imported or confirmed; {missing} without a cache; {failed} failed reads. Repeated imports do not create duplicates.',
    search: 'Search fingerprint ID, UA or device ID', searchAccount: 'Search subscription account name or ID',
    allSources: 'All sources', source: 'Source', request: 'Incoming request', cache: 'Account cache',
    identity: 'Fingerprint', device: 'Device ID', details: 'Show full fingerprint',
    incoming: 'Identity fields supplied by the request (registered profiles reuse existing validation and defaults)',
    origin_client: 'Device ID supplied by client', origin_generated: 'No client device ID; registration ID generated using existing logic', origin_cache: 'Existing cached device ID preserved',
    observations: 'Observations', count: '{count} incoming requests', firstSeen: 'First registered', lastSeen: 'Last registered',
    boundAccounts: 'Bound subscription accounts', unbound: 'Unbound', bindAccount: 'Bind subscription account',
    bindingAction: 'Identity binding', bindingTitle: 'Subscription account identity binding', account: 'Subscription account',
    selectAccount: 'Select a subscription account', currentBinding: 'Current binding', selectedFingerprint: 'Fingerprint to bind',
    empty: 'No fingerprints yet. Import existing cached identities or wait for new requests to be registered.',
    loadFailed: 'Loading failed. Please retry.', saveFailed: 'Could not save binding. Refresh and retry.',
    importFailed: 'Cache import did not complete. Imported records are retained; retry the import.'
  }
}
