#!/usr/bin/env python3
"""Do not use ssh-keyscan at deployment time: host keys are pinned at setup."""
import ipaddress
import os
from pathlib import Path


def main():
    directory = Path(os.environ['RUNNER_TEMP']) / 'asterflow-ssh'
    directory.mkdir(mode=0o700)
    key, hosts = os.environ['SSH_KEY'], os.environ['KNOWN_HOSTS']
    if not key.strip() or not hosts.strip():
        raise ValueError('production deployment secrets have not been configured')
    jump = str(ipaddress.ip_address(os.environ['JUMP_HOST']))
    target = str(ipaddress.ip_address(os.environ['TARGET_HOST']))
    for name, content in [('key', key), ('known_hosts', hosts)]:
        path = directory / name
        path.write_text(content.rstrip() + '\n')
        path.chmod(0o600)
    (directory / 'config').write_text(f'''Host *
  BatchMode yes
  IdentitiesOnly yes
  IdentityFile {directory}/key
  UserKnownHostsFile {directory}/known_hosts
  StrictHostKeyChecking yes
  ConnectTimeout 15
  ServerAliveInterval 30
  ServerAliveCountMax 4
  ForwardAgent no
  RequestTTY no
Host asterflow-prod-app-01
  HostName {jump}
  HostKeyAlias asterflow-prod-app-01
  User ecs-user
Host asterflow-prod-account-central-01
  HostName {target}
  HostKeyAlias asterflow-prod-account-central-01
  User ecs-user
  ProxyJump asterflow-prod-app-01
''')


if __name__ == '__main__':
    main()
