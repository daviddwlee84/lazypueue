#!/usr/bin/env python3
"""Real-terminal dashboard + shared-form smoke tests with disposable fixtures.

go build -o bin/lazypueue .
uv run --no-project --with pyte==0.8.2 python scripts/pty_smoke.py --binary bin/lazypueue

Requires pyte 0.8.2 to inspect actual rendered screens, including incremental
terminal updates. Screen text is exported to /tmp/lazypueue-pty-120.txt (use
--screen-dir to change the artifact location). The separate form_pty_smoke.py
script needs only Python's standard library.
"""

import argparse
import codecs
import os
import pathlib
import shutil
import tempfile
import time

from form_pty_smoke import Terminal, run as run_forms

try:
    import pyte
except ImportError as error:
    raise SystemExit('Dashboard PTY verification requires pyte. Install pyte==0.8.2, or run: '
                     'uv run --no-project --with pyte==0.8.2 python scripts/pty_smoke.py --binary bin/lazypueue') from error


class DashboardTerminal(Terminal):
    def __init__(self, command, env):
        self.screen = pyte.Screen(120, 36)
        self.stream = pyte.Stream(self.screen)
        self.decoder = codecs.getincrementaldecoder('utf-8')('replace')
        super().__init__(command, env)

    def resize(self, width, height):
        self.screen.resize(lines=height, columns=width)
        super().resize(width, height)

    def read(self, duration=0.2):
        offset = len(self.output)
        super().read(duration)
        self.stream.feed(self.decoder.decode(self.output[offset:]))

    def text(self):
        return '\n'.join(self.screen.display)

    def expect(self, value, timeout=5):
        end = time.monotonic() + timeout
        while value not in self.text() and time.monotonic() < end:
            self.read(0.1)
        assert value in self.text(), (value, self.text())

    def capture(self, path):
        path.write_text(self.text() + '\n')


def dashboard(binary, screen_dir):
    with tempfile.TemporaryDirectory(prefix='lazypueue-dashboard-pty-') as temporary:
        root = pathlib.Path(temporary)
        for name in ('pueue', 'pueue-lab', 'pueue-offline'):
            target = root / name
            shutil.copyfile(pathlib.Path(__file__).with_name('fake_pueue.py'), target)
            target.chmod(0o700)
        config = root / 'config.toml'
        config.write_text('''default_connection = "all"
[tui]
refresh_seconds = 2
background_seconds = 5
[[connections]]
id = "local"
name = "Laptop"
kind = "local"
[[connections]]
id = "lab"
name = "Lab"
kind = "local"
binary = "pueue-lab"
[[connections]]
id = "offline"
name = "Offline"
kind = "local"
binary = "pueue-offline"
''')
        env = os.environ.copy()
        env.update(HOME=temporary, XDG_CONFIG_HOME=str(root / 'config'), XDG_STATE_HOME=str(root / 'state'),
                   TERM='xterm-256color', PATH=temporary + ':' + env.get('PATH', ''),
                   FIXTURE_CALLS=str(root / 'calls'))
        for name in ('PUEUE_CONFIG_PATH', 'LAZYPUEUE_CONFIG', 'LAZYPUEUE_CONNECTION'):
            env.pop(name, None)
        with DashboardTerminal([str(binary), '--config', str(config)], env) as terminal:
            terminal.expect('2/3 connections fresh')
            terminal.expect('printf')
            terminal.capture(screen_dir / 'lazypueue-pty-120.txt')
            assert 'must-not-be-displayed' not in terminal.text(), 'captured task environment was exposed'
            terminal.send(b'j\x1b[A\x1b[Bk')
            terminal.send(b'/qjk/?:')
            terminal.expect('Filter: qjk/?:')
            terminal.send(b'\r')
            assert terminal.process.poll() is None, 'literal q exited the dashboard'
            terminal.send(b'\x1b')
            terminal.send(b':qjk/?')
            terminal.expect('No applicable actions match.')
            terminal.send(b'\x1b')

            terminal.send(b':after\r')
            terminal.expect('New Pueue task')
            terminal.send(b'\x04')
            terminal.expect('[x] 7')
            terminal.send(b'\x1b')
            terminal.send(b'\x1b')
            terminal.send(b'n')
            terminal.expect('New Pueue task')
            terminal.send(b'\x1b[200~echo qjk/?:\necho pasted\x1b[201~')
            terminal.send(b'\x1b')

            terminal.send(b'\r')
            terminal.expect('fixture log')
            terminal.send(b'/qjk/?:\r')
            terminal.send(b'\x1b')
            terminal.send(b'F')
            terminal.expect('following')
            terminal.expect('fixture live')
            terminal.send(b'\x1b[A')
            terminal.expect('scroll paused')
            terminal.send(b'F')
            terminal.send(b'\x1b')

            terminal.send(b't\x1b[Be')
            terminal.expect('Connection configuration')
            terminal.send(b'\x14')
            terminal.expect('Connected')
            terminal.send(b'\x1b')
            terminal.send(b'\x1b')
            terminal.send(b'2jk1')
            for width, height in ((80, 24), (38, 12), (12, 4), (120, 36)):
                terminal.resize(width, height)
                terminal.read(0.3)
                terminal.expect('Resize' if width == 12 else 'lazypueue')
                terminal.capture(screen_dir / f'lazypueue-pty-{width}.txt')
            terminal.send(b'q')
            terminal.finish()
        assert not (root / 'calls').exists(), 'dashboard browsing/cancellation invoked a mutation'
        print('PASS dashboard: multi-target equal IDs/offline host, navigation/filter/palette, after/new wizard cancellation, log/follow, draft Test, four sizes, no mutation, terminal restore')
        print('Screen artifacts:', screen_dir / 'lazypueue-pty-120.txt')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=pathlib.Path, default=pathlib.Path('bin/lazypueue'))
    parser.add_argument('--screen-dir', type=pathlib.Path, default=pathlib.Path('/tmp'))
    parser.add_argument('--dashboard-only', action='store_true')
    args = parser.parse_args()
    args.screen_dir.mkdir(parents=True, exist_ok=True)
    binary = args.binary.resolve()
    if not args.dashboard_only:
        run_forms(binary)
    dashboard(binary, args.screen_dir)
