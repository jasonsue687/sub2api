#!/usr/bin/env python3
"""Accept only successful build artifacts from this repository's asterflow branch."""
import json
import os
from pathlib import Path
import re
import subprocess
from manifest import REPOSITORY, validate


def check_run(run):
    if (run.get('conclusion') != 'success' or run.get('status') != 'completed'
            or run.get('head_branch') != 'asterflow'
            or run.get('event') not in ('push', 'workflow_dispatch')
            or run.get('path') != '.github/workflows/asterflow-build.yml'
            or run.get('head_repository', {}).get('full_name') != REPOSITORY):
        raise ValueError('Select a successful AsterFlow Build on asterflow in the personal fork')


def download_build(run_id, directory):
    if not re.fullmatch(r'[1-9][0-9]*', str(run_id)):
        raise ValueError('build_run_id must be numeric')
    run = json.loads(subprocess.check_output(['gh', 'api', f'repos/{REPOSITORY}/actions/runs/{run_id}']))
    check_run(run)
    subprocess.run(['git', 'merge-base', '--is-ancestor', run['head_sha'], 'HEAD'], check=True)
    subprocess.run(['gh', 'run', 'download', str(run_id), '--repo', REPOSITORY,
                    '--name', 'asterflow-release', '--dir', str(directory)], check=True)
    release = validate(json.loads((directory / 'release.json').read_text()))
    if (release['sha'] != run['head_sha'] or release['run_id'] != int(run_id)
            or release['run_attempt'] != run['run_attempt']):
        raise ValueError('Artifact does not match the selected successful run')
    return release


def check_release(metadata, release, tag, tag_sha, asset_bytes):
    from manifest import published
    import hashlib
    published(release)
    if (metadata.get('draft') is not False or metadata.get('prerelease') is not False
            or metadata.get('immutable') is not True or metadata.get('tag_name') != tag
            or release['release_tag'] != tag or release['sha'] != tag_sha):
        raise ValueError('Select a published immutable AsterFlow release matching its Git tag')
    assets = [a for a in metadata.get('assets', []) if a['name'] == 'release.json']
    digest = 'sha256:' + hashlib.sha256(asset_bytes).hexdigest()
    if len(assets) != 1 or assets[0].get('digest') != digest:
        raise ValueError('Release asset checksum mismatch')


def download_release(tag, directory):
    from manifest import validate_tag
    validate_tag(tag)
    metadata = json.loads(subprocess.check_output(['gh', 'api', f'repos/{REPOSITORY}/releases/tags/{tag}']))
    subprocess.run(['gh', 'release', 'download', tag, '--repo', REPOSITORY,
                    '--pattern', 'release.json', '--dir', str(directory)], check=True)
    path = directory / 'release.json'
    release = json.loads(path.read_bytes())
    tag_sha = subprocess.check_output(['gh', 'api', f'repos/{REPOSITORY}/commits/{tag}', '--jq', '.sha'], text=True).strip()
    check_release(metadata, release, tag, tag_sha, path.read_bytes())
    subprocess.run(['git', 'merge-base', '--is-ancestor', tag_sha, 'HEAD'], check=True)
    subprocess.run(['gh', 'release', 'verify-asset', tag, str(path), '--repo', REPOSITORY], check=True)
    return release


def main():
    tag = os.environ.get('RELEASE_TAG', '').strip()
    if not tag and os.environ.get('OPERATION') in ('rollback', 'preflight-rollback'):
        config = str(Path(os.environ['RUNNER_TEMP']) / 'asterflow-ssh/config')
        state = json.loads(subprocess.check_output(['ssh', '-F', config,
                    'asterflow-prod-account-central-01', 'status']))
        tag = state.get('rollback_release', '')
        if not tag:
            raise ValueError('No recorded rollback release; select an explicit compatible release tag')
    release = download_release(tag, Path('release'))
    print(json.dumps({key: release[key] for key in ('release_tag', 'sha', 'digest', 'version')}))


if __name__ == '__main__':
    main()
