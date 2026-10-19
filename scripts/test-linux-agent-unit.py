#!/usr/bin/env python3
"""Check the shipped Linux user service against its safety contract."""

import configparser
import pathlib
import platform
import shutil
import subprocess
import unittest


ROOT = pathlib.Path(__file__).resolve().parents[1]
UNIT = ROOT / "packaging/systemd/fleet-agent.service"
DEFAULTS = ROOT / "docs/specs/defaults.md"


class AgentUnitTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        parser = configparser.ConfigParser(interpolation=None, strict=True)
        parser.optionxform = str
        with UNIT.open(encoding="utf-8") as unit:
            parser.read_file(unit)
        cls.unit = parser

    def test_user_service_contract(self):
        expected = {
            "Unit": {"StartLimitIntervalSec": "60s", "StartLimitBurst": "5"},
            "Service": {
                "Type": "simple",
                "ExecStart": '"%h/.local/bin/fleet-agent" run',
                "WorkingDirectory": "%h",
                "Restart": "on-failure",
                "RestartSec": "5s",
                "RestartPreventExitStatus": "2",
                "KillSignal": "SIGTERM",
                "TimeoutStopSec": "30s",
                "KillMode": "process",
                "StandardOutput": "journal",
                "StandardError": "journal",
            },
            "Install": {"WantedBy": "default.target"},
        }
        self.assertEqual(set(self.unit.sections()), set(expected))
        for section, values in expected.items():
            with self.subTest(section=section):
                for key, value in values.items():
                    self.assertEqual(self.unit[section].get(key), value, key)
        forbidden = {
            "User", "Group", "Environment", "EnvironmentFile", "ExecStartPre",
            "ExecStop", "PrivateHome", "PrivateTmp", "RootDirectory", "NetworkNamespacePath",
        }
        self.assertFalse(forbidden.intersection(self.unit["Service"]))

    def test_numeric_defaults_match_service(self):
        table = DEFAULTS.read_text(encoding="utf-8")
        for row in (
            "| Linux agent restart interval | 60 | s |",
            "| Linux agent restart burst | 5 | starts |",
            "| Linux agent restart delay | 5 | s |",
            "| Linux agent stop grace | 30 | s |",
        ):
            with self.subTest(row=row):
                self.assertIn(row, table)

    def test_systemd_accepts_unit(self):
        if platform.system() != "Linux":
            self.skipTest("systemd-analyze requires Linux; run in a Linux test host")
        if shutil.which("systemd-analyze") is None:
            self.skipTest("systemd-analyze is not installed")
        if not (pathlib.Path.home() / ".local/bin/fleet-agent").is_file():
            self.skipTest("install a disposable fleet-agent binary for systemd verify")
        result = subprocess.run(
            ["systemd-analyze", "--user", "verify", str(UNIT)],
            text=True,
            capture_output=True,
            check=False,
            timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
