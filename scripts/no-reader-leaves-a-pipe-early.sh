#!/usr/bin/env bash
#
# CLAUDE.md invariant 17. A check says what the text holds, not which of a writer and its reader
# finished first: no pipe in live shell code ends in a grep that leaves before its input does.
#
# WHAT THIS IS ABOUT
#
# `printf '%s\n' "$text" | grep -q x` under `set -o pipefail` reports the writer as well as the reader.
# grep -q leaves at its first match. A writer that still has text to write takes SIGPIPE, the pipeline
# returns 141, and a line that WAS there reads as a line that was not. Text of one write cannot fail,
# since the writer is gone before the reader starts; text over one buffer can, and whether it does
# depends on which process the machine ran first. So a gate on a healthy tree goes red on a busy
# runner, with a sentence that accuses the tree. The negative form is worse: `! ... | grep -q bad`
# reads as clean when the 141 turns it around, so a refused line passes.
#
# @measured forge, 4 cores, bash 5.2, GNU grep 3.11, 2026-11-06: scripts/costcrew-ceiling-is-set.sh in
# stack-k8s at bbed808 failed 1 of 300 runs idle and 151 of 300 beside four busy loops, saying a
# launcher "does not parse --costcrew-ceiling" when it did; the same search through a here-string
# failed 0 of 500.
#
# WHAT THIS CHECKS
#
#   A. THE CONTROL. A writer of 2.7 MB, far more than a pipe holds, is run into grep -q five times
#      under pipefail and must fail every time, and must pass five times with pipefail off, through
#      a here-string, and into grep -c. So the fault is known to be real on this machine, and so are
#      the two remedies this file sends a reader to.
#   B. THE READER. Lines of shell with a known answer are run through the masking and the match below
#      before any file is, because a reader that has stopped matching reports exactly what a clean
#      tree reports.
#   C. THE TREE. Every tracked *.sh, .githooks/*, file with a bash shebang, and `run:` step in
#      .github/workflows is masked down to live code and read. No pipe may end in grep with -q,
#      --quiet, --silent, -m, --max-count, -l, -L, --files-with-matches or --files-without-match.
#      (-l leaves early on a Mac's grep and not on GNU's, measured the same day; -L is its mirror.) The rule has
#      no exception by file: a library is sourced by a script with pipefail on, and pipefail is not
#      read from the text.
#
# WHAT A READER DOES INSTEAD
#
#   grep -q x <<<"$text"             the ordinary case, and `<<<"$(command)"` for a command
#   command | grep -c x >/dev/null   when the writer's status has to count, or the text is a key:
#                                    -c reads to the end, and a here-string is a file on bash before 5.1
#
# WHAT IT DOES NOT READ
#
# Text in quotes, comments and here-document bodies. So a command handed to another shell, `sh -c "..."`
# or a node over ssh, is not read, and pipefail is off there. `| head` is not read either: the ones
# here take a few lines from a writer of a few lines, which is one write, and a long list of harmless
# hits would teach a reader to look away. A `head` on a long writer under `set -e` is the same fault.
#
# AND IT REFUSES TO REPORT OK ON NOTHING
#
# No shell file, no pipe read, no workflow step, or a file the masking lost its place in: each fails.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
python3 - <<'PY'
import os
import re
import subprocess
import sys

problems = []


def fail(msg):
    problems.append(msg)
    print("FAIL: " + msg)


FILL = "_"


