#!/usr/bin/env python3
"""Build the immutable release tag on the candidate's native architecture."""
import argparse
import io
from pathlib import PurePosixPath
import re
import subprocess
import tarfile
import tempfile

parser = argparse.ArgumentParser()
parser.add_argument("--tag", default="v1.0.0")
parser.add_argument("--image", default="dupabase:release-baseline")
args = parser.parse_args()
if not re.fullmatch(r"v\d+\.\d+\.\d+", args.tag):
    raise SystemExit("A stable release tag is required")
revision = subprocess.check_output(["git", "rev-parse", f"{args.tag}^{{commit}}"], text=True).strip()
with tempfile.TemporaryDirectory(prefix="dupabase-release-baseline-") as directory:
    data = subprocess.check_output(["git", "archive", "--format=tar", args.tag])
    with tarfile.open(fileobj=io.BytesIO(data)) as archive:
        for member in archive.getmembers():
            if member.name.startswith("/") or ".." in PurePosixPath(member.name).parts:
                raise SystemExit("Unsafe repository archive path")
            if member.issym() or member.islnk():
                raise SystemExit("Release baseline archives must not contain links")
        archive.extractall(directory)
    subprocess.run(["docker", "build", "--file", directory + "/.deploy/_shared/Dockerfile",
                    "--build-arg", "BUILD_REVISION=" + revision, "--tag", args.image, directory], check=True)
print(f"Baseline {args.tag}: {revision}")
