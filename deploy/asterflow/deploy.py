#!/usr/bin/env python3
"""Root-owned forced SSH command for the central Sub2API deployment only.

No shell, arbitrary commands, arbitrary paths, or arbitrary registries are accepted.
Production data/credentials and command stderr stay on the server.
"""
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import urllib.request
from manifest import IMAGE, clean, published

ROOT = Path('/opt/sub2api')
BASE = ROOT / 'docker-compose.yml'
OVERRIDE = ROOT / 'docker-compose.override.yml'
HOST = 'asterflow-prod-account-central-01'
APP = 'sub2api'
POSTGRES = 'sub2api-postgres'
REDIS = 'sub2api-redis'
URL = 'http://172.30.219.204:8080'


class DeploymentError(Exception):
    pass


def run(args, *, input=None, stdin=None, stdout=None, timeout=300):
    result = subprocess.run(args, input=input, stdin=stdin,
                            stdout=stdout if stdout is not None else subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout)
    if result.returncode:
        # docker/psql errors may contain configuration or payloads: do not echo them.
        raise DeploymentError('Command failed: ' + args[0] + ' ' + args[1])
    return result.stdout or b''


def inspect(name):
    return json.loads(run(['docker', 'inspect', name]))[0]


def compose(*args):
    command = ['docker', 'compose', '--project-directory', str(ROOT), '-f', str(BASE)]
    if OVERRIDE.exists():
        command += ['-f', str(OVERRIDE)]
    return run(command + list(args), timeout=600)


def sql(query):
    info = inspect(POSTGRES)
    env = dict(item.split('=', 1) for item in info['Config']['Env'])
    return run(['docker', 'exec', POSTGRES, 'psql', '-X', '-v', 'ON_ERROR_STOP=1',
                '-U', env.get('POSTGRES_USER', 'postgres'),
                '-d', env.get('POSTGRES_DB', 'postgres'), '-At', '-c', query]).decode().strip()


def migrations():
    rows = json.loads(sql("SELECT COALESCE(json_object_agg(filename,checksum),'{}'::json) FROM schema_migrations"))
    if not rows:
        raise DeploymentError('Production migration history is empty')
    return rows


def pending_migrations(current, candidate):
    for name, checksum in current.items():
        if candidate.get(name) != checksum:
            raise DeploymentError('Missing or changed applied migration: ' + name)
    return sorted(set(candidate) - set(current))


def replacement(release):
    return {'x-asterflow-deployment': {'schema': 1, 'sha': release['sha']},
            'services': {APP: {'image': IMAGE + '@' + release['digest'],
                               'environment': {'SUB2API_ANTHROPIC_AUDIT_ENABLED': 'true',
                                               'SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS': '*',
                                               'OPS_ENABLED': 'true',
                                               'OPS_CLEANUP_ENABLED': 'true'}}}}


def check_config_change(before, after):
    expected = json.loads(json.dumps(before))
    expected['services'][APP]['image'] = after['services'][APP]['image']
    env = expected['services'][APP].setdefault('environment', {})
    for key in ('SUB2API_ANTHROPIC_AUDIT_ENABLED', 'SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS', 'OPS_ENABLED', 'OPS_CLEANUP_ENABLED'):
        env[key] = after['services'][APP]['environment'][key]
    # Compose preserves top-level extension fields but these do not change services.
    expected.pop('x-asterflow-deployment', None)
    after = dict(after)
    after.pop('x-asterflow-deployment', None)
    if expected != after:
        raise DeploymentError('Unexpected Compose change outside the application image/audit settings')


def atomic_write(path, data):
    temporary = path.with_name(path.name + '.next')
    with open(temporary, 'wb') as stream:
        os.chmod(temporary, 0o600)
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def release_state():
    path = ROOT / 'automation/state.json'
    if not path.exists():
        return {'current': None, 'previous': None}
    state = json.loads(path.read_text())
    for key in ('current', 'previous'):
        if state.get(key):
            published(state[key])
    return state


def rollback_release(info, state):
    current = state.get('current')
    previous = state.get('previous')
    # A failed deployment may have replaced the container without advancing state.
    target = current if current and info['Config']['Image'] != IMAGE + '@' + current['digest'] else previous
    return target.get('release_tag', '') if target else ''


