#!/usr/bin/env python3
"""Foreground diagnostics against Docker PostgreSQL; never sources .env or prints DSN."""
import argparse
import json
import os
import pathlib
import subprocess
import urllib.parse
import uuid

ROOT = pathlib.Path(__file__).resolve().parents[3]
PACKAGE = './docs/diagnostics/full-collection-audit-2026-11-10/'


def local_environment():
    def output(args):
        return subprocess.check_output(args, cwd=ROOT, text=True).strip()
    container = output(['docker', 'compose', 'ps', '-q', 'postgres'])
    if not container or '\n' in container:
        raise RuntimeError('one running project PostgreSQL container is required')
    info = json.loads(output(['docker', 'inspect', container]))[0]
    if not info['State']['Running']:
        raise RuntimeError('PostgreSQL is not running')
    settings = dict(item.split('=', 1) for item in info['Config']['Env'] if '=' in item)
    ports = info['NetworkSettings']['Ports']['5432/tcp']
    port = next(p['HostPort'] for p in ports if p['HostIp'] in ('127.0.0.1', '0.0.0.0'))
    quote = lambda v: urllib.parse.quote(v, safe='')
    dsn = ('postgresql://' + quote(settings['POSTGRES_USER']) + ':' + quote(settings['POSTGRES_PASSWORD'])
           + '@127.0.0.1:' + port + '/' + quote(settings['POSTGRES_DB']) + '?sslmode=disable')
    env = {k: v for k, v in os.environ.items()
           if not k.startswith(('YANDEX_', 'VERCEL_', 'CRON_', 'HOTEL_PARSER_'))}
    env['HOTEL_PARSER_DATABASE_URL'] = dsn
    return env


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--live', action='store_true', help='explicitly allow all configured + regular source scenarios')
    parser.add_argument('--inspect', metavar='SCHEMA', help='read-only export of an existing local diagnostic schema; no HTTP')
    parser.add_argument('--out', required=True, type=pathlib.Path)
    args = parser.parse_args()
    if args.live == bool(args.inspect):
        parser.error('choose exactly one of --live or --inspect')
    args.out.mkdir(mode=0o700, parents=True, exist_ok=True)
    if args.live and (args.out / 'plan.json').exists():
        raise RuntimeError('refuse to overwrite an existing measurement')
    env = local_environment()
    mode = 'inspect' if args.inspect else 'harness'
    binary = args.out / mode
    subprocess.run(['go', 'build', '-o', str(binary), PACKAGE + mode], cwd=ROOT, env=env, check=True)
    if args.inspect:
        command = [str(binary), '--schema', args.inspect, '--out', str(args.out / 'readback.json')]
    else:
        schema = 'hp_audit_20261110_' + uuid.uuid4().hex[:10]
        command = [str(binary), '--live', '--schema', schema, '--out', str(args.out)]
    subprocess.run(command, cwd=ROOT, env=env, check=True)


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, subprocess.SubprocessError, KeyError, ValueError):
        raise SystemExit('Local diagnostics failed; see sanitized command output. Credentials are not printed.')
