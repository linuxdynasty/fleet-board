#!/usr/bin/env python3
"""Read-only check of committed development-agent setup; no live harness claims."""

import json
import os
import re
import shutil
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CONFIGS = ('.claude/settings.json', '.codex/hooks.json', '.muse/hooks.json', '.cursor/hooks.json')
REFERENCE = re.compile(r'(?:\.claude/hooks|\.codex/hooks|\.muse/hooks|scripts)/[A-Za-z0-9_./-]+\.(?:sh|py)')


def main():
    failures = []

    def require(ok, name):
        print(('OK   ' if ok else 'FAIL ') + name)
        if not ok:
            failures.append(name)

    for tool in ('git', 'bash', 'python3'):
        require(shutil.which(tool) is not None, 'required tool: ' + tool)
    for tool in ('gh', 'rg', 'make'):
        if not shutil.which(tool):
            print('WARN tool needed for issue/PR or gate work: ' + tool)
    for name in ('AGENTS.md', '.agents/roles.json', 'scripts/agent-roles.py',
                 'scripts/goal-role-router.py'):
        require((ROOT / name).is_file(), name)
    skills = sorted((ROOT / '.agents/skills').glob('*/SKILL.md'))
    require(bool(skills), 'project skill catalog')
    for skill in skills:
        for harness in ('.claude', '.codex'):
            link = ROOT / harness / 'skills' / skill.parent.name
            require(link.is_symlink() and link.resolve() == skill.parent.resolve()
                    and (link / 'SKILL.md').is_file(), str(link.relative_to(ROOT)))
    for name in CONFIGS:
        try:
            hooks = json.loads((ROOT / name).read_text())['hooks']
            require(isinstance(hooks, dict) and bool(hooks), name + ' hook map')
            for event, rows in hooks.items():
                for row in rows:
                    commands = [row] if name == '.cursor/hooks.json' else row['hooks']
                    for entry in commands:
                        refs = REFERENCE.findall(entry['command'])
                        require(bool(refs), name + ' ' + event + ' repo command')
                        for ref in refs:
                            target = ROOT / ref
                            require(target.is_file() and (not ref.endswith('.sh') or os.access(target, os.X_OK)),
                                    name + ' target ' + ref)
        except (OSError, ValueError, KeyError, TypeError, AttributeError) as error:
            require(False, name + ': ' + str(error))
    renderer = ROOT / 'scripts/agent-roles.py'
    if renderer.is_file():
        result = subprocess.run(['python3', str(renderer), 'check'], cwd=ROOT,
                                env={**os.environ, 'FLEET_AGENT_ROLES_REPO_ROOT': str(ROOT)},
                                capture_output=True, text=True)
        require(result.returncode == 0, 'role bindings match .agents/roles.json')
        if result.returncode:
            print(result.stdout + result.stderr)
    print('Manual per-machine steps: trust the workspace and restart the harness; authenticate gh; '
          'opt in to git hooks with make hooks.')
    print('NOT_CHECKED: live hook activation, skill loading, native role/model enforcement, '
          'scoped AGENTS.md loading, gh authentication and git hook activation. '
          'File discovery alone proves none of these.')
    print('check-agent-env: ' + ('FAIL' if failures else 'PASS (static setup only)'))
    return 1 if failures else 0


if __name__ == '__main__':
    raise SystemExit(main())
