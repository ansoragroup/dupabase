#!/usr/bin/env python3
"""Full Docker fixtures. Creates and removes only containers with its unique label."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
PASSWORD = "isolated-major-compatibility-password"
ADMIN_EMAIL = "major-compatibility-admin@example.test"
ADMIN_PASSWORD = "IsolatedCompatibility2026!"
SECRET = "isolated-major-compatibility-platform-secret-20261002"
run_id = uuid.uuid4().hex[:12]
label = f"dupabase.compatibility={run_id}"
network = f"dupabase-compat-{run_id}"
containers = []


def command(args, capture=True, env=None):
    return subprocess.run(args, cwd=ROOT, env=env, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None,
                          stderr=subprocess.PIPE if capture else None).stdout


def remove(name):
    result = subprocess.run(["docker", "inspect", "--format", '{{index .Config.Labels "dupabase.compatibility"}}', name], capture_output=True, text=True)
    if result.returncode == 0 and result.stdout.strip() == run_id:
        command(["docker", "rm", "--force", "--volumes", name])


def start(name, args):
    containers.append(name)
    command(["docker", "run", "--detach", "--name", name, "--label", label, "--network", network, *args])
    return name


def port(name, internal):
    return int(command(["docker", "port", name, f"{internal}/tcp"]).strip().rsplit(":", 1)[1])


def wait_http(url, method="GET"):
    last_error = None
    for _ in range(480):
        try:
            with urllib.request.urlopen(urllib.request.Request(url, method=method), timeout=2) as response:
                if 200 <= response.status < 300:
                    return
        except (OSError, ValueError) as error:
            last_error = error
        time.sleep(0.25)
    raise RuntimeError(f"Disposable fixture did not become ready: {url}: {last_error}")


def psql(pg, query, database="postgres"):
    return command(["docker", "exec", pg, "psql", "-U", "postgres", "-d", database, "-X", "-v", "ON_ERROR_STOP=1", "-tAc", query]).strip()


def postgres(major):
    command(["docker", "pull", f"postgres:{major}-alpine"])
    name = start(f"{network}-pg{major}", ["--network-alias", "maintenance-pg", "-p", "127.0.0.1::5432",
                 "-e", f"POSTGRES_PASSWORD={PASSWORD}", "-e", "POSTGRES_DB=dupabase_maintenance_ci", f"postgres:{major}-alpine"])
    for _ in range(240):
        # The entrypoint's temporary initialization server listens only on a
        # Unix socket. Wait for the final TCP server before creating fixtures.
        if subprocess.run(["docker", "exec", name, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0:
            break
        time.sleep(0.25)
    else:
        raise RuntimeError(f"PostgreSQL {major} did not become ready")
    psql(name, "CREATE DATABASE dupabase_maintenance_platform")
    return name


def application(image, database, suffix):
    gateway = command(["docker", "network", "inspect", "--format", '{{(index .IPAM.Config 0).Gateway}}', network]).strip()
    name = start(f"{network}-{suffix}", ["-p", "127.0.0.1::3333",
                 "-e", f"DATABASE_URL=postgres://postgres:{PASSWORD}@maintenance-pg:5432/{database}?sslmode=disable",
                 "-e", f"ADMIN_EMAIL={ADMIN_EMAIL}", "-e", f"ADMIN_PASSWORD={ADMIN_PASSWORD}",
                 "-e", f"PLATFORM_JWT_SECRET={SECRET}", "-e", "LOG_FORMAT=json", "-e", "PORT=3333",
                 "-e", "TRUST_PROXY=true", "-e", f"TRUSTED_PROXY_CIDRS={gateway}/32",
                 "-e", "SITE_URL=http://127.0.0.1:3333", "-e", "REST_MAX_RESPONSE_BYTES=4096",
                 "-e", "BACKUP_ALLOWED_ENDPOINTS=http://maintenance-s3:9090", image])
    address = f"http://127.0.0.1:{port(name, 3333)}"
    wait_http(address + "/health")
    return name, address


def test(script, env):
    output = command(["node", f"tests/{script}"], env=env)
    print(output.strip(), flush=True)
    return output.strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", required=True)
    parser.add_argument("--baseline-image")
    parser.add_argument("--postgres", nargs="+", type=int, default=[14, 15, 16, 17, 18], choices=[14, 15, 16, 17, 18])
    parser.add_argument("--report")
    args = parser.parse_args()
    versions = sorted(set(args.postgres))
    report = {"image": args.image, "postgres": [], "baselineImage": args.baseline_image}
    command(["docker", "network", "create", "--label", label, network])
    try:
        with tempfile.TemporaryDirectory(prefix="dupabase-compatibility-") as scratch:
            archive_dir = Path(scratch) / "archives"
            archive_dir.mkdir(mode=0o700)
            sdks = [("1.35.7", "1"), ("2.95.3", "2")]
            for version, _ in sdks:
                command(["npm", "install", "--prefix", str(Path(scratch) / f"sdk-{version}"),
                         "--ignore-scripts", "--no-audit", "--no-fund", "--save-exact", f"@supabase/supabase-js@{version}"])
            s3 = start(f"{network}-s3", ["--network-alias", "maintenance-s3", "-p", "127.0.0.1::9090",
                       "-e", "httpPort=9090", "-e", "initialBuckets=dupabase-maintenance", "adobe/s3mock:latest"])
            s3url = f"http://127.0.0.1:{port(s3, 9090)}"
            wait_http(s3url + "/dupabase-maintenance", "PUT")
            for major in versions:
                print(f"Starting PostgreSQL {major} contracts", flush=True)
                pg = postgres(major)
                app, url = application(args.image, "dupabase_maintenance_platform", f"app{major}")
                env = dict(os.environ, DUPABASE_TEST_URL=url, DUPABASE_TEST_ADMIN_EMAIL=ADMIN_EMAIL,
                           DUPABASE_TEST_ADMIN_PASSWORD=ADMIN_PASSWORD, DUPABASE_TEST_POSTGRES_MAJOR=str(major),
                           DUPABASE_TEST_S3_INTERNAL_URL="http://maintenance-s3:9090", DUPABASE_TEST_REST_MAX_BYTES="4096",
                           DUPABASE_TEST_UPGRADE_DIR=str(archive_dir))
                row = {"major": major, "serverVersion": psql(pg, "SHOW server_version"), "security": test("security_regression.mjs", env), "sdk": []}
                for version, sdk_major in sdks:
                    sdk_env = dict(env, DUPABASE_TEST_SDK_ROOT=str(Path(scratch) / f"sdk-{version}"),
                                   DUPABASE_TEST_SDK_MAJOR=sdk_major, DUPABASE_TEST_SDK_VERSION=version)
                    row["sdk"].append(json.loads(test("sdk_contract.mjs", sdk_env)))
                row["sdk"].append(json.loads(test("sdk_contract.mjs", dict(env, DUPABASE_TEST_SDK_MAJOR="2"))))
                if versions == [14, 15, 16, 17, 18]:
                    row["upgrades"] = test("database_upgrade.mjs", env)
                remove(app)
                if major == 16 and args.baseline_image:
                    psql(pg, "CREATE DATABASE dupabase_maintenance_baseline")
                    psql(pg, "CREATE DATABASE dupabase_maintenance_candidate_perf")
                    baseline_app, baseline_url = application(args.baseline_image, "dupabase_maintenance_baseline", "baseline-perf")
                    candidate_app, candidate_url = application(args.image, "dupabase_maintenance_candidate_perf", "candidate-perf")
                    row["performance"] = json.loads(test("performance_regression.mjs", dict(env,
                         DUPABASE_TEST_URL=candidate_url, DUPABASE_TEST_BASELINE_URL=baseline_url)))
                    remove(candidate_app)
                    remove(baseline_app)
                    psql(pg, "CREATE DATABASE dupabase_maintenance_release_upgrade")
                    marker = str(Path(scratch) / "release-marker.json")
                    migration_counts = []
                    for phase, image in [("seed", args.baseline_image), ("upgrade", args.image), ("rollback", args.baseline_image)]:
                        switched, switched_url = application(image, "dupabase_maintenance_release_upgrade", f"release-{phase}")
                        test("release_upgrade.mjs", dict(env, DUPABASE_TEST_URL=switched_url,
                             DUPABASE_TEST_RELEASE_MARKER=marker, DUPABASE_TEST_RELEASE_PHASE=phase))
                        migration_counts.append(psql(pg, "SELECT count(*) FROM platform._migrations", "dupabase_maintenance_release_upgrade"))
                        remove(switched)
                    assert len(set(migration_counts)) == 1, "Image upgrade/rollback changed historical migration metadata"
                    row["imageUpgradeAndRollback"] = "passed"
                remove(pg)
                report["postgres"].append(row)
            if versions == [14, 15, 16, 17, 18]:
                pg = postgres(14)
                app, url = application(args.image, "dupabase_maintenance_platform", "downgrade-rejection")
                env = dict(os.environ, DUPABASE_TEST_URL=url, DUPABASE_TEST_ADMIN_EMAIL=ADMIN_EMAIL,
                           DUPABASE_TEST_ADMIN_PASSWORD=ADMIN_PASSWORD, DUPABASE_TEST_POSTGRES_MAJOR="14",
                           DUPABASE_TEST_UPGRADE_DIR=str(archive_dir), DUPABASE_TEST_UPGRADE_MODE="reject-downgrade")
                report["downgradeRejection"] = test("database_upgrade.mjs", env)
                remove(app)
                remove(pg)
            report["status"] = "passed"
            print(json.dumps(report), flush=True)
            if args.report:
                Path(args.report).write_text(json.dumps(report, indent=2) + "\n")
    except Exception:
        for container in containers:
            result = subprocess.run(["docker", "logs", "--tail", "30", container], capture_output=True, text=True)
            if result.returncode == 0:
                print(f"Fixture diagnostics for {container}:\n{result.stdout}\n{result.stderr}", flush=True)
        raise
    finally:
        for container in reversed(containers):
            remove(container)
        result = subprocess.run(["docker", "network", "inspect", "--format", '{{index .Labels "dupabase.compatibility"}}', network], capture_output=True, text=True)
        if result.returncode == 0 and result.stdout.strip() == run_id:
            command(["docker", "network", "rm", network])


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as error:
        print(error.stdout or "", flush=True)
        print(error.stderr or "", flush=True)
        raise SystemExit(error.returncode)
