#!/usr/bin/env python3
"""Seed fresh-machine failures without changing the developer's checkout."""

import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


class AgentEnvironmentTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        for name in ('.agents', '.claude', '.codex', '.muse', '.cursor', '.opencode', 'scripts'):
            shutil.copytree(ROOT / name, self.root / name, symlinks=True,
                            ignore=shutil.ignore_patterns('__pycache__'))
        shutil.copyfile(ROOT / 'AGENTS.md', self.root / 'AGENTS.md')

    def check(self):
        return subprocess.run([sys.executable, str(self.root / 'scripts/check-agent-env.py')],
                              text=True, capture_output=True, cwd=self.root)

    def test_complete_copy_passes_without_writing_files(self):
        before = sorted(p.relative_to(self.root) for p in self.root.rglob('*'))
        result = self.check()
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertIn('NOT_CHECKED', result.stdout)
        self.assertEqual(before, sorted(p.relative_to(self.root) for p in self.root.rglob('*')))

    def test_missing_hook_target_fails(self):
        (self.root / '.claude/hooks/goal-refresh.sh').unlink()
        result = self.check()
        self.assertNotEqual(0, result.returncode)
        self.assertIn('goal-refresh.sh', result.stdout)

    def test_broken_skill_link_fails(self):
        (self.root / '.codex/skills/fleet-go').unlink()
        result = self.check()
        self.assertNotEqual(0, result.returncode)
        self.assertIn('fleet-go', result.stdout)

    def test_malformed_config_fails(self):
        (self.root / '.muse/hooks.json').write_text('{broken')
        result = self.check()
        self.assertNotEqual(0, result.returncode)
        self.assertIn('.muse/hooks.json', result.stdout)

    def test_role_drift_fails(self):
        with (self.root / '.codex/agents/develop-fleet.toml').open('a') as stream:
            stream.write('\n# drift\n')
        result = self.check()
        self.assertNotEqual(0, result.returncode)
        self.assertIn('role bindings', result.stdout)


if __name__ == '__main__':
    unittest.main()
