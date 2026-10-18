"""Linux helper regression tests. Uses a fake rdcli; never runs an installer."""
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time
import unittest


SCRIPT = Path(__file__).with_name('netbird-upgrade.sh')


class UpgradeHelperTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.script = self.root / 'netbird-upgrade.sh'
        self.script.write_bytes(SCRIPT.read_bytes())
        self.calls = self.root / 'calls.jsonl'
        stub = self.root / 'rdcli'
        stub.write_text('''#!/usr/bin/env python3
import json, os, sys, time
args = sys.argv[1:]
device = args[args.index('-c') + 1]
with open(os.environ['CALLS_PATH'], 'a') as out:
    out.write(json.dumps({'id': device, 'password': os.environ.get('RDC_PASSWORD'), 'args': args}) + '\\n')
print('output from ' + device, flush=True)
if os.environ.get('STUB_MIXED_OUTPUT'):
    print('stderr from ' + device, file=sys.stderr, flush=True)
    print('unterminated ' + device, end='', flush=True)
if os.environ.get('STUB_DELAY'):
    time.sleep(float(os.environ['STUB_DELAY']))
sys.exit(7 if device == '222' else 0)
''')
        stub.chmod(0o700)
        self.env = dict(os.environ, RDCLI_BIN=str(stub), CALLS_PATH=str(self.calls),
                        RDC_PASSWORD='wrong-environment-password')

    def run_helper(self, *args):
        before = set(self.root.iterdir())
        result = subprocess.run(['bash', str(self.script), *args], env=self.env, stdin=subprocess.DEVNULL,
                                capture_output=True, text=True, timeout=10, cwd=self.root)
        self.assertFalse(set(self.root.iterdir()) - before - {self.calls}, 'helper created output files')
        return result

    def device_file(self, content):
        path = self.root / ('devices-%d.txt' % len(list(self.root.glob('devices-*.txt'))))
        path.write_bytes(content.encode('utf-8'))
        return str(path)

    def read_calls(self):
        return [json.loads(line) for line in self.calls.read_text().splitlines()] if self.calls.exists() else []

    def test_file_literals_crlf_bom_and_no_final_newline(self):
        passwords = ['p a$"ss#\\literal', 'second password  ']
        path = self.device_file('\ufeff# comment\r\n\r\n 111\t' + passwords[0] + '\r\n333 ' + passwords[1])
        result = self.run_helper('--check', '--file', path)
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.read_calls()
        self.assertEqual([c['id'] for c in calls], ['111', '333'])
        self.assertEqual([c['password'] for c in calls], passwords)
        for call in calls:
            self.assertEqual(call['args'][-1], 'whoami')
            self.assertIn('--relay', call['args'])
        for password in passwords:
            self.assertNotIn(password, result.stdout + result.stderr)
        self.assertIn('output from 111', result.stdout)
        self.assertIn('output from 333', result.stdout)

    def test_failure_continues_once_and_reports_original_exit(self):
        path = self.device_file('111 first\n222 second\n333 third\n')
        result = self.run_helper('--file', path)
        self.assertEqual(result.returncode, 1, result.stderr)
        self.assertEqual([c['id'] for c in self.read_calls()], ['111', '222', '333'])
        self.assertIn('2 succeeded, 1 failed', result.stdout)
        self.assertIn('222: FAILED (exit 7)', result.stdout)
        self.assertIn('Invoke-Expression', self.read_calls()[0]['args'][-1])

    def test_batch_output_is_ordered_without_extra_blank_lines(self):
        self.env['STUB_MIXED_OUTPUT'] = '1'
        path = self.device_file('111 first\n222 second\n333 third\n')
        result = self.run_helper('--check', '--file', path)
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stderr, '')
        expected = ['[rdcli] Loaded 3 device(s). Mode: check.']
        for index, device in enumerate(['111', '222', '333'], 1):
            expected.extend([
                f'[rdcli] Device {index}/3: {device}',
                f'[rdcli] Connecting to device {device} via relay (setup timeout 30s)...',
                'output from ' + device,
                'stderr from ' + device,
                'unterminated ' + device,
                f'[rdcli] {device}: ' + ('FAILED (exit 7)' if device == '222' else 'OK (exit 0)'),
            ])
        expected.append('[rdcli] Finished: 2 succeeded, 1 failed.')
        self.assertEqual(result.stdout, '\n'.join(expected) + '\n')

    def test_bad_file_prevents_all_connections(self):
        for bad_line in ['222', '222   ', 'bad-id private-password', '111 duplicate-password']:
            with self.subTest(bad_line=bad_line):
                result = self.run_helper('--file', self.device_file('111 good\n' + bad_line))
                self.assertEqual(result.returncode, 2)
                self.assertIn('line 2', result.stderr)
                self.assertNotIn('private-password', result.stderr)
                self.assertNotIn('duplicate-password', result.stderr)
                self.assertEqual(self.read_calls(), [])

    def test_empty_or_missing_file_and_conflicting_args(self):
        for args in [[], ['--file'], ['--unknown'], ['--file', str(self.root / 'missing')],
                     ['--file', self.device_file('# only comments\n')],
                     ['111', '--file', self.device_file('111 p')]]:
            with self.subTest(args=args):
                self.assertEqual(self.run_helper(*args).returncode, 2)
                self.assertEqual(self.read_calls(), [])

    def test_single_device_and_noninteractive_missing_password(self):
        result = self.run_helper('--check', 'home-pc')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.read_calls()[0]['password'], 'wrong-environment-password')
        self.assertEqual(self.read_calls()[0]['args'][-1], 'whoami')
        self.calls.unlink()
        self.env.pop('RDC_PASSWORD')
        result = self.run_helper('home-pc')
        self.assertEqual(result.returncode, 2)
        self.assertIn('device password required', result.stderr)
        self.assertEqual(self.read_calls(), [])

    def test_timeouts_are_forwarded(self):
        self.env.update(RDC_CONNECT_TIMEOUT='45s', RDC_COMMAND_TIMEOUT='0')
        result = self.run_helper('--check', '--file', self.device_file('111 password'))
        self.assertEqual(result.returncode, 0, result.stderr)
        args = self.read_calls()[0]['args']
        self.assertEqual(args[args.index('--timeout') + 1], '45s')
        self.assertEqual(args[args.index('--command-timeout') + 1], '0')

    def test_interrupt_stops_before_next_device(self):
        self.env['STUB_DELAY'] = '1'
        path = self.device_file('111 password\n333 another')
        proc = subprocess.Popen(['bash', str(self.script), '--file', path], env=self.env,
                                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            deadline = time.monotonic() + 5
            while not self.read_calls() and time.monotonic() < deadline:
                time.sleep(0.02)
            self.assertTrue(self.read_calls(), 'fake command did not start')
            proc.send_signal(signal.SIGINT)
            stdout, stderr = proc.communicate(timeout=5)
            self.assertEqual(proc.returncode, 1, stdout + stderr)
            self.assertEqual([c['id'] for c in self.read_calls()], ['111'])
            self.assertIn('Interrupted', stderr)
        finally:
            if proc.poll() is None:
                proc.kill()
                proc.communicate()


if __name__ == '__main__':
    unittest.main()
