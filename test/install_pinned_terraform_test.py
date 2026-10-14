"""Host selection and fail-closed checksum proof without provider/API calls."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / '.github/scripts/install-pinned-terraform.sh'

class InstallerTest(unittest.TestCase):
    def run_script(self, host, corrupt=False, selection_only=False):
        with tempfile.TemporaryDirectory() as temp:
            d = Path(temp)
            (d / 'uname').write_text('#!/bin/sh\ncase "$1" in -s) echo ' + host[0] + ';; -m) echo ' + host[1] + ';; esac\n')
            (d / 'uname').chmod(0o755)
            if corrupt:
                (d / 'curl').write_text('#!/bin/sh\nfor last; do :; done\nprintf corrupt > "$last"\n')
                (d / 'curl').chmod(0o755)
            env = {**os.environ, 'PATH': str(d) + os.pathsep + os.environ['PATH'], 'GITHUB_PATH': str(d / 'paths'), 'RUNNER_TEMP': temp}
            if selection_only:
                prefix = SCRIPT.read_text().split(': "${GITHUB_PATH', 1)[0]
                cmd = ['bash', '-c', prefix + '\nprintf "%s %s\\n" "$TF_SHA" "$TF_PLATFORM"']
            else:
                cmd = ['bash', str(SCRIPT)]
            out = subprocess.run(cmd, env=env, text=True, capture_output=True, timeout=20)
            paths = (d / 'paths').read_text() if (d / 'paths').exists() else ''
            return out, paths

    def test_supported_hosts_match_published_pins(self):
        manifest = (ROOT / 'test/fixtures/terraform-1.13.4-SHA256SUMS').read_text()
        for host, platform in [(('Linux', 'x86_64'), 'linux_amd64'), (('Linux', 'aarch64'), 'linux_arm64'), (('Linux', 'arm64'), 'linux_arm64'), (('Darwin', 'x86_64'), 'darwin_amd64'), (('Darwin', 'arm64'), 'darwin_arm64')]:
            with self.subTest(host=host):
                out, _ = self.run_script(host, selection_only=True)
                self.assertEqual(out.returncode, 0, out.stderr)
                sha, selected = out.stdout.strip().split()
                self.assertEqual(selected, platform)
                self.assertIn(sha + '  terraform_1.13.4_' + platform + '.zip', manifest)

    def test_unknown_host_refused_before_install(self):
        out, paths = self.run_script(('Linux', 'mips64'))
        self.assertNotEqual(out.returncode, 0)
        self.assertIn('Unsupported Terraform host', out.stderr)
        self.assertEqual(paths, '')

    def test_corrupt_archive_refused_without_exporting_binary(self):
        out, paths = self.run_script(('Darwin', 'arm64'), corrupt=True)
        self.assertNotEqual(out.returncode, 0)
        self.assertEqual(paths, '')
        self.assertIn('FAILED', out.stdout + out.stderr)

if __name__ == '__main__':
    unittest.main()
