#!/usr/bin/env python3
"""Create only deterministic fixture files, then commit their clean baseline."""
import os, pathlib, subprocess
root=pathlib.Path(os.environ['WORKOS_V2_DIR'])/'project'
skill=root/'.agents/skills/project-check/SKILL.md'
skill.parent.mkdir(parents=True,exist_ok=True)
skill.write_text('---\nname: project-check\ndescription: Check the fixture project using its existing files.\n---\nPROJECT_SKILL_PRIVATE_BODY\nInspect the project without changing its parent worktree.\n')
for args in [['init','-q'],['add','.'],['-c','user.name=WorkOS Fixture','-c','user.email=fixture@invalid','commit','-qm','Fixture baseline']]:
 subprocess.run(['git','-C',str(root),*args],check=True)
