#!/usr/bin/env python3
"""Reproduce the licensed Go bcrypt subset without the unused OpenPGP module."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
DESTINATION = ROOT / "internal" / "cryptocompat"
FILES = (
    "LICENSE",
    "bcrypt/base64.go",
    "bcrypt/bcrypt.go",
    "bcrypt/bcrypt_test.go",
    "blowfish/block.go",
    "blowfish/cipher.go",
    "blowfish/const.go",
    "blowfish/blowfish_test.go",
)
OLD_IMPORT = b'"golang.org/x/crypto/blowfish"'
NEW_IMPORT = b'"github.com/ansoraGROUP/dupabase/internal/cryptocompat/blowfish"'


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", help="Official golang.org/x/crypto release to import")
    parser.add_argument("--check", action="store_true", help="Verify provenance and copied sources without editing")
    parser.add_argument("--check-latest", action="store_true", help="Fail if the selected upstream packages have changed")
    args = parser.parse_args()
    manifest_path = DESTINATION / "upstream.json"
    previous = json.loads(manifest_path.read_text()) if manifest_path.exists() else {}
    version = args.version or previous.get("version")
    if not version or not re.fullmatch(r"v\d+\.\d+\.\d+", version):
        parser.error("an explicit upstream release vMAJOR.MINOR.PATCH is required")
    query = "latest" if args.check_latest else version
    module = json.loads(subprocess.check_output(
        ["go", "mod", "download", "-json", "golang.org/x/crypto@" + query],
        cwd=ROOT, text=True,
    ))
    if module.get("Error") or not module.get("Sum") or not module.get("GoModSum"):
        raise RuntimeError("upstream download/checksum verification failed")
    source = Path(module["Dir"])
    hashes = {}
    generated = {}
    for name in FILES:
        original = (source / name).read_bytes()
        copied = original
        if name == "bcrypt/bcrypt.go":
            if original.count(OLD_IMPORT) != 1:
                raise RuntimeError("upstream import layout changed; manual compatibility review required")
            copied = original.replace(OLD_IMPORT, NEW_IMPORT)
        hashes[name] = {"upstreamSha256": hashlib.sha256(original).hexdigest(),
                        "localSha256": hashlib.sha256(copied).hexdigest()}
        generated[name] = copied
    manifest = {"module": "golang.org/x/crypto", "version": module["Version"],
                "sourceCommit": module.get("Origin", {}).get("Hash"),
                "moduleSum": module["Sum"], "goModSum": module["GoModSum"],
                "source": "https://github.com/golang/crypto/tree/" + module["Version"],
                "onlySourceModification": "bcrypt blowfish import redirected to the internal copied package",
                "files": hashes}
    if args.check or args.check_latest:
        changed = [name for name, data in generated.items()
                   if not (DESTINATION / name).exists() or (DESTINATION / name).read_bytes() != data]
        if changed:
            raise RuntimeError("upstream source differs: " + ", ".join(changed))
        if not args.check_latest and previous != manifest:
            raise RuntimeError("upstream provenance/checksums differ")
        print("Verified bcrypt/blowfish subset against official " + module["Version"])
        return
    DESTINATION.mkdir(parents=True, exist_ok=True)
    for name, data in generated.items():
        target = DESTINATION / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n")
    print("Imported bcrypt/blowfish subset from official " + module["Version"])


if __name__ == "__main__":
    try:
        main()
    except (OSError, RuntimeError, subprocess.CalledProcessError, ValueError) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
