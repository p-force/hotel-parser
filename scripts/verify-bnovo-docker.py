#!/usr/bin/env python3
"""Explicit opt-in live Bnovo verification; default integration tests stay offline."""
import argparse
import json
import os
import pathlib
import runpy
import signal
import subprocess
import tempfile
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]


def rate_conforms(evidence):
    """A persistence PASS is not a rate-limit PASS."""
    return len(evidence.get("Requests", [])) < 2 or evidence.get("MinimumStartGapMS", 0) >= 500


def stop_verification(container, remote):
    result = subprocess.run(["docker", "exec", container, "cat", remote + "/evidence.json.pid"], capture_output=True, text=True)
    pid = result.stdout.strip()
    if result.returncode == 0 and pid.isdecimal():
        subprocess.run(["docker", "exec", container, "sh", "-c", 'kill -TERM "$1"', "verification", pid], capture_output=True)


def interrupted(*_):
    raise KeyboardInterrupt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--phase", choices=("smoke", "full", "replay"), required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--smoke-evidence", type=pathlib.Path)
    parser.add_argument("--replay-evidence", type=pathlib.Path)
    parser.add_argument("--ca-bundle", type=pathlib.Path,
                        help="trusted PEM roots; TLS verification remains enabled")
    parser.add_argument("--private-raw-dir", type=pathlib.Path,
                        help="optional private HTML evidence outside the checkout; never publish")
    args = parser.parse_args()
    sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    if args.phase == "full":
        if not args.smoke_evidence:
            parser.error("full requires successful --smoke-evidence")
        smoke = json.loads(args.smoke_evidence.read_text())
        if not smoke.get("smoke_passed") or smoke.get("base_sha") != sha:
            parser.error("smoke did not pass on this base SHA")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    raw = args.private_raw_dir.resolve() if args.private_raw_dir else None
    if raw and raw.is_relative_to(ROOT):
        parser.error("raw HTML must remain outside the repository")
    if args.phase == "replay" and (not args.replay_evidence or not raw or not raw.is_dir()):
        parser.error("replay requires --replay-evidence and existing --private-raw-dir")
    if raw and args.phase != "replay":
        raw.mkdir(parents=True, exist_ok=False, mode=0o700)
    helper = runpy.run_path(str(ROOT / "scripts/test-integration-docker.py"))
    container, env = helper["docker_environment"]()
    candidates = [args.ca_bundle] if args.ca_bundle else [pathlib.Path("/etc/ssl/cert.pem"), pathlib.Path("/etc/ssl/certs/ca-certificates.crt")]
    roots = next((p for p in candidates if p and p.is_file()), None)
    if roots is None:
        parser.error("provide --ca-bundle: PostgreSQL image has no trusted TLS roots")
    remote = "/tmp/hotel-parser-live-" + uuid.uuid4().hex
    env.update(HOTEL_PARSER_LIVE_BNOVO=args.phase,
               HOTEL_PARSER_E2E_OUTPUT=remote + "/evidence.json",
               HOTEL_PARSER_E2E_BASE_SHA=sha,
               SSL_CERT_FILE=remote + "/ca-certificates.crt")
    env["HOTEL_PARSER_E2E_PRIVATE_RAW"] = remote + "/private-raw" if raw else ""
    env["HOTEL_PARSER_E2E_REPLAY"] = remote + "/replay.json" if args.phase == "replay" else ""
    subprocess.run(["docker", "exec", container, "mkdir", "-p", remote + "/internal/schedule"], check=True)
    subprocess.run(["docker", "cp", str(ROOT / "configs"), container + ":" + remote + "/configs"], check=True)
    subprocess.run(["docker", "cp", str(roots), container + ":" + env["SSL_CERT_FILE"]], check=True)
    if args.phase == "replay":
        subprocess.run(["docker", "cp", str(raw), container + ":" + remote + "/private-raw"], check=True)
        subprocess.run(["docker", "cp", str(args.replay_evidence), container + ":" + env["HOTEL_PARSER_E2E_REPLAY"]], check=True)
    print("Live phase:", args.phase, "| source interval >=500ms | singleton guard enabled", flush=True)
    if args.phase == "full":
        print("122 base jobs; up to 366 completed attempts plus canceled in-flight requests; smoke gate passed", flush=True)
    if args.phase == "replay":
        print("OFFLINE replay: no public HTTP; limits and singleton remain enabled", flush=True)
    try:
        with tempfile.TemporaryDirectory(prefix="bnovo-live-") as directory:
            binary = str(pathlib.Path(directory) / "schedule.test")
            subprocess.run(["go", "test", "-c", "-tags=integration", "-o", binary, "./internal/schedule"], cwd=ROOT, env=env, check=True)
            subprocess.run(["docker", "cp", binary, container + ":" + remote + "/schedule.test"], check=True)
            command = ["docker", "exec", "--workdir", remote + "/internal/schedule"]
            for name in ("HOTEL_PARSER_DATABASE_URL", "HOTEL_PARSER_LIVE_BNOVO", "HOTEL_PARSER_E2E_OUTPUT", "HOTEL_PARSER_E2E_BASE_SHA", "SSL_CERT_FILE", "HOTEL_PARSER_E2E_PRIVATE_RAW", "HOTEL_PARSER_E2E_REPLAY"):
                command += ["--env", name]
            command += [container, remote + "/schedule.test", "-test.v", "-test.run=^TestLiveBnovoVerification$", "-test.timeout=15m"]
            with (output / "run.log").open("w") as log:
                process = subprocess.Popen(command, env=env, stdout=log, stderr=subprocess.STDOUT)
                try:
                    returncode = process.wait()
                except KeyboardInterrupt:
                    stop_verification(container, remote)
                    process.wait(timeout=20)
                    raise
            copied = subprocess.run(["docker", "cp", container + ":" + remote + "/evidence.json", str(output / "evidence.json")], capture_output=True)
            if copied.returncode:
                raise RuntimeError("no evidence was produced")
            subprocess.run(["docker", "cp", container + ":" + remote + "/evidence.json.processes.log", str(output / "processes.log")], capture_output=True)
            if raw and args.phase != "replay":
                subprocess.run(["docker", "cp", container + ":" + remote + "/private-raw/.", str(raw)], check=True)
            evidence = json.loads((output / "evidence.json").read_text())
            if args.phase == "full" and not rate_conforms(evidence):
                print("FAILED rate conformance despite completed jobs:", evidence["MinimumStartGapMS"], "ms")
                returncode = 1
            print("Evidence:", output / "evidence.json", "| validation exit:", returncode)
            return returncode
    finally:
        subprocess.run(["docker", "exec", container, "rm", "-rf", remote], check=True)


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupted)
    try:
        raise SystemExit(main())
    except (subprocess.SubprocessError, RuntimeError, KeyError, ValueError):
        raise SystemExit("Verification failed; inspect sanitized evidence/logs (credentials are not printed)")
