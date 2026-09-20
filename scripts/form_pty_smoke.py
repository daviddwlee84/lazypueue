#!/usr/bin/env python3
"""Exercise shared forms in a real PTY using only disposable fake Pueue state.

Build the app, then: python3 scripts/form_pty_smoke.py --binary ./bin/lazypueue
Python's standard library is sufficient; no emulator or real daemon is needed.
"""

import argparse
import fcntl
import json
import os
import pathlib
import pty
import select
import signal
import struct
import subprocess
import tempfile
import termios
import time


FIXTURE = r'''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
if '--version' in args:
    print('pueue 4.0.2')
elif 'status' in args:
    print(json.dumps({'tasks': {}, 'groups': {'default': {'status': 'Running', 'parallel_tasks': 1}}}))
elif 'add' in args:
    with open(os.environ['FIXTURE_CALLS'], 'a') as output:
        output.write(json.dumps(args) + '\n')
    print(42)
else:
    print('unexpected fixture command: ' + repr(args), file=sys.stderr)
    sys.exit(1)
'''


class Terminal:
    def __init__(self, command, env):
        self.master, self.slave = pty.openpty()
        self.resize(120, 36)
        self.before = termios.tcgetattr(self.slave)
        self.process = subprocess.Popen(command, stdin=self.slave, stdout=self.slave,
                                        stderr=self.slave, env=env)
        self.output = b''

    def __enter__(self):
        return self

    def __exit__(self, *_):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=3)
        os.close(self.master)
        os.close(self.slave)

    def resize(self, width, height):
        fcntl.ioctl(self.slave, termios.TIOCSWINSZ, struct.pack('HHHH', height, width, 0, 0))
        if hasattr(self, 'process'):
            self.process.send_signal(signal.SIGWINCH)

    def read(self, duration=0.2):
        end = time.monotonic() + duration
        while time.monotonic() < end:
            ready, _, _ = select.select([self.master], [], [], max(0, end - time.monotonic()))
            if not ready:
                break
            try:
                self.output += os.read(self.master, 65536)
            except OSError:
                break

    def send(self, value):
        os.write(self.master, value)
        self.read()

    def wait_for(self, value, timeout=5):
        end = time.monotonic() + timeout
        while value not in self.output and time.monotonic() < end:
            self.read(0.1)
        assert value in self.output, (value, self.output[-4000:])

    def finish(self, code=0):
        end = time.monotonic() + 5
        while self.process.poll() is None and time.monotonic() < end:
            self.read(0.1)
        assert self.process.poll() == code, (self.process.poll(), self.output[-4000:])
        self.read(0.1)
        after = termios.tcgetattr(self.slave)
        mask = termios.ECHO | termios.ICANON
        assert self.before[3] & mask == after[3] & mask, 'terminal modes were not restored'
        assert b'\x1b[?1049l' in self.output, 'alternate screen was not restored'


def run(binary):
    with tempfile.TemporaryDirectory(prefix='lazypueue-form-pty-') as temporary:
        root = pathlib.Path(temporary)
        fixture = root / 'pueue'
        fixture.write_text(FIXTURE)
        fixture.chmod(0o700)
        config = root / 'config.toml'
        config.write_text('default_connection="local"\n[[connections]]\nid="local"\nkind="local"\n')
        env = os.environ.copy()
        env.update(HOME=temporary, XDG_CONFIG_HOME=str(root / 'config'),
                   XDG_STATE_HOME=str(root / 'state'), TERM='xterm-256color',
                   PATH=temporary + ':' + env.get('PATH', ''),
                   FIXTURE_CALLS=str(root / 'calls'))
        for name in ('PUEUE_CONFIG_PATH', 'LAZYPUEUE_CONFIG', 'LAZYPUEUE_CONNECTION'):
            env.pop(name, None)
        prefix = [str(binary), '--config', str(config)]

        with Terminal(prefix + ['add'], env) as terminal:
            terminal.wait_for(b'New Pueue task')
            terminal.wait_for(b'Ctrl+L connections')
            command = "printf 'qjk/?:中文'\necho second"
            terminal.send(b'\x1b[200~' + command.encode() + b'\x1b[201~')
            terminal.send(b'\x13')
            terminal.wait_for(b'Review new task')
            terminal.send(b'\r')
            assert not (root / 'calls').exists(), 'Enter submitted the task review'
            terminal.send(b'\x1b')
            terminal.resize(80, 24)
            terminal.read()
            terminal.send(b'\x13')
            terminal.send(b'\x13')
            terminal.finish()
        calls = [json.loads(line) for line in (root / 'calls').read_text().splitlines()]
        assert len(calls) == 1 and calls[0][-1] == command, calls
        state = json.loads((root / 'state/lazypueue/state.json').read_text())
        assert state['last_used']['local']['group'] == 'default', state
        print('PASS task form: multiline paste, literal shortcuts, explicit review/submit, Back, resize, one add, success-only defaults, terminal restore')

        with Terminal(prefix + ['connections', 'add'], env) as terminal:
            terminal.wait_for(b'Connection configuration')
            terminal.send(b'new\tqjk/?:name')
            terminal.send(b'\x14')
            terminal.wait_for(b'Connected')
            assert 'new' not in config.read_text(), 'connection Test saved its draft'
            terminal.send(b'\x13')
            terminal.wait_for(b'Review connection')
            terminal.send(b'\x1b')
            terminal.send(b'\x13')
            terminal.send(b'\x13')
            terminal.finish()
        saved = config.read_text()
        assert 'qjk/?:name' in saved and 'new' in saved, saved
        print('PASS connection form: literal shortcuts, asynchronous Test without save, review/back/save, terminal restore')

        original = config.read_bytes()
        with Terminal(prefix + ['connections', 'add'], env) as terminal:
            terminal.wait_for(b'Connection configuration')
            terminal.send(b'discard-me')
            terminal.send(b'\x03')
            terminal.finish(130)
        assert config.read_bytes() == original, 'cancel changed configuration'
        print('PASS cancellation: no save, exit 130, terminal restore')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=pathlib.Path, default=pathlib.Path('bin/lazypueue'))
    args = parser.parse_args()
    run(args.binary.resolve())