def mask(text):
    """The text with everything that is not live shell code overwritten, same length, newlines kept.

    Comments, quoted text and here-document bodies go; a $(...) inside double quotes stays, because
    that is code. Returns (masked, None), or (masked, why) when the text ends inside a quote or a
    substitution, which means this reader lost its place and must not be believed about that file.
    """
    out = list(text)
    n = len(text)

    def blank(a, b):
        for k in range(a, min(b, n)):
            if out[k] != "\n":
                out[k] = FILL

    def arithmetic_end(start):
        depth, k = 0, start
        while k < n:
            if text[k] == "(":
                depth += 1
            elif text[k] == ")":
                depth -= 1
                if depth == 0:
                    return k + 1
            k += 1
        return n

    frames = [{"kind": "code", "sub": False, "depth": 0, "case": 0}]
    waiting = []  # here-documents named on this line, as (delimiter, strip tabs)
    i = 0
    while i < n:
        f = frames[-1]
        c = text[i]
        two = text[i:i + 2]
        if f["kind"] == "code":
            if c == "\n" and waiting:
                i += 1
                for delim, strip in waiting:
                    while i < n:
                        end = text.find("\n", i)
                        end = n if end < 0 else end
                        line = text[i:end]
                        blank(i, end)
                        i = end + 1
                        if (line.lstrip("\t") if strip else line) == delim:
                            break
                waiting = []
                continue
            if c == "\\":
                if text[i + 1:i + 2] == "\n":
                    out[i] = out[i + 1] = " "
                else:
                    blank(i, i + 2)
                i += 2
            elif c == "#" and (i == 0 or text[i - 1] in " \t\n;&|("):
                end = text.find("\n", i)
                end = n if end < 0 else end
                blank(i, end)
                i = end
            elif two == "$'":
                k = i + 2
                while k < n and text[k] != "'":
                    k += 2 if text[k] == "\\" else 1
                blank(i, k + 1)
                i = k + 1
            elif c == "'":
                end = text.find("'", i + 1)
                if end < 0:
                    return "".join(out), "a single quote is never closed"
                blank(i, end + 1)
                i = end + 1
            elif c == '"':
                blank(i, i + 1)
                frames.append({"kind": "dq"})
                i += 1
            elif text[i:i + 3] == "$((":
                end = arithmetic_end(i + 1)
                blank(i, end)
                i = end
            elif two == "$(":
                frames.append({"kind": "code", "sub": True, "depth": 0, "case": 0})
                i += 2
            elif two == "${":
                blank(i, i + 2)
                frames.append({"kind": "param", "in_dq": False})
                i += 2
            elif c == "(":
                f["depth"] += 1
                i += 1
            elif c == ")":
                if f["depth"] > 0:
                    f["depth"] -= 1
                elif f["sub"] and f["case"] == 0:
                    frames.pop()
                i += 1
            elif text[i:i + 3] == "<<<":
                i += 3
            elif two == "<<":
                m = HEREDOC.match(text, i)
                if m:
                    delim = m.group(2) or m.group(3) or m.group(4)
                    waiting.append((delim, m.group(1) == "-"))
                    blank(i, m.end())
                    i = m.end()
                else:
                    i += 2
            elif c.isalpha() and (i == 0 or not (text[i - 1].isalnum() or text[i - 1] in "_-./$")):
                m = WORD.match(text, i)
                word = m.group(0)
                if word == "case":
                    f["case"] += 1
                elif word == "esac":
                    f["case"] = max(0, f["case"] - 1)
                i = m.end()
            else:
                i += 1
        elif f["kind"] == "dq":
            if c == "\\":
                blank(i, i + 2)
                i += 2
            elif c == '"':
                blank(i, i + 1)
                frames.pop()
                i += 1
            elif text[i:i + 3] == "$((":
                end = arithmetic_end(i + 1)
                blank(i, end)
                i = end
            elif two == "$(":
                frames.append({"kind": "code", "sub": True, "depth": 0, "case": 0})
                i += 2
            elif two == "${":
                blank(i, i + 2)
                frames.append({"kind": "param", "in_dq": True})
                i += 2
            else:
                blank(i, i + 1)
                i += 1
        else:  # inside ${...}
            if c == "\\":
                blank(i, i + 2)
                i += 2
            elif c == "}":
                blank(i, i + 1)
                frames.pop()
                i += 1
            elif c == "'" and not f["in_dq"]:
                end = text.find("'", i + 1)
                if end < 0:
                    return "".join(out), "a single quote is never closed"
                blank(i, end + 1)
                i = end + 1
            elif c == '"':
                blank(i, i + 1)
                frames.append({"kind": "dq"})
                i += 1
            elif text[i:i + 3] == "$((":
                end = arithmetic_end(i + 1)
                blank(i, end)
                i = end
            elif two == "$(":
                frames.append({"kind": "code", "sub": True, "depth": 0, "case": 0})
                i += 2
            elif two == "${":
                blank(i, i + 2)
                frames.append({"kind": "param", "in_dq": f["in_dq"]})
                i += 2
            else:
                blank(i, i + 1)
                i += 1
    if len(frames) != 1:
        return "".join(out), "it ends inside a quote or a substitution"
    return "".join(out), None


