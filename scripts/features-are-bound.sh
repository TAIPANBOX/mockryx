#!/usr/bin/env bash
# Holds every scenario in features/*.feature to a test, both ways.
#
# Each Scenario carries exactly one @bound:<TestName> tag on the line above it,
# and that name must be a Test function in a *_test.go file of this repository.
# A scenario with no tag is a behaviour nothing proves. A tag that names no
# function is a binding that points at nothing, which reads as proved and is
# not. A tag with no scenario below it is an orphan. Zero scenarios is not a
# pass: it means the subject was not found, so the script says it measured
# nothing and fails.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 1

python3 - <<'PY'
import glob, os, re, sys

files = sorted(glob.glob("features/*.feature"))
if not files:
    print("features-are-bound: no features/*.feature found, so this measured nothing")
    sys.exit(1)

tests = set()
for root, dirs, names in os.walk("."):
    dirs[:] = [d for d in dirs if d != ".git"]
    for name in names:
        if name.endswith("_test.go"):
            with open(os.path.join(root, name)) as f:
                tests.update(re.findall(r"^func (Test\w+)\(", f.read(), re.M))

problems = []
scenarios = 0
bound = 0
for path in files:
    pending, pending_line = None, 0
    with open(path) as f:
        for n, raw in enumerate(f, 1):
            line = raw.strip()
            if line.startswith("@bound:"):
                if pending:
                    problems.append(f"{path}:{pending_line}: @bound tag with no scenario below it")
                pending, pending_line = line[len("@bound:"):].strip(), n
            elif line.startswith("Scenario:") or line.startswith("Scenario Outline:"):
                scenarios += 1
                if not pending:
                    problems.append(f"{path}:{n}: scenario has no @bound tag, so no test proves it")
                elif pending not in tests:
                    problems.append(f"{path}:{pending_line}: @bound:{pending} points at nothing: no func {pending} in any *_test.go")
                else:
                    bound += 1
                pending = None
    if pending:
        problems.append(f"{path}:{pending_line}: @bound tag with no scenario below it")

if scenarios == 0:
    print("features-are-bound: no Scenario found in features/*.feature, so this measured nothing")
    sys.exit(1)
if problems:
    for p in problems:
        print(p)
    print(f"features-are-bound: {len(problems)} problem(s) across {scenarios} scenario(s)")
    sys.exit(1)
print(f"OK: {scenarios} scenario(s) in {len(files)} feature file(s), every one bound to a test that exists ({bound} bindings)")
PY