def record_success(release, directory=None, rollback=False):
    state = release_state()
    history = state.get('history') or [r for r in (state.get('previous'), state.get('current')) if r]
    target = clean(release)
    if rollback:
        # Discard reverted releases from the default rollback chain; never bounce
        # back to the version the operator just rolled back away from.
        index = next((i for i in range(len(history)-1, -1, -1)
                      if history[i]['digest'] == release['digest']), None)
        history = history[:index+1] if index is not None else []
    if history and history[-1]['digest'] == release['digest']:
        history[-1] = target
    else:
        history.append(target)
    state['history'] = history[-20:]
    state['current'] = state['history'][-1]
    state['previous'] = state['history'][-2] if len(state['history']) > 1 else None
    state['updated_at'] = datetime.datetime.now(datetime.timezone.utc).isoformat()
    if directory:
        state['backup'] = str(directory)
    atomic_write(ROOT / 'automation/state.json', (json.dumps(state, indent=2) + '\n').encode())


def already_deployed(info, config, release):
    service = config.get('services', {}).get(APP, {})
    desired = replacement(release)['services'][APP]
    return (info['State'].get('Health', {}).get('Status') == 'healthy'
            and info['Config']['Image'] == desired['image']
            and service.get('image') == desired['image']
            and all(service.get('environment', {}).get(k) == v for k, v in desired['environment'].items()))


def preflight_result(release, rollback=False):
    info, config, _, pending = preflight(release, rollback=rollback)
    state = release_state()
    return {'status': 'preflight-passed', 'operation': 'rollback' if rollback else 'deploy',
            'current_image': info['Config']['Image'],
            'last_successful_release': (state.get('current') or {}).get('release_tag'),
            'target_release': release['release_tag'], 'candidate_sha': release['sha'],
            'target_image': IMAGE + '@' + release['digest'], 'pending_migrations': pending,
            'previous_release': rollback_release(info, state),
            'deploy_requires_allow_migrations': bool(pending),
            'would_skip': not pending and already_deployed(info, config, release),
            'image_pulled': False, 'production_changed': False}


def restore_override(previous):
    if previous is None:
        OVERRIDE.unlink(missing_ok=True)
    else:
        atomic_write(OVERRIDE, previous)


def wait_healthy(version=None):
    for _ in range(90):
        info = inspect(APP)
        if info['State'].get('Health', {}).get('Status') == 'healthy':
            try:
                with urllib.request.urlopen(URL + '/health', timeout=5) as response:
                    if response.status != 200:
                        raise ValueError('health status')
                if version:
                    with urllib.request.urlopen(URL + '/api/v1/settings/public', timeout=5) as response:
                        actual = json.load(response)['data']['version']
                    if actual != version:
                        raise ValueError('version mismatch')
                return
            except (OSError, ValueError, KeyError):
                pass
        time.sleep(2)
    raise DeploymentError('Health/version verification failed')


def backup(directory):
    shutil.copy2(BASE, directory / BASE.name)
    if OVERRIDE.exists():
        shutil.copy2(OVERRIDE, directory / OVERRIDE.name)
    if (ROOT / '.env').exists():
        shutil.copy2(ROOT / '.env', directory / '.env')
    run(['tar', '--exclude=data/logs', '-czf', str(directory / 'app-data.tar.gz'), '-C', str(ROOT), 'data'])
    env = dict(item.split('=', 1) for item in inspect(POSTGRES)['Config']['Env'])
    with open(directory / 'database.dump', 'wb') as stream:
        run(['docker', 'exec', POSTGRES, 'pg_dump', '-Fc',
             '-U', env.get('POSTGRES_USER', 'postgres'),
             '-d', env.get('POSTGRES_DB', 'postgres')], stdout=stream, timeout=600)
    with open(directory / 'database.dump', 'rb') as stream:
        run(['docker', 'exec', '-i', POSTGRES, 'pg_restore', '--list'], stdin=stream, timeout=60)
    checksums = {}
    for path in directory.iterdir():
        if path.is_file():
            with path.open('rb') as stream:
                checksums[path.name] = hashlib.file_digest(stream, 'sha256').hexdigest()
    (directory / 'checksums.json').write_text(json.dumps(checksums, indent=2))


