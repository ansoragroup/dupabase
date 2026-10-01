#!/usr/bin/env python3
"""Check copied bcrypt/Blowfish against the official Go vulnerability database."""

import json
from pathlib import Path
import re
import sys
import urllib.request


DATABASE = "https://vuln.go.dev"
PACKAGES = {"golang.org/x/crypto/bcrypt", "golang.org/x/crypto/blowfish"}


def version(value):
    match = re.fullmatch(r"v?(\d+)\.(\d+)\.(\d+)(?:-.*)?", value)
    if not match:
        raise ValueError("unsupported vulnerability version: " + value)
    return tuple(map(int, match.groups()))


def read_json(path):
    with urllib.request.urlopen(DATABASE + path, timeout=30) as response:
        return json.load(response)


def main():
    root = Path(__file__).resolve().parents[1]
    manifest = json.loads((root / "internal/cryptocompat/upstream.json").read_text())
    selected = version(manifest["version"])
    modules = read_json("/index/modules.json")
    module = next((entry for entry in modules if entry["path"] == manifest["module"]), None)
    if module is None:
        raise ValueError("upstream module missing from vulnerability index")
    affected = []
    for entry in module["vulns"]:
        if entry.get("fixed") and selected >= version(entry["fixed"]):
            continue
        advisory = read_json("/ID/" + entry["id"] + ".json")
        if advisory.get("withdrawn"):
            continue
        for item in advisory["affected"]:
            if item["package"]["name"] != manifest["module"]:
                continue
            ecosystem = item.get("ecosystem_specific", {})
            packages = ecosystem.get("imports", []) + ecosystem.get("packages", [])
            if not packages or any(package["path"] in PACKAGES for package in packages):
                affected.append(entry["id"])
    if affected:
        raise ValueError("copied crypto needs upstream security review: " + ", ".join(sorted(set(affected))))
    print("No advisories affect the copied bcrypt/Blowfish packages at " + manifest["version"])


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, StopIteration) as error:
        print(str(error), file=sys.stderr)
        sys.exit(1)
