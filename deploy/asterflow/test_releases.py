import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import deploy
import manifest
import promote
import resolve
from test_pipeline import release


class ReleaseTests(unittest.TestCase):
    def test_tag_namespace_and_upstream_version_match(self):
        for tag in ('v0.2.13', 'asterflow-v0.2.13-r0', '../r1', 'asterflow-v0.2.14-r1'):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                manifest.published({**release(), 'release_tag': tag})
        manifest.published(release())

    def test_rejects_mutable_draft_prerelease_moved_tag_and_altered_asset(self):
        data = release(); payload = json.dumps(data).encode()
        meta = {'draft': False, 'prerelease': False, 'immutable': True,
                'tag_name': data['release_tag'],
                'assets': [{'name': 'release.json', 'digest': 'sha256:' + hashlib.sha256(payload).hexdigest()}]}
        resolve.check_release(meta, data, data['release_tag'], data['sha'], payload)
        for key, value in [('draft', True), ('prerelease', True), ('immutable', False), ('tag_name', 'other')]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                resolve.check_release({**meta, key: value}, data, data['release_tag'], data['sha'], payload)
        with self.assertRaises(ValueError):
            resolve.check_release(meta, data, data['release_tag'], 'd'*40, payload)
        with self.assertRaises(ValueError):
            resolve.check_release(meta, data, data['release_tag'], data['sha'], payload + b' ')

    def test_deploy_resolves_release_without_actions_artifact_or_build_history(self):
        data = release(); payload = json.dumps(data).encode()
        meta = {'draft': False, 'prerelease': False, 'immutable': True,
                'tag_name': data['release_tag'],
                'assets': [{'name': 'release.json', 'digest': 'sha256:' + hashlib.sha256(payload).hexdigest()}]}
        calls = []
        def execute(args, **kwargs):
            calls.append(args)
            if args[:3] == ['gh', 'release', 'download']:
                directory = Path(args[args.index('--dir')+1]); directory.mkdir(exist_ok=True)
                (directory/'release.json').write_bytes(payload)
            return SimpleNamespace(returncode=0)
        with tempfile.TemporaryDirectory() as tmp, patch.object(resolve.subprocess, 'run', side_effect=execute), patch.object(resolve.subprocess, 'check_output', side_effect=[json.dumps(meta).encode(), data['sha']]):
            self.assertEqual(resolve.download_release(data['release_tag'], Path(tmp)), data)
        self.assertTrue(any(c[:3] == ['gh', 'release', 'verify-asset'] for c in calls))
        self.assertFalse(any('actions' in str(c) or c[:2] == ['gh', 'run'] for c in calls))

    def test_cannot_promote_build_under_a_tag_for_another_commit(self):
        with patch.object(promote, 'tag_commit', return_value='d'*40), patch.object(promote, 'download_build', return_value=release()), patch.object(promote.subprocess, 'run') as run:
            with self.assertRaisesRegex(ValueError, 'different commit'):
                promote.promote(release()['release_tag'], '123')
            run.assert_not_called()

    def test_history_advances_only_successes_and_keeps_credentials_out(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(deploy, 'ROOT', Path(tmp)):
            (Path(tmp)/'automation').mkdir()
            one = release(); two = {**one, 'digest': 'sha256:'+'d'*64, 'release_tag': 'asterflow-v0.2.13-r2'}
            deploy.record_success(one)
            deploy.record_success({**two, 'registry_token': 'private', 'allow_migrations': True})
            deploy.record_success(two)
            state = deploy.release_state()
            self.assertEqual(state['previous'], one)
            self.assertEqual(state['current'], two)
            self.assertNotIn('private', json.dumps(state))
            info = {'Config': {'Image': manifest.IMAGE + '@' + two['digest']}}
            self.assertEqual(deploy.rollback_release(info, state), one['release_tag'])
            # A later failed attempt has not replaced the last successful record.
            info['Config']['Image'] = 'failed:attempt'
            self.assertEqual(deploy.rollback_release(info, state), two['release_tag'])

    def test_rollback_chain_excludes_the_reverted_bad_version(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(deploy, 'ROOT', Path(tmp)):
            (Path(tmp)/'automation').mkdir()
            one = release()
            two = {**one, 'digest': 'sha256:'+'d'*64, 'release_tag': 'asterflow-v0.2.13-r2'}
            three = {**one, 'digest': 'sha256:'+'e'*64, 'release_tag': 'asterflow-v0.2.13-r3'}
            for item in (one, two, three):
                deploy.record_success(item)
            deploy.record_success(two, rollback=True)
            state = deploy.release_state()
            self.assertEqual(state['current'], two)
            self.assertEqual(state['previous'], one)
            self.assertNotIn(three, state['history'])

    def test_changed_settings_must_not_be_skipped(self):
        data = release(); config = deploy.replacement(data)
        info = {'State': {'Health': {'Status': 'healthy'}}, 'Config': {'Image': manifest.IMAGE + '@' + data['digest']}}
        self.assertTrue(deploy.already_deployed(info, config, data))
        config['services']['sub2api']['environment']['SUB2API_ANTHROPIC_AUDIT_ACCOUNT_IDS'] = '11'
        self.assertFalse(deploy.already_deployed(info, config, data))


class RollbackPreflightTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.data = release()
        self.info = {'Config': {'Image': 'old:version', 'Labels': {
            'com.docker.compose.project.config_files': str(self.root/'docker-compose.yml')}},
            'State': {'Health': {'Status': 'unhealthy'}}}
        self.current = self.data['migrations'].copy()
        patches = [patch.object(deploy.os, 'uname', return_value=SimpleNamespace(nodename=deploy.HOST, machine='x86_64')),
                   patch.object(deploy, 'ROOT', self.root),
                   patch.object(deploy, 'BASE', self.root/'docker-compose.yml'),
                   patch.object(deploy, 'OVERRIDE', self.root/'docker-compose.override.yml'),
                   patch.object(deploy, 'inspect', return_value=self.info),
                   patch.object(deploy, 'compose', return_value=b'{"services":{"sub2api":{"image":"old:version"}}}'),
                   patch.object(deploy, 'migrations', side_effect=lambda: self.current),
                   patch.object(deploy, 'sql', side_effect=lambda q: '{}' if 'settings' in q else '100'),
                   patch.object(deploy.shutil, 'disk_usage', return_value=SimpleNamespace(free=10**12))]
        for item in patches:
            item.start(); self.addCleanup(item.stop)

    def test_unhealthy_application_can_rollback_but_cannot_normal_deploy(self):
        with self.assertRaisesRegex(deploy.DeploymentError, 'not healthy'):
            deploy.preflight(self.data)
        self.assertEqual(deploy.preflight(self.data, rollback=True)[3], [])

    def test_rollback_still_refuses_removed_changed_or_new_migrations(self):
        for candidate in [{}, {'001_init.sql': 'e'*64}, {**self.current, '002_next.sql': 'd'*64}]:
            with self.subTest(candidate=candidate), self.assertRaisesRegex(deploy.DeploymentError, '[Mm]igration'):
                deploy.preflight({**self.data, 'migrations': candidate}, rollback=True)


if __name__ == '__main__':
    unittest.main()