def preflight(release, rollback=False):
    if os.uname().nodename != HOST or os.uname().machine != 'x86_64':
        raise DeploymentError('Wrong production host or architecture')
    if OVERRIDE.exists():
        try:
            if json.loads(OVERRIDE.read_text()).get('x-asterflow-deployment', {}).get('schema') != 1:
                raise ValueError('unknown override')
        except ValueError as exc:
            raise DeploymentError('Existing Compose override is not managed by this pipeline') from exc
    info = inspect(APP)
    if not rollback and info['State'].get('Health', {}).get('Status') != 'healthy':
        raise DeploymentError('Current application is not healthy; use a compatible rollback release')
    configured_files = info['Config'].get('Labels', {}).get('com.docker.compose.project.config_files', '')
    expected_files = [str(BASE)] + ([str(OVERRIDE)] if OVERRIDE.exists() else [])
    if configured_files.split(',') != expected_files:
        raise DeploymentError('Running Compose files differ from the managed deployment')
    config = json.loads(compose('config', '--format', 'json'))
    if config['services'][APP]['image'] != info['Config']['Image']:
        raise DeploymentError('Running image differs from Compose; resolve deployment drift')
    current = migrations()
    pending = pending_migrations(current, release['migrations'])
    if rollback and pending:
        raise DeploymentError('Rollback cannot introduce migrations; choose a database-compatible release')
    settings = json.loads(sql("SELECT COALESCE(json_object_agg(key,value),'{}'::json) FROM settings WHERE key IN ('ops_advanced_settings','ops_monitoring_enabled')"))
    if settings.get('ops_monitoring_enabled') == 'false':
        raise DeploymentError('Enable Ops monitoring in the administrator settings before deploying')
    advanced = json.loads(settings.get('ops_advanced_settings', '{}'))
    if advanced.get('data_retention', {}).get('cleanup_enabled') is False:
        raise DeploymentError('Enable Ops scheduled cleanup before deploying the 30-day retention policy')
    database_size = int(sql('SELECT pg_database_size(current_database())'))
    if shutil.disk_usage(ROOT).free < max(2 * 1024**3, database_size * 3):
        raise DeploymentError('Insufficient free space for database backup and image pull')
    return info, config, current, pending


