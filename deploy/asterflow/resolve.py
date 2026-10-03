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


def main():
    run_id = os.environ['BUILD_RUN_ID']
    if not re.fullmatch(r'[1-9][0-9]*', run_id):
        raise ValueError('build_run_id must be numeric')
    run = json.loads(subprocess.check_output(['gh', 'api', f'repos/{REPOSITORY}/actions/runs/{run_id}']))
    check_run(run)
    subprocess.run(['git', 'merge-base', '--is-ancestor', run['head_sha'], 'HEAD'], check=True)
    subprocess.run(['gh', 'run', 'download', run_id, '--repo', REPOSITORY, '--name', 'asterflow-release', '--dir', 'release'], check=True)
    release = validate(json.loads(Path('release/release.json').read_text()))
    if (release['sha'] != run['head_sha'] or release['run_id'] != int(run_id)
            or release['run_attempt'] != run['run_attempt']):
        raise ValueError('Artifact does not match the selected successful run')
    print(json.dumps({key: release[key] for key in ('sha', 'digest', 'version', 'run_id')}))


if __name__ == '__main__':
    main()
