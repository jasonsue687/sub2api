#!/usr/bin/env python3
"""Promote a successful branch build without rebuilding its image."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
from manifest import IMAGE, REPOSITORY, clean, validate_tag
from resolve import download_build, download_release


def api(path, payload=None, method=None):
    args = ['gh', 'api', f'repos/{REPOSITORY}/' + path]
    if method:
        args += ['--method', method]
    if payload is not None:
        args += ['--input', '-']
    return json.loads(subprocess.check_output(args, input=json.dumps(payload).encode() if payload is not None else None))


def tag_commit(tag):
    result = subprocess.run(['git', 'rev-parse', '--verify', f'refs/tags/{tag}^{{commit}}'],
                            capture_output=True, text=True)
    return result.stdout.strip() if result.returncode == 0 else None


def promote(tag, run_id):
    validate_tag(tag)
    sha = tag_commit(tag)
    # On a tag push, locate an already successful branch build for that exact commit.
    if not run_id:
        if not sha:
            raise ValueError('Provide build_run_id to create a new release tag')
        runs = api(f'actions/workflows/asterflow-build.yml/runs?branch=asterflow&status=success&head_sha={sha}&per_page=100')['workflow_runs']
        run_id = next((str(r['id']) for r in runs if r['head_sha'] == sha
                       and r['event'] in ('push', 'workflow_dispatch')), '')
        if not run_id:
            raise ValueError('Wait for a successful AsterFlow Build before publishing this tag')
    with tempfile.TemporaryDirectory() as temporary:
        release = download_build(run_id, Path(temporary))
    validate_tag(tag, release['version'])
    if sha and sha != release['sha']:
        raise ValueError('Existing tag points to a different commit; never move published tags')
    # Ensure the immutable digest is still retrievable; promotion does not rebuild.
    subprocess.run(['docker', 'buildx', 'imagetools', 'inspect', IMAGE + '@' + release['digest']], check=True)
    candidate = {**clean(release), 'release_tag': tag}
    existing = subprocess.run(['gh', 'api', f'repos/{REPOSITORY}/releases/tags/{tag}'],
                              capture_output=True, text=True)
    if existing.returncode == 0:
        metadata = json.loads(existing.stdout)
        if not metadata['draft']:
            with tempfile.TemporaryDirectory() as temporary:
                published = download_release(tag, Path(temporary))
            if published != candidate:
                raise ValueError('Release already published with different metadata')
            return published
        raise ValueError('A draft already exists; inspect it before retrying publication')
    if 'HTTP 404' not in existing.stderr:
        raise ValueError('Cannot determine whether the release already exists')
    if not sha:
        obj = api('git/tags', {'tag': tag, 'message': f'AsterFlow release {tag}',
                              'object': release['sha'], 'type': 'commit'}, 'POST')
        api('git/refs', {'ref': 'refs/tags/' + tag, 'sha': obj['sha']}, 'POST')
        subprocess.run(['git', 'fetch', 'origin', 'tag', tag], check=True)
    directory = Path('release'); directory.mkdir(exist_ok=True)
    path = directory / 'release.json'
    path.write_text(json.dumps(candidate, indent=2) + '\n')
    notes = directory / 'notes.md'
    notes.write_text(f"Commit: `{release['sha']}`\n\nImage: `{IMAGE}@{release['digest']}`\n\n"
                     f"Build: `{release['version']}` / run `{release['run_id']}`\n\n"
                     "Select this tag in AsterFlow Deploy. Publishing does not deploy production. "
                     "Rollback requires a compatible database migration history.\n")
    subprocess.run(['gh', 'release', 'create', tag, str(path), '--repo', REPOSITORY,
                    '--verify-tag', '--draft', '--title', tag, '--notes-file', str(notes)], check=True)
    # Upload before publishing: immutable releases prohibit subsequent asset changes.
    subprocess.run(['gh', 'release', 'edit', tag, '--repo', REPOSITORY, '--draft=false', '--latest=false'], check=True)
    with tempfile.TemporaryDirectory() as temporary:
        verified = download_release(tag, Path(temporary))
    if verified != candidate:
        raise ValueError('Published release differs from selected build')
    return verified


def main():
    release = promote(os.environ['RELEASE_TAG'], os.environ.get('BUILD_RUN_ID', '').strip())
    summary = f"Release: `{release['release_tag']}`\n\nImage: `{IMAGE}@{release['digest']}`\n"
    print(summary)
    if os.environ.get('GITHUB_STEP_SUMMARY'):
        with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as stream:
            stream.write(summary)


if __name__ == '__main__':
    main()