HEREDOC = re.compile(r"""<<(-?)[ \t]*(?:'([^'\n]+)'|"([^"\n]+)"|\\?([A-Za-z_][A-Za-z0-9_]*))""")
WORD = re.compile(r"[A-Za-z0-9_]+")
# A pipe, not `||`, into grep, past any VAR=value and `command` or `env` in front of it.
READER = re.compile(
    r"(?<!\|)\|(?!\|)&?\s*"
    r"(?:[A-Za-z_][A-Za-z0-9_]*=\S*\s+)*"
    r"(?:(?:command|env)\s+)*"
    r"(?:[\w./-]*/)?"
    r"(?:grep|egrep|fgrep)(?![\w-])"
    r"(?P<args>[^|;&()<>\n]*)"
)
PIPE = re.compile(r"(?<!\|)\|(?!\|)")
LONG = ("quiet", "silent", "max-count", "files-with-matches", "files-without-match")


def leaves_early(args):
    """The option that makes this grep leave before its input ends, or None."""
    for word in args.split():
        if word == "--":
            return None
        if word.startswith("--"):
            if word[2:].split("=")[0] in LONG:
                return word
        elif word.startswith("-") and len(word) > 1:
            for ch in word[1:]:
                if ch in "qmlL":
                    return word
                if ch in "efABCdD":  # the rest of the word is this option's value
                    break
    return None


def readers(masked):
    """(offset of the pipe, the option) for every pipe into a grep that leaves early."""
    found = []
    for m in READER.finditer(masked):
        flag = leaves_early(m.group("args"))
        if flag:
            found.append((m.start(), flag))
    return found


# ---- A. the control -------------------------------------------------------------------------------
WRITER = "seq 1 400000"


def status(script):
    return subprocess.run(["bash", "-c", script], stdout=subprocess.DEVNULL,
                          stderr=subprocess.DEVNULL, timeout=120).returncode


early = [status("set -o pipefail; " + WRITER + " | grep -q '^1$'") for _ in range(5)]
if 0 in early:
    fail("the control: a writer far larger than a pipe, into grep -q under pipefail, returned "
         + " ".join(map(str, early)) + "; it must fail every time, or this file proves nothing about the fault it names")
for name, script in (
    ("the same pipe with pipefail off", "set +o pipefail; " + WRITER + " | grep -q '^1$'"),
    ("a here-string", "set -o pipefail; t=$(" + WRITER + "); grep -q '^1$' <<<\"$t\""),
    ("grep -c", "set -o pipefail; " + WRITER + " | grep -c '^1$' >/dev/null"),
):
    got = [status(script) for _ in range(5)]
    if any(got):
        fail("the control: " + name + " returned " + " ".join(map(str, got)) + "; it must pass every time")

