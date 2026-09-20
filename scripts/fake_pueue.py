#!/usr/bin/env python3
"""Disposable Pueue CLI fixture used by the PTY tests; never invokes a daemon."""

import json
import os
import pathlib
import sys
import time


def main():
    args = sys.argv[1:]
    name = pathlib.Path(sys.argv[0]).name
    host = 'lab' if 'lab' in name else 'local'
    if '--version' in args:
        print('pueue 4.0.2')
        return
    if 'offline' in name:
        print('Fixture daemon unavailable', file=sys.stderr)
        sys.exit(1)
    created = '2026-09-20T08:00:00+08:00'
    start = '2026-09-20T08:01:00+08:00'
    end = '2026-09-20T08:02:00+08:00'
    def task(identifier, command, status, group='default'):
        return {'id': identifier, 'created_at': created, 'command': command,
                'original_command': command, 'path': '/tmp', 'label': host + ' job',
                'group': group, 'priority': 0, 'dependencies': [], 'status': status,
                'envs': {'FIXTURE_PRIVATE': 'must-not-be-displayed'}}
    if 'status' in args:
        tasks = {
            '7': task(7, "printf 'qjk/?:demo' # 中文", {'Running': {'enqueued_at': created, 'start': start}}),
            '8': task(8, 'echo queued', {'Queued': {'enqueued_at': created}}),
            '9': task(9, 'echo failed', {'Done': {'enqueued_at': created, 'start': start, 'end': end, 'result': {'Failed': 2}}}, 'training'),
            '10': task(10, 'echo complete', {'Done': {'enqueued_at': created, 'start': start, 'end': end, 'result': 'Success'}}, 'training'),
        }
        print(json.dumps({'tasks': tasks, 'groups': {'default': {'status': 'Running', 'parallel_tasks': 1},
                                                    'training': {'status': 'Running', 'parallel_tasks': 2}}}))
    elif 'log' in args:
        identifier = args[args.index('log') + 1]
        print(json.dumps({identifier: {'output': f'fixture log {host} #{identifier}\nqjk/?: log text\n中文 log line\n'}}))
    elif 'follow' in args:
        identifier = args[args.index('follow') + 1]
        print(f'fixture live {host} #{identifier}', flush=True)
        for i in range(300):
            print(f'live line {i} qjk/?:', flush=True)
            time.sleep(0.1)
    else:
        with open(os.environ['FIXTURE_CALLS'], 'a') as output:
            output.write(json.dumps(args) + '\n')
        if 'add' in args:
            print(42)
        else:
            print('Fixture accepted operation')


if __name__ == '__main__':
    main()
