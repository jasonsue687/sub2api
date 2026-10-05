const strictSession = {
        title: 'Strict session binding',
        description: 'Permanently bind each client session ID to its original account, even when the API key changes. If the account is unavailable, return an error and let the caller decide how to reroute. Changes take effect without restarting.',
        enabled: 'Enable strict session binding',
        enabledHint: 'Enabled by default. Disabling restores normal scheduling and may switch accounts. Stored bindings remain and resume when re-enabled.',
        retryLimit: 'Same-account retry limit',
        retryLimitHint: '-1 uses the account retry count, 0 disables same-account retries, and a positive number is a fixed cap.',
        sessionHeader: 'Extra session header',
      }

export default strictSession