# ---- B. the reader, on lines with a known answer ---------------------------------------------------
CAUGHT = (
    "printf '%s\\n' \"$x\" | grep -q y\n",
    "printf '%s\\n' \"$x\" \\\n  | grep -qE y\n",
    "printf '%s\\n' \"$x\" |\n  grep -Fq y\n",
    "if ! a | LC_ALL=C grep --quiet y; then :; fi\n",
    "v=\"$(a | grep -m1 y)\"\n",
    "v=\"${w:-$(a | grep -m 1 y)}\"\n",
    "a | /usr/bin/grep -q y\n",
    "a | command grep -iq y\n",
)
LEFT = (
    "grep -q y <<<\"$x\"\n",
    "# printf x | grep -q y\n",
    "check \"a name\" \"a | grep -q y\"\n",
    "sh -c 'a | grep -q y'\n",
    "cat <<'EOF'\na | grep -q y\nEOF\n",
    "a | grep -c y >/dev/null\n",
    "a | grep y\n",
    "a || grep -q y file\n",
    "a | grep -- -q\n",
    "a | grep -A2 y\n",
    "a | grep 'y -q'\n",
)
for line in CAUGHT:
    if not readers(mask(line)[0]):
        fail("the reader missed a pipe into an early reader: " + repr(line) + ", so a clean report from it means nothing")
for line in LEFT:
    if readers(mask(line)[0]):
        fail("the reader flagged what is not a pipe into an early reader: " + repr(line))


# ---- C. the tree -----------------------------------------------------------------------------------
def run_steps(text):
    """(line the step's script starts on, the script) for every run: in a workflow."""
    lines = text.split("\n")
    i = 0
    while i < len(lines):
        m = re.match(r"^(\s*)(- )?run:\s*(.*)$", lines[i])
        if not m:
            i += 1
            continue
        indent = len(m.group(1)) + (2 if m.group(2) else 0)
        if m.group(3)[:1] in ("|", ">"):
            j = i + 1
            while j < len(lines) and (not lines[j].strip() or len(lines[j]) - len(lines[j].lstrip()) > indent):
                j += 1
            yield i + 1, "\n".join(lines[i + 1:j])
            i = j
        else:
            yield i, m.group(3)
            i += 1


listed = subprocess.run(["git", "ls-files", "-z"], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
if listed.returncode != 0:
    fail("git ls-files failed, so this gate measured NOTHING: " + listed.stderr.decode("utf8", "replace").strip())
files = steps = pipes = 0
for path in listed.stdout.decode("utf8", "replace").split("\0"):
    if not path or not os.path.isfile(path):
        continue
    with open(path, "rb") as handle:
        raw = handle.read()
    if b"\0" in raw[:4096]:
        continue
    text = raw.decode("utf8", "replace")
    first = text.split("\n", 1)[0]
    if path.endswith(".sh") or path.startswith(".githooks/") or (first.startswith("#!") and "bash" in first):
        files += 1
        scripts = [(0, text)]
    elif path.startswith(".github/workflows/") and path.endswith((".yml", ".yaml")):
        scripts = list(run_steps(text))
        steps += len(scripts)
    else:
        continue
    for offset, script in scripts:
        masked, why = mask(script)
        if why:
            fail(f"{path}: {why}, so this reader lost its place and measured NOTHING past that point")
            continue
        pipes += len(PIPE.findall(masked))
        for at, flag in readers(masked):
            line = offset + script.count("\n", 0, at) + 1
            fail(f"{path}:{line} pipes into `grep {flag}`, which leaves before its input ends: a writer still writing "
                 "takes SIGPIPE, and under pipefail the pipeline fails with the line there. "
                 "Use `grep ... <<<\"$text\"`, or `grep -c ... >/dev/null` where the writer's status counts or the text is a key")
if files == 0:
    fail("no tracked shell file was found, so this gate measured NOTHING")
if steps == 0:
    fail("no `run:` step was found in .github/workflows, so this gate measured NOTHING there")
if pipes == 0:
    fail("no pipe was read in any shell file, so this gate measured NOTHING")

if problems:
    print()
    print(f"{len(problems)} problem(s). A check has to say what the text holds, not which of two processes")
    print("finished first. See CLAUDE.md invariant 17.")
    sys.exit(1)

print(f"OK: {files} shell files and {steps} workflow steps, {pipes} pipes read, none ending in a grep that leaves early.")
print(f"    The control broke the pipe {len(early)} of {len(early)} times, and the here-string and grep -c held.")
PY