def deploy(release, registry_user='', registry_token='', rollback=False):
    info, old_config, before, pending = preflight(release, rollback=rollback)
    was_healthy = info['State'].get('Health', {}).get('Status') == 'healthy'
    if not pending and already_deployed(info, old_config, release):
        wait_healthy(release['version'])
        record_success(release, rollback=rollback)
        return {'status': 'unchanged', 'release_tag': release['release_tag'],
                'image': IMAGE + '@' + release['digest'], 'production_changed': False}
    if pending and release.get('allow_migrations') is not True:
        raise DeploymentError('New migrations require allow_migrations: ' + ', '.join(pending))
    image = IMAGE + '@' + release['digest']
    with tempfile.TemporaryDirectory(prefix='asterflow-registry-') as auth:
        if registry_token:
            run(['docker', '--config', auth, 'login', 'ghcr.io', '-u', registry_user, '--password-stdin'], input=registry_token.encode())
        run(['docker', '--config', auth, 'pull', image], timeout=900)
    candidate = inspect(image)
    labels = candidate['Config'].get('Labels', {})
    if (candidate['Architecture'] != 'amd64' or candidate['Os'] != 'linux'
            or labels.get('org.opencontainers.image.revision') != release['sha']
            or labels.get('org.opencontainers.image.version') != release['version']
            or labels.get('org.opencontainers.image.source') != 'https://github.com/jasonsue687/sub2api'):
        raise DeploymentError('Pulled image does not match the release manifest')
    stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
    directory = ROOT / 'backups' / ('asterflow-' + stamp + '-' + release['sha'][:12])
    directory.mkdir(mode=0o700, parents=True)
    (directory / 'release.json').write_text(json.dumps(clean(release), indent=2))
    (directory / 'before-migrations.json').write_text(json.dumps(before, sort_keys=True))
    (directory / 'old-image.txt').write_text(info['Config']['Image'] + '\n' + info['Image'] + '\n')
    deps = {name: (inspect(name)['Id'], inspect(name)['State']['StartedAt']) for name in (POSTGRES, REDIS)}
    previous = OVERRIDE.read_bytes() if OVERRIDE.exists() else None
    stopped, changed, launched = False, False, False
    try:
        stopped = True
        compose('stop', '-t', '60', APP)
        # Final backup is taken with the sole application writer stopped.
        backup(directory)
        if migrations() != before:
            raise DeploymentError('Migration history changed during backup; retry preflight')
        changed = True
        atomic_write(OVERRIDE, (json.dumps(replacement(release), indent=2) + '\n').encode())
        check_config_change(old_config, json.loads(compose('config', '--format', 'json')))
        launched = True
        compose('up', '-d', '--no-deps', '--pull', 'never', APP)
        wait_healthy(release['version'])
        if inspect(APP)['Image'] != candidate['Id']:
            raise DeploymentError('Running image differs from the pulled immutable image')
        if migrations() != release['migrations']:
            raise DeploymentError('Migration verification failed')
        for name, identity in deps.items():
            actual = inspect(name)
            if (actual['Id'], actual['State']['StartedAt']) != identity:
                raise DeploymentError('Database/cache container unexpectedly changed')
        result = {'status': 'rolled-back' if rollback else 'deployed',
                  'release_tag': release['release_tag'], 'sha': release['sha'], 'image': image,
                  'backup': str(directory), 'new_migrations': pending,
                  'verification': 'container health, HTTP health, version, migration hashes, unchanged PostgreSQL/Redis',
                  'model_request_test': 'not performed'}
        (directory / 'result.json').write_text(json.dumps(result, indent=2))
        record_success(release, directory, rollback=rollback)
        return result
    except BaseException:
        # Never automatically restore a DB or boot an old binary against a possibly
        # partially migrated schema. Keep backups/new data for explicit recovery.
        safe = not launched or not pending
        if safe:
            try:
                safe = migrations() == before
            except Exception:
                safe = False
        recovery = 'manual-recovery-required'
        if rollback and not was_healthy:
            safe = False  # Do not automatically restart the known unhealthy release.
        if safe:
            if changed:
                restore_override(previous)
            if stopped:
                if launched:
                    compose('up', '-d', '--no-deps', '--pull', 'never', APP)
                else:
                    compose('start', APP)
                wait_healthy()
            recovery = 'previous-application-restored'
        elif launched:
            compose('stop', '-t', '60', APP)
        (directory / 'result.json').write_text(json.dumps({'status': 'failed', 'recovery': recovery}))
        raise DeploymentError('Deployment failed; ' + recovery + '; backup=' + str(directory)) from None


def main():
    os.umask(0o077)
    operation = sys.argv[1] if len(sys.argv) == 2 else ''
    if operation not in ('status', 'preflight', 'deploy', 'preflight-rollback', 'rollback'):
        raise DeploymentError('Allowed commands: status, preflight, deploy, preflight-rollback, rollback')
    if os.geteuid() != 0 or os.uname().nodename != HOST:
        raise DeploymentError('Run only through the installed production entrypoint')
    if operation == 'status':
        info = inspect(APP)
        state = release_state()
        print(json.dumps({'host': HOST, 'image': info['Config']['Image'],
                          'health': info['State'].get('Health', {}).get('Status'),
                          'last_successful_release': (state.get('current') or {}).get('release_tag'),
                          'rollback_release': rollback_release(info, state)}))
        return
    payload = sys.stdin.buffer.read(256 * 1024 + 1)
    if len(payload) > 256 * 1024:
        raise DeploymentError('Release manifest is too large')
    release = json.loads(payload)
    token = release.pop('registry_token', '')
    user = release.pop('registry_user', '')
    published(release)
    with open(ROOT / 'automation/deployment.lock', 'w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        if operation in ('preflight', 'preflight-rollback'):
            result = preflight_result(release, rollback=operation == 'preflight-rollback')
        else:
            result = deploy(release, user, token, rollback=operation == 'rollback')
        print(json.dumps(result))


if __name__ == '__main__':
    def interrupted(signum, frame):
        raise DeploymentError('Deployment interrupted')
    for sig in (signal.SIGTERM, signal.SIGHUP):
        signal.signal(sig, interrupted)
    try:
        main()
    except Exception as error:
        message = str(error) if isinstance(error, (DeploymentError, ValueError)) else type(error).__name__
        print(json.dumps({'status': 'failed', 'error': message}))
        sys.exit(1)
