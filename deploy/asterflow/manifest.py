#!/usr/bin/env python3
"""Release metadata contains no credentials and binds one build to one digest."""
import hashlib
import json
import os
from pathlib import Path
import re

REPOSITORY = "jasonsue687/sub2api"
IMAGE = "ghcr.io/jasonsue687/sub2api"


def validate(data):
    if data.get("schema") != 1 or data.get("repository") != REPOSITORY:
        raise ValueError("unexpected release schema/repository")
    for key, pattern in {"sha": r"[0-9a-f]{40}", "digest": r"sha256:[0-9a-f]{64}",
                         "version": r"[0-9]+\.[0-9]+\.[0-9]+-asterflow\.[0-9]+"}.items():
        if not re.fullmatch(pattern, str(data.get(key, ""))):
            raise ValueError("invalid " + key)
    for key in ("run_id", "run_attempt"):
        if type(data.get(key)) is not int or data[key] <= 0:
            raise ValueError("invalid " + key)
    migrations = data.get("migrations")
    if not isinstance(migrations, dict) or not migrations:
        raise ValueError("missing migration manifest")
    for name, checksum in migrations.items():
        if not re.fullmatch(r"[0-9][0-9A-Za-z_]*\.sql", name) or not re.fullmatch(r"[0-9a-f]{64}", checksum):
            raise ValueError("invalid migration entry")
    return data


def create(root):
    migrations = {}
    for path in sorted((root / "backend/migrations").glob("*.sql")):
        content = path.read_text().strip()
        if content:
            migrations[path.name] = hashlib.sha256(content.encode()).hexdigest()
    return validate({"schema": 1, "repository": REPOSITORY,
                     "sha": os.environ["GITHUB_SHA"], "digest": os.environ["IMAGE_DIGEST"],
                     "version": os.environ["RELEASE_VERSION"],
                     "run_id": int(os.environ["GITHUB_RUN_ID"]),
                     "run_attempt": int(os.environ["GITHUB_RUN_ATTEMPT"]),
                     "migrations": migrations})


if __name__ == "__main__":
    Path("release.json").write_text(json.dumps(create(Path.cwd()), indent=2) + "\n")
