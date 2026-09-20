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
import base64
import codecs
import os
import pathlib
import json
import re
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

    def click_text(self, label):
        self.expect(label)
        for y in range(len(self.screen.display)-1, -1, -1):
            row = self.screen.display[y]
            if label in row:
                x = row.index(label) + max(1, len(label)//2)
                self.send(f'\x1b[<0;{x+1};{y+1}M'.encode())
                self.send(f'\x1b[<0;{x+1};{y+1}m'.encode())
                return
        raise AssertionError(('mouse target missing', label, self.text()))

    def wheel(self, x, y, up=True):
        self.send(f'\x1b[<{64 if up else 65};{x+1};{y+1}M'.encode())

    def text_style(self, word):
        self.expect(word)
        for y, row in enumerate(self.screen.display):
            if word in row:
                return self.screen.buffer[y][row.index(word)]
        raise AssertionError(('styled text missing', word, self.text()))


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
                   FIXTURE_CALLS=str(root / 'calls'), FIXTURE_READS=str(root / 'reads'))
        for name in ('PUEUE_CONFIG_PATH', 'LAZYPUEUE_CONFIG', 'LAZYPUEUE_CONNECTION', 'NO_COLOR'):
            env.pop(name, None)
        with DashboardTerminal([str(binary), '--config', str(config)], env) as terminal:
            terminal.expect('2/3 connections fresh')
            terminal.expect('printf')
            terminal.expect('50.0%')
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

            # The details pane and expanded reader share the live log session.
            reads_before=[json.loads(line) for line in (root/'reads').read_text().splitlines()]
            following_before=sum('follow' in item['args'] for item in reads_before)
            terminal.send(b'\r')
            terminal.expect('live')
            terminal.send(b'i')
            terminal.expect('Task details · snapshot at opening')
            terminal.expect('Progress: 50%')
            terminal.send(b'\x1b')
            terminal.send(b'?')
            terminal.expect('LOGS / MONITOR')
            terminal.expect('Copy loaded plain log')
            terminal.send(b'\x1b')
            terminal.send(b'/qjk/?:\r')
            terminal.send(b'\x1b')
            reads_after=[json.loads(line) for line in (root/'reads').read_text().splitlines()]
            assert sum('follow' in item['args'] for item in reads_after)==following_before, 'zoom restarted the live source'
            terminal.send(b'3')
            terminal.send(b'\x1b[5~G')
            terminal.send(b'0')
            terminal.expect('* 0 Scope')
            terminal.send(b'1')

            # Mouse form controls use the same field/review actions as keys.
            terminal.click_text('[Add n]')
            terminal.expect('New Pueue task')
            terminal.click_text('Label (optional)')
            terminal.send(b'qjk/?:mouse-label')
            terminal.click_text('[Cancel]')

            # Add separate hosts to Monitor; cross-host task mutation selection
            # remains separate from this watchlist.
            terminal.send(b' W')
            terminal.expect('poll 10s')
            terminal.expect('fixture log')
            terminal.send(b'a')
            terminal.send(b'jW')
            terminal.expect('poll 10s')
            terminal.expect('fixture log')
            terminal.send(b'L')
            terminal.expect('Log collection mode / frequency')
            terminal.send(b'\x1b[B\x1b[B\r')
            terminal.expect('manual')
            terminal.send(b'\r')
            terminal.expect('manual')
            terminal.send(b'\x1b')
            terminal.send(b' L')
            terminal.expect('Log collection mode / frequency')
            terminal.send(b'\x1b')
            terminal.send(b' ')
            terminal.send(b'1')

            terminal.send(b't\x1b[Be')
            terminal.expect('Connection configuration')
            terminal.click_text('[Test]')
            terminal.expect('Connected')
            terminal.click_text('[Cancel]')
            terminal.send(b'\x1b')

            # Failed-task report copying includes metadata and full loaded
            # tail, while edit/restart reviews default to No.
            terminal.send(b'/echo failed\r')
            terminal.expect('ERROR: fixture failure')
            assert terminal.text_style('SOURCE_COLOR').fg == 'magenta', 'safe source color was lost'
            assert terminal.text_style('after').fg != 'magenta', 'source color bled beyond reset'
            terminal.expect('SOURCE_HIDDEN')
            assert b'\x1b[8m' not in terminal.output and b'\x1b[0;8m' not in terminal.output, 'source conceal style survived'
            assert b'\x1b]52;c;Zml4dHVyZS1hbHBoYQ==' not in terminal.output, 'source OSC clipboard control escaped'
            terminal.send(b'Y')
            terminal.read(0.4)
            reports=re.findall(rb'\x1b\]52;[^;]*;([A-Za-z0-9+/=]+)',terminal.output)
            assert reports and b'Exit code: 2' in base64.b64decode(reports[-1]), 'failure report metadata missing'
            assert b'\x1b' not in base64.b64decode(reports[-1]), 'copied report retained source ANSI'
            terminal.send(b'e')
            terminal.expect('Edit task #9')
            terminal.click_text('[Review]')
            terminal.expect('Review task correction')
            terminal.expect('create a new task')
            terminal.send(b'\r')
            terminal.expect('Edit task #9')
            terminal.click_text('[Cancel]')
            terminal.send(b':Restart in place\r')
            terminal.expect('permanently overwrite')
            terminal.send(b'\r')
            assert not (root/'calls').exists(), 'default No invoked a mutation'
            terminal.send(b':Restart in place\r')
            terminal.expect('permanently overwrite')
            terminal.click_text('[No]')
            assert not (root/'calls').exists(), 'mouse No invoked a mutation'
            terminal.send(b':Restart in place\r')
            terminal.expect('permanently overwrite')
            terminal.send(b'y')
            deadline=time.monotonic()+5
            while not (root/'calls').exists() and time.monotonic()<deadline:
                terminal.read(0.1)
            calls=[json.loads(line) for line in (root/'calls').read_text().splitlines()]
            assert len(calls)==1 and 'restart' in calls[0] and '--in-place' in calls[0],calls
            terminal.send(b'\x1b')
            terminal.send(b'm')
            terminal.click_text('[Add n]')
            assert 'New Pueue task' not in terminal.text(), 'mouse-disabled click opened a form'
            terminal.send(b'm')
            terminal.send(b'2jk1')
            for width, height in ((80, 24), (38, 12), (12, 4), (120, 36)):
                terminal.resize(width, height)
                terminal.read(0.3)
                terminal.expect('Resize' if width == 12 else 'lazypueue')
                terminal.capture(screen_dir / f'lazypueue-pty-{width}.txt')
            terminal.send(b'q')
            terminal.finish()
        state=json.loads((root/'state/lazypueue/state.json').read_text())
        assert len(state.get('watches',[]))==2, state
        print('PASS dashboard: shared live log/progress, metadata/help, safe source ANSI, Monitor modes/persistence, mouse forms/Test, paging/direct panes, plain report copy, edit cancellation, in-place y/N, four sizes, one reviewed mutation, terminal restore')
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
