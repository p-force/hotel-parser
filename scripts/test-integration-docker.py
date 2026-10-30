#!/usr/bin/env python3
"""Запуск тестов внутри уже работающего PostgreSQL проекта; credentials не печатаются."""
import json
import os
import pathlib
import subprocess
import tempfile
import urllib.parse
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[1]


def output(args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


def main():
    container = output(["docker", "compose", "ps", "-q", "postgres"])
    if not container or "\n" in container:
        raise RuntimeError("one running project PostgreSQL container is required")
    info = json.loads(output(["docker", "inspect", container]))[0]
    if not info["State"]["Running"]:
        raise RuntimeError("project PostgreSQL container is not running")
    settings = dict(item.split("=", 1) for item in info["Config"]["Env"] if "=" in item)
    for key in ("POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB"):
        if not settings.get(key):
            raise RuntimeError("explicit PostgreSQL container settings are required")
    port = output(["docker", "exec", container, "sh", "-c",
                   'PGPASSWORD="$POSTGRES_PASSWORD" psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -qAtc "SHOW port"'])
    if not port.isdecimal():
        raise RuntimeError("cannot determine PostgreSQL port")
    image = json.loads(output(["docker", "image", "inspect", info["Image"]]))[0]
    arch = image["Architecture"]
    if arch not in ("amd64", "arm64"):
        raise RuntimeError("unsupported container architecture")
    env = os.environ.copy()
    env.update(GOOS="linux", GOARCH=arch)
    # ASCII key; deliberately no shell expansion or sourcing .env.
    env["CGO_ENABLED"] = "0"
    user = urllib.parse.quote(settings["POSTGRES_USER"], safe="")
    password = urllib.parse.quote(settings["POSTGRES_PASSWORD"], safe="")
    database = urllib.parse.quote(settings["POSTGRES_DB"], safe="")
    env["HOTEL_PARSER_DATABASE_URL"] = f"postgresql://{user}:{password}@127.0.0.1:{port}/{database}?sslmode=disable"
    remote = "/tmp/hotel-parser-integration-" + uuid.uuid4().hex
    subprocess.run(["docker", "exec", container, "mkdir", "-p", remote + "/internal/storage", remote + "/internal/collect", remote + "/internal/schedule"], check=True)
    subprocess.run(["docker", "cp", str(ROOT / "configs"), container + ":" + remote + "/configs"], check=True)
    subprocess.run(["docker", "cp", str(ROOT / "internal/collect/testdata"), container + ":" + remote + "/internal/collect/testdata"], check=True)
    try:
        with tempfile.TemporaryDirectory(prefix="hotel-parser-tests-") as directory:
            for package in ("storage", "collect", "schedule"):
                binary = str(pathlib.Path(directory) / (package + ".test"))
                subprocess.run(["go", "test", "-c", "-tags=integration", "-o", binary,
                                "./internal/" + package], cwd=ROOT, env=env, check=True)
                target = remote + "/" + package + ".test"
                subprocess.run(["docker", "cp", binary, container + ":" + target], check=True)
                subprocess.run(["docker", "exec", "--workdir", remote + "/internal/" + package, "--env", "HOTEL_PARSER_DATABASE_URL", container,
                                target, "-test.v", "-test.run", "^TestPostgres", "-test.timeout=120s"], env=env, check=True)
    finally:
        subprocess.run(["docker", "exec", container, "rm", "-rf", remote], check=True)


if __name__ == "__main__":
    try:
        main()
    except (subprocess.SubprocessError, RuntimeError, KeyError, ValueError):
        # Не выводим command/env с connection string даже при ошибке subprocess.
        raise SystemExit("Integration tests failed; check Docker PostgreSQL configuration and test output")
