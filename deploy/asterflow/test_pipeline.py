import copy
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import deploy
import manifest
import resolve


def release():
    return {'schema': 1, 'repository': manifest.REPOSITORY, 'sha': 'a' * 40,
            'digest': 'sha256:' + 'b' * 64, 'version': '0.2.13-asterflow.1',
            'release_tag': 'asterflow-v0.2.13-r1', 'run_id': 123, 'run_attempt': 1, 'migrations': {'001_init.sql': 'c' * 64}}


class PolicyTests(unittest.TestCase):
    def test_manifest_rejects_injection_and_foreign_images(self):
        for key, value in [('sha', 'main;sh'), ('digest', 'x;id'), ('repository', 'other/sub2api'),
                           ('version', 'latest'), ('run_id', 0), ('run_attempt', True),
                           ('migrations', {'../../x.sql': 'c' * 64})]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                data = release(); data[key] = value; manifest.validate(data)

    def test_refuses_modified_or_removed_applied_migrations(self):
        for candidate in [{}, {'001_init.sql': 'd' * 64}]:
            with self.assertRaises(deploy.DeploymentError):
                deploy.pending_migrations(release()['migrations'], candidate)
        self.assertEqual(deploy.pending_migrations(release()['migrations'],
                         {**release()['migrations'], '002_new.sql': 'd' * 64}), ['002_new.sql'])

    def test_run_must_be_successful_trusted_branch_build(self):
        good = {'status': 'completed', 'conclusion': 'success', 'head_branch': 'asterflow',
                'path': '.github/workflows/asterflow-build.yml', 'event': 'push',
                'head_repository': {'full_name': manifest.REPOSITORY}}
        resolve.check_run(good)
        for key, value in [('conclusion', 'failure'), ('status', 'in_progress'),
                           ('event', 'pull_request'), ('head_branch', 'main'),
                           ('path', '.github/workflows/release.yml'),
                           ('head_repository', {'full_name': 'other/sub2api'})]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                resolve.check_run({**good, key: value})

    def test_compose_change_preserves_data_ports_and_other_services(self):
        before = {'services': {'sub2api': {'image': 'old', 'ports': ['private:8080:8080'],
                  'volumes': ['data:/app/data'], 'environment': {'DATABASE_PASSWORD': 'do-not-print'}},
                  'postgres': {'image': 'postgres:18', 'volumes': ['pg:/var/lib/postgresql']}},
                  'volumes': {'data': {}, 'pg': {}}}
        after = copy.deepcopy(before)
        after['services']['sub2api'].update(deploy.replacement(release())['services']['sub2api'])
        after['services']['sub2api']['environment']['DATABASE_PASSWORD'] = 'do-not-print'
        deploy.check_config_change(before, after)
        for service, key, value in [('sub2api', 'ports', ['0.0.0.0:8080:8080']),
                                   ('sub2api', 'volumes', []), ('postgres', 'image', 'postgres:19')]:
            changed = copy.deepcopy(after); changed['services'][service][key] = value
            with self.assertRaises(deploy.DeploymentError):
                deploy.check_config_change(before, changed)

    def test_registry_credentials_do_not_enter_release_manifest(self):
        # Stored manifests are explicitly created from build metadata, not runtime secrets.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); (root / 'backend/migrations').mkdir(parents=True)
            (root / 'backend/migrations/001_init.sql').write_text(' SELECT 1;\n')
            with patch.dict(os.environ, {'GITHUB_SHA': 'a'*40, 'IMAGE_DIGEST': 'sha256:'+'b'*64,
                        'RELEASE_VERSION': '0.2.13-asterflow.1', 'GITHUB_RUN_ID': '123',
                        'GITHUB_RUN_ATTEMPT': '1', 'REGISTRY_TOKEN': 'private-token'}):
                result = manifest.create(root)
            self.assertNotIn('private-token', json.dumps(result))
            import hashlib
            self.assertEqual(result['migrations']['001_init.sql'], hashlib.sha256(b'SELECT 1;').hexdigest())


class DeploymentRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / 'automation').mkdir()
        self.data = release()
        self.current = self.data['migrations'].copy()
        self.calls = []
        self.old = {'Config': {'Image': 'old:version'}, 'Image': 'sha256:old',
                    'State': {'Health': {'Status': 'healthy'}}}
        self.patches = [patch.object(deploy, 'ROOT', self.root),
                        patch.object(deploy, 'BASE', self.root / 'docker-compose.yml'),
                        patch.object(deploy, 'OVERRIDE', self.root / 'docker-compose.override.yml'),
                        patch.object(deploy, 'preflight', side_effect=lambda r, **kwargs: (self.old, {}, self.current, deploy.pending_migrations(self.current, r['migrations']))),
                        patch.object(deploy, 'run', return_value=b''),
                        patch.object(deploy, 'backup'),
                        patch.object(deploy, 'check_config_change'),
                        patch.object(deploy, 'inspect', side_effect=self.inspect),
                        patch.object(deploy, 'compose', side_effect=self.compose),
                        patch.object(deploy, 'wait_healthy'),
                        patch.object(deploy, 'migrations', side_effect=lambda: self.data['migrations'] if any(c[0] == 'up' for c in self.calls) else self.current)]
        for item in self.patches:
            item.start(); self.addCleanup(item.stop)

    def inspect(self, name):
        if name.startswith(manifest.IMAGE):
            return {'Id': 'new-image-id', 'Architecture': 'amd64', 'Os': 'linux', 'Config': {'Labels': {
                'org.opencontainers.image.revision': self.data['sha'],
                'org.opencontainers.image.version': self.data['version'],
                'org.opencontainers.image.source': 'https://github.com/jasonsue687/sub2api'}}}
        return {'Id': name, 'Image': 'new-image-id', 'State': {'StartedAt': 'unchanged'}}

    def compose(self, *args):
        self.calls.append(args)
        return b'{}'

    def test_new_migration_requires_explicit_deployment_input(self):
        self.data['migrations']['002_next.sql'] = 'd' * 64
        with self.assertRaisesRegex(deploy.DeploymentError, 'allow_migrations'):
            deploy.deploy(self.data)
        self.assertEqual(self.calls, [])
        deploy.backup.assert_not_called()

    def test_backup_failure_restarts_original_container(self):
        deploy.backup.side_effect = deploy.DeploymentError('backup failed')
        with self.assertRaisesRegex(deploy.DeploymentError, 'previous-application-restored'):
            deploy.deploy(self.data)
        self.assertIn(('start', 'sub2api'), self.calls)
        self.assertFalse(any(call[0] == 'up' for call in self.calls))
        self.assertFalse(deploy.OVERRIDE.exists())

    def test_unhealthy_release_without_migration_restores_old_override(self):
        previous = b'{"old": "override"}'; deploy.OVERRIDE.write_bytes(previous)
        deploy.wait_healthy.side_effect = [deploy.DeploymentError('unhealthy'), None]
        with self.assertRaisesRegex(deploy.DeploymentError, 'previous-application-restored'):
            deploy.deploy(self.data)
        self.assertEqual(deploy.OVERRIDE.read_bytes(), previous)
        self.assertEqual(sum(call[0] == 'up' for call in self.calls), 2)

    def test_migration_failure_never_starts_old_binary_or_restores_database(self):
        self.data['migrations']['002_next.sql'] = 'd' * 64
        self.data['allow_migrations'] = True
        deploy.wait_healthy.side_effect = deploy.DeploymentError('failed')
        with self.assertRaisesRegex(deploy.DeploymentError, 'manual-recovery-required'):
            deploy.deploy(self.data)
        self.assertEqual(sum(call[0] == 'up' for call in self.calls), 1)
        self.assertEqual(self.calls[-1], ('stop', '-t', '60', 'sub2api'))
        self.assertTrue(deploy.OVERRIDE.exists())

    def test_only_application_is_recreated(self):
        result = deploy.deploy(self.data)
        self.assertEqual(result['status'], 'deployed')
        self.assertIn(('up', '-d', '--no-deps', '--pull', 'never', 'sub2api'), self.calls)
        self.assertFalse(any('down' in call or 'postgres' in call or 'redis' in call for call in self.calls))
        self.assertEqual(json.loads(deploy.OVERRIDE.read_text())['services']['sub2api']['environment']['SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS'], '*')

    def test_redeploy_same_image_and_settings_skips_pull_backup_and_restart(self):
        config = deploy.replacement(self.data)
        self.old['Config']['Image'] = manifest.IMAGE + '@' + self.data['digest']
        deploy.preflight.side_effect = None
        deploy.preflight.return_value = (self.old, config, self.current, [])
        result = deploy.deploy(self.data)
        self.assertEqual(result['status'], 'unchanged')
        deploy.run.assert_not_called()
        deploy.backup.assert_not_called()
        self.assertEqual(self.calls, [])

    def test_unhealthy_rollback_failure_does_not_restart_bad_release(self):
        self.old['State']['Health']['Status'] = 'unhealthy'
        deploy.wait_healthy.side_effect = deploy.DeploymentError('rollback target unhealthy')
        with self.assertRaisesRegex(deploy.DeploymentError, 'manual-recovery-required'):
            deploy.deploy(self.data, rollback=True)
        self.assertEqual(sum(c[0] == 'up' for c in self.calls), 1)
        self.assertEqual(self.calls[-1], ('stop', '-t', '60', 'sub2api'))

    def test_successful_rollback_updates_history_without_database_restore(self):
        self.old['State']['Health']['Status'] = 'unhealthy'
        current = {**self.data, 'digest': 'sha256:' + 'd'*64, 'release_tag': 'asterflow-v0.2.13-r2'}
        deploy.record_success(self.data)
        deploy.record_success(current)
        result = deploy.deploy(self.data, rollback=True)
        self.assertEqual(result['status'], 'rolled-back')
        state = deploy.release_state()
        self.assertEqual(state['current']['release_tag'], self.data['release_tag'])
        self.assertIsNone(state['previous'])
        self.assertEqual([r['release_tag'] for r in state['history']], [self.data['release_tag']])
        self.assertTrue(all('pg_restore' not in str(c) for c in deploy.run.call_args_list))


if __name__ == '__main__':
    unittest.main()
