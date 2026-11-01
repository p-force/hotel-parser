#!/usr/bin/env python3
"""Opt-in: один сценарий TravelLine через штатный scheduler и Docker PostgreSQL."""
import argparse
import pathlib
import runpy
import subprocess
import tempfile
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--ca-bundle", type=pathlib.Path, default=pathlib.Path("/etc/ssl/cert.pem"))
    args = parser.parse_args()
    if not args.ca_bundle.is_file():
        parser.error("trusted CA bundle required; TLS verification remains enabled")
    if args.output.exists():
        parser.error("output already exists")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    helper = runpy.run_path(str(ROOT / "scripts/test-integration-docker.py"))
    container, env = helper["docker_environment"]()
    remote = "/tmp/hotel-parser-tl-" + uuid.uuid4().hex
    env.update(HOTEL_PARSER_LIVE_TRAVELLINE="1", HOTEL_PARSER_E2E_OUTPUT=remote + "/evidence.json", SSL_CERT_FILE=remote + "/roots.pem")
    subprocess.run(["docker", "exec", container, "mkdir", "-p", remote + "/internal/schedule"], check=True)
    try:
        subprocess.run(["docker", "cp", str(ROOT / "configs"), container + ":" + remote + "/configs"], check=True)
        subprocess.run(["docker", "cp", str(args.ca_bundle), container + ":" + env["SSL_CERT_FILE"]], check=True)
        with tempfile.TemporaryDirectory(prefix="travelline-smoke-") as directory:
            binary = str(pathlib.Path(directory) / "schedule.test")
            subprocess.run(["go", "test", "-c", "-tags=integration", "-o", binary, "./internal/schedule"], cwd=ROOT, env=env, check=True)
            subprocess.run(["docker", "cp", binary, container + ":" + remote + "/schedule.test"], check=True)
            command = ["docker", "exec", "--workdir", remote + "/internal/schedule"]
            for name in ("HOTEL_PARSER_DATABASE_URL", "HOTEL_PARSER_LIVE_TRAVELLINE", "HOTEL_PARSER_E2E_OUTPUT", "SSL_CERT_FILE"):
                command += ["--env", name]
            command += [container, remote + "/schedule.test", "-test.v", "-test.run=^TestLiveTravelLineSmoke$", "-test.timeout=90s"]
            result = subprocess.run(command, env=env)
            subprocess.run(["docker", "cp", container + ":" + remote + "/evidence.json", str(args.output)], check=True)
            if result.returncode:
                raise RuntimeError("live smoke failed; inspect safe evidence")
    finally:
        subprocess.run(["docker", "exec", container, "rm", "-rf", remote], check=True)


if __name__ == "__main__":
    try:
        main()
    except (subprocess.SubprocessError, RuntimeError):
        raise SystemExit("TravelLine smoke failed; no automatic retry")
