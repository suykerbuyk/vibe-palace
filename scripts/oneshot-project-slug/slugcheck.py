#!/usr/bin/env python3
# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0
"""ONE-SHOT helper for census.sh, parity.sh and rehearsal.sh.

Task: migrate-quantum-ng-vault-history-to-qa-metabuild-system. This whole
directory (scripts/oneshot-project-slug/) is deleted with the Go one-shot code.

Standard library only. Every subcommand is READ-ONLY on tracked vault content;
the only vault writes are the embed-cache vectors that `vp search` itself
creates under the gitignored palace/.local/. Absolute vault paths are scrubbed
from every recorded value, so two measurements of different copies of the
same state compare equal.

Floors (the "cannot pass by measuring nothing" guards) default to the live
figures in the plan and can be lowered only through SLUG_FLOOR_* environment
variables, which the fixture self-test sets.
"""

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile

FROM_DEFAULT = "quantum-ng"
TO_DEFAULT = "qa-metabuild-system"

# Search parity query set (plan, C7): 18 plain queries plus 2 filtered.
#
# DEPTH (code review of R-dev, F1). The engine takes its candidate pool as
# limit*3 by raw distance BEFORE the wing/room filters, so a room-filtered
# query at -n 10 survives only a handful of rows: on the live vault, `-r
# decisions` returned 4 and `-w … -r security` returned 1, out of rooms
# holding 1,680 and 1,324 drawers. Those queries therefore run deeper, in B1
# and in A alike, so that what they compare is a real slice of the room.
N_PLAIN_B1, N_PLAIN_A = 10, 20
N_FILTERED = 60
QUERIES = [
    {"q": "nested hypervisor for the Proxmox cluster"},
    {"q": "one build at a time, the WSL2 host dies if two builds run"},
    {"q": "vp_manage_task amend replaces the whole section"},
    {"q": "a subagent claim is not evidence until someone opens the file"},
    {"q": "cluster teardown fails on a two-node PVE cluster without PVE_NODE"},
    {"q": "D1 mgmt nginx serving stale content"},
    {"q": "TFTP RRQ not sent during PXE boot"},
    {"q": "tapemanager qa/dev-johns forwardable patch archive"},
    {"q": "workspace root contaminated by a subproject"},
    {"q": "SONiC VS emulator parity baseline"},
    {"q": "EdgeCore switch fabric discovery"},
    {"q": "scaler protobuf config optional on the host"},
    {"q": "Nexus asset cache for Proxmox deploys"},
    {"q": "resume exceeds the core floor and blocks ADR-009 arming"},
    {"q": "Spectrum ASIC emulation under normal hypervisors"},
    {"q": "pveforge adoption decision, keep the shell facade"},
    {"q": "known-flake retry reported as FLAKE never PASS"},
    {"q": "redact credentials before writing to the vault"},
    {"q": "nested Proxmox belongs to pveforge", "room": "decisions"},
    {"q": "unauthenticated control plane committed secrets", "wing": "FROM", "room": "security"},
]


# Every floor actually consulted, so the verdict and the census JSON can state
# what they were measured against (a lowered floor must never be invisible).
FLOORS_USED = {}


def floor(name, default):
    v = os.environ.get("SLUG_FLOOR_" + name)
    n = int(v) if v not in (None, "") else default
    FLOORS_USED[name] = {"used": n, "default": default, "overridden": n != default}
    return n


def floors_line():
    over = sorted(k for k, v in FLOORS_USED.items() if v["overridden"])
    base = "floors: " + " ".join("%s=%d" % (k, FLOORS_USED[k]["used"]) for k in sorted(FLOORS_USED))
    if over:
        base += "  OVERRIDDEN VIA SLUG_FLOOR_*: " + ", ".join(over)
    return base


def die(msg, code=2):
    print(msg, file=sys.stderr)
    sys.exit(code)


def vp_bin():
    return os.environ.get("VP", "vp")


# A directory with no .vibe-palace.toml anywhere above it, used as the cwd for
# every vp subprocess: `vp mcp`, `vp audit vault`, `vp migrate kg-filenames`
# and `vp search` all resolve a vault from the cwd BEFORE the XDG config, so
# running them from the caller's cwd can silently measure another vault.
_NEUTRAL = None


def neutral_cwd():
    global _NEUTRAL
    if _NEUTRAL is None:
        _NEUTRAL = tempfile.mkdtemp(prefix="slug-neutral-")
    return _NEUTRAL


def resolved_vault():
    """The vault vp itself resolves, from `vp status`."""
    p = run([vp_bin(), "status"], cwd=neutral_cwd(), check=False)
    for line in (p.stdout + p.stderr).decode(errors="replace").splitlines():
        if line.startswith("vault_path = "):
            return line[len("vault_path = "):].strip()
    return ""


def pin(root):
    """Refuse unless vp resolves the vault being measured."""
    got = resolved_vault()
    try:
        same = bool(got) and os.path.samefile(got, root)
    except OSError:
        same = False
    if not same:
        die("refusing: vp resolves %r, not the vault under test %r; set XDG_CONFIG_HOME" % (got, root))
    return got


def run(cmd, cwd=None, check=True, env=None):
    p = subprocess.run(cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if check and p.returncode != 0:
        die("command failed (%d): %s\n%s" % (p.returncode, " ".join(cmd), p.stderr.decode(errors="replace")))
    return p


def git(root, *args, check=True):
    return run(["git", "-C", root] + list(args), check=check).stdout.decode()


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""):
            h.update(b)
    return h.hexdigest()


def sha256_text(s):
    return hashlib.sha256(s.encode()).hexdigest()


def drawer_id(wing, content):
    return hashlib.md5((wing + content).encode()).hexdigest()[:8]


def scrub(v, root):
    """Replace the absolute vault root (and its realpath) in every string."""
    roots = sorted({root.rstrip("/"), os.path.realpath(root).rstrip("/")}, key=len, reverse=True)

    def s(x):
        if isinstance(x, str):
            for r in roots:
                x = x.replace(r, "<vault>")
            return x
        if isinstance(x, list):
            return [s(i) for i in x]
        if isinstance(x, dict):
            return {k: s(i) for k, i in x.items()}
        return x

    return s(v)


def write_json(path, obj):
    data = json.dumps(obj, indent=1, sort_keys=True) + "\n"
    if path == "-":
        sys.stdout.write(data)
        return
    with open(path, "w") as f:
        f.write(data)


def read_json(path):
    with open(path) as f:
        return json.load(f)


# ---------------------------------------------------------------- MCP client


class MCP:
    """One `vp mcp` at a time, driven over stdio, stopped by close()."""

    def __init__(self, cwd=None):
        self.p = subprocess.Popen([vp_bin(), "mcp"], cwd=cwd or neutral_cwd(), stdin=subprocess.PIPE,
                                  stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True)
        self.n = 0
        self._rpc("initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                                 "clientInfo": {"name": "oneshot-project-slug", "version": "1"}})
        self._send({"jsonrpc": "2.0", "method": "notifications/initialized"})

    def _send(self, m):
        self.p.stdin.write(json.dumps(m) + "\n")
        self.p.stdin.flush()

    def _rpc(self, method, params):
        self.n += 1
        self._send({"jsonrpc": "2.0", "id": self.n, "method": method, "params": params})
        while True:
            line = self.p.stdout.readline()
            if not line:
                die("vp mcp closed stdout during %s" % method)
            r = json.loads(line)
            if r.get("id") == self.n:
                if "error" in r:
                    raise RuntimeError("%s: %s" % (method, r["error"]))
                return r["result"]

    def call(self, name, args):
        r = self._rpc("tools/call", {"name": name, "arguments": args})
        if r.get("isError"):
            raise RuntimeError("%s: %s" % (name, r.get("content", [{}])[0].get("text", "")))
        if "structuredContent" in r:
            return r["structuredContent"]
        text = r.get("content", [{}])[0].get("text", "")
        try:
            return json.loads(text)
        except ValueError:
            return {"text": text}

    def close(self):
        self.p.stdin.close()
        self.p.wait(timeout=60)


# ---------------------------------------------------------------- measuring

PATTERNS = {
    "project_fm": r"(?m)^project: {s}$",
    "note_path": r"(?m)^note_path: Projects/{s}/",
    "archive": r"(?m)^archive: Projects/{s}/",
    "tracked_by": r"(?m)^tracked_by: Projects/{s}/",
    "project_slug": r'"project_slug": "{s}"',
    "vault_rel_session_note": r'"vault_rel_session_note": "Projects/{s}/',
}


def tracked(root):
    out = run(["git", "-C", root, "ls-files", "-z"]).stdout.decode()
    return [p for p in out.split("\0") if p]


def in_scope(rel, frm, to):
    for s in (frm, to):
        if rel.startswith("Projects/%s/" % s) or rel.startswith("palace/%s/" % s):
            return True
    return rel.startswith("Audits/")


def tree_classes(root, files, s):
    P, R = "Projects/%s/" % s, "palace/%s/" % s
    mine = [f for f in files if f.startswith(P) or f.startswith(R)]
    c = {"tracked": len(mine)}

    def cnt(rx):
        r = re.compile(rx)
        return sum(1 for f in mine if r.fullmatch(f))

    e = re.escape(P)
    c["sessions"] = cnt(e + r"sessions/[^/]+\.md")
    c["manifests"] = cnt(e + r"transcripts/[^/]+\.manifest\.json")
    c["zst"] = cnt(e + r"transcripts/[^/]+\.jsonl\.zst")
    c["tasks_active"] = cnt(e + r"tasks/[^/]+\.md")
    c["tasks_done"] = cnt(e + r"tasks/done/[^/]+\.md")
    c["tasks_cancelled"] = cnt(e + r"tasks/cancelled/[^/]+\.md")
    c["memory"] = cnt(e + r"memory/[^/]+\.md")
    c["notes"] = cnt(e + r"notes/[^/]+\.md")
    rows = {}
    d = R + "drawers/%s/" % s
    for f in mine:
        if f.startswith(d) and f.endswith("/drawers.jsonl"):
            room = f[len(d):-len("/drawers.jsonl")]
            with open(os.path.join(root, f), encoding="utf-8", errors="replace") as fh:
                rows[room] = sum(1 for line in fh if line.strip())
    c["drawer_rows"] = rows
    kg = {}
    for f in mine:
        if f.startswith(R + "kg/"):
            kg[f[len(R):]] = sha256_file(os.path.join(root, f))
    c["kg_files"] = kg
    bak = 0
    pdir = os.path.join(root, P)
    for dp, _dn, fn in os.walk(pdir):
        bak += sum(1 for n in fn if n.endswith(".bak"))
    c["bak_on_disk"] = bak
    c["exists"] = {"projects": os.path.isdir(pdir), "palace": os.path.isdir(os.path.join(root, R))}
    return c


def literal_counts(root, files, frm, to):
    per = {}
    pat = {k: {} for k in PATTERNS}
    bf, bt = frm.encode(), to.encode()
    for rel in files:
        if not in_scope(rel, frm, to):
            continue
        with open(os.path.join(root, rel), "rb") as fh:
            b = fh.read()
        nf, nt = b.count(bf), b.count(bt)
        if nf or nt:
            per[rel] = [nf, nt]
        if rel.endswith(".zst"):
            continue
        t = b.decode("utf-8", errors="replace")
        for k, rx in PATTERNS.items():
            for s in (frm, to):
                n = len(re.findall(rx.format(s=re.escape(s)), t))
                if n:
                    pat[k][s] = pat[k].get(s, 0) + n
    return per, pat


def baseline_counts(root, slugs):
    """Audits/baseline.json, split the way W8 splits it.

    `accepted` is what the migration rewrites: the accepted[] entries, which
    name real files. `prose` is the reason strings, which the migration must
    NOT touch — they are the record of why a debt was accepted, written in the
    tense of the day it was accepted. Counting them together (as an earlier
    draft did) fails a correct run; not counting them at all would let a
    rewrite reach text it was never supposed to reach, unseen.
    """
    out = {"accepted": {s: 0 for s in slugs}, "prose": {s: 0 for s in slugs}}
    p = os.path.join(root, "Audits", "baseline.json")
    try:
        with open(p, encoding="utf-8", errors="replace") as fh:
            doc = json.load(fh)
    except (OSError, ValueError):
        return out
    for dim in doc.get("dimensions", {}).values():
        if not isinstance(dim, dict):
            continue
        for entry in dim.get("accepted", []) or []:
            for s in slugs:
                if "Projects/%s/" % s in str(entry):
                    out["accepted"][s] += 1
        reason = str(dim.get("reason", ""))
        for s in slugs:
            out["prose"][s] += reason.count("Projects/%s/" % s)
    return out


def stray_group(root, s, stem_prefix):
    """Session notes of one date-writer group: numbers and session_ids."""
    d = os.path.join(root, "Projects", s, "sessions")
    out = []
    if not stem_prefix or not os.path.isdir(d):
        return out
    for n in sorted(os.listdir(d)):
        m = re.fullmatch(re.escape(stem_prefix) + r"-(\d+)\.md", n)
        if not m:
            continue
        sid = ""
        with open(os.path.join(d, n), encoding="utf-8", errors="replace") as fh:
            for line in fh:
                if line.startswith("session_id:"):
                    sid = line.split(":", 1)[1].strip()
                    break
        out.append([int(m.group(1)), sid])
    return out


def name_status(root, rev):
    out = git(root, "show", "-M", "--name-status", "--format=", rev)
    st = {}
    for line in out.splitlines():
        if line.strip():
            k = line.split("\t", 1)[0]
            k = "R" if k.startswith("R") and k != "R100" else k
            st[k] = st.get(k, 0) + 1
    return st


def wrap_iter(mcp, s):
    """vp_collect_wrap_state against a throwaway repo whose toml names s."""
    with tempfile.TemporaryDirectory(prefix="slug-wrap-") as d:
        run(["git", "-C", d, "init", "-q", "-b", "main"])
        with open(os.path.join(d, ".vibe-palace.toml"), "w") as f:
            f.write('[project]\nname = "%s"\n' % s)
        run(["git", "-C", d, "add", "-A"])
        run(["git", "-C", d, "-c", "user.email=census@localhost", "-c", "user.name=census",
             "commit", "-q", "-m", "census"])
        r = mcp.call("vp_collect_wrap_state", {"project": s, "project_path": d})
    return r.get("iter_n")


def last_heading(root, s):
    p = os.path.join(root, "Projects", s, "iterations.md")
    if not os.path.exists(p):
        return 0
    with open(p, encoding="utf-8", errors="replace") as fh:
        ns = [int(m.group(1)) for m in re.finditer(r"(?m)^## Iteration (\d+)", fh.read())]
    return max(ns) if ns else 0


def mcp_facts(root, s, mcp):
    f = {}
    try:
        st = mcp.call("vp_kg_stats", {"project": s})
        f["kg_stats"] = st
    except RuntimeError as e:
        f["kg_stats"] = {"error": str(e)}
    ents = []
    ep = os.path.join(root, "palace", s, "kg", "entities.jsonl")
    if os.path.exists(ep):
        with open(ep, encoding="utf-8", errors="replace") as fh:
            for line in fh:
                try:
                    e = json.loads(line)
                except ValueError:
                    continue
                name = e.get("name", "")
                if FROM_ARG in name or TO_ARG in name:
                    ents.append(name)
    q = {}
    for name in sorted(set(ents) | {FROM_ARG}):
        try:
            q[name] = mcp.call("vp_kg_query", {"project": s, "entity": name})
        except RuntimeError as e:
            q[name] = {"error": str(e)}
    f["kg_query"] = q
    t = mcp.call("vp_list_tasks", {"project": s, "include_done": True, "include_icebox": True})
    by = {}
    for task in t.get("tasks", []):
        k = task.get("status", "?")
        by[k] = by.get(k, 0) + 1
    f["tasks_by_status"] = by
    f["memory_count"] = len(mcp.call("vp_memory_list", {"project": s, "limit": 100000}).get("memories", []))
    b = mcp.call("vp_bootstrap_context", {"project": s})
    f["bootstrap"] = {k: b.get(k) for k in ("resume_sha256", "active_task_count", "complete")}
    f["iter_n"] = wrap_iter(mcp, s)
    return f


def kg_rows(q):
    """Rows in a vp_kg_query result: the length of its longest list value."""
    return max([len(x) for x in q.values() if isinstance(x, list)] or [0])


def kg_filenames_dry_run():
    p = run([vp_bin(), "migrate", "kg-filenames", "--dry-run"], cwd=neutral_cwd(), check=False)
    text = (p.stdout + p.stderr).decode(errors="replace")
    ren = re.search(r"Would rename\s*:\s*(\d+)", text)
    col = re.search(r"Would collapse dups\s*:\s*(\d+)", text)
    clean = "No collisions or bad files detected" in text
    renames = int(ren.group(1)) + int(col.group(1)) if ren and col and clean else None
    return {"exit": p.returncode, "renames": renames}


def cmd_measure(a):
    global FROM_ARG, TO_ARG
    FROM_ARG, TO_ARG = a.frm, a.to
    root = os.path.abspath(a.vault)
    # Unconditional: --no-mcp still starts `vp audit vault` in the rehearsal
    # profile, so pinning inside the MCP branch left one subprocess unproven
    # (imp3 round-3 R3-N2).
    pin(root)
    files = tracked(root)
    rec = {"profile": a.profile, "phase": a.phase, "from": a.frm, "to": a.to,
           "head": git(root, "rev-parse", "HEAD").strip()}
    rec["trees"] = {s: tree_classes(root, files, s) for s in (a.frm, a.to)}
    per, pat = literal_counts(root, files, a.frm, a.to)
    rec["literal_per_file"] = per
    rec["literal_total"] = {a.frm: sum(v[0] for v in per.values()), a.to: sum(v[1] for v in per.values())}
    rec["patterns"] = pat
    rec["baseline"] = baseline_counts(root, (a.frm, a.to))
    rec["stray_prefix"] = a.stray_prefix
    rec["stray_group"] = {s: stray_group(root, s, a.stray_prefix) for s in (a.frm, a.to)}
    rec["last_heading"] = {s: last_heading(root, s) for s in (a.frm, a.to)}
    rec["resume_sha256"] = {}
    for s in (a.frm, a.to):
        rp = os.path.join(root, "Projects", s, "resume.md")
        rec["resume_sha256"][s] = sha256_file(rp) if os.path.exists(rp) else None
    samples = {}
    for s in (a.frm, a.to):
        pre = "Projects/%s/tasks/" % s
        ts = sorted(f for f in files if f.startswith(pre) and f.endswith(".md"))[:3]
        samples[s] = {f[len(pre):]: sha256_file(os.path.join(root, f)) for f in ts}
    rec["task_samples"] = samples
    if a.phase == "A":
        rec["base3"] = git(root, "rev-parse", "HEAD~3", check=False).strip()
        rec["name_status"] = {k: name_status(root, r) for k, r in (("K0", "HEAD~2"), ("K1", "HEAD~1"), ("K2", "HEAD"))}
        rec["k2_files"] = [f for f in git(root, "diff", "--name-only", "HEAD^", "HEAD").splitlines() if f]
    rec["mcp"] = {}
    if not a.no_mcp:
        mcp = MCP(cwd=a.mcp_cwd)
        try:
            for s in (a.frm, a.to):
                if os.path.isdir(os.path.join(root, "Projects", s)):
                    rec["mcp"][s] = mcp_facts(root, s, mcp)
            rec["projects"] = sorted(mcp.call("vp_list_projects", {}).get("projects", []))
        finally:
            mcp.close()
        rec["kg_filenames_dry_run"] = kg_filenames_dry_run()
    if a.profile == "rehearsal" and not a.no_audit:
        p = run([vp_bin(), "audit", "vault"], cwd=neutral_cwd(), check=False)
        # Only the verdict-bearing lines: one status heading per dimension
        # ("## `dim` — …") and one bullet per finding ("- `path` — …"). The
        # date line and log lines would differ between any two runs.
        text = scrub(p.stdout.decode(errors="replace"), root)
        rec["audit"] = {"exit": p.returncode,
                        "lines": sorted({ln for ln in text.splitlines() if ln.startswith("## `") or ln.startswith("- `")})}
    rec["floors"] = FLOORS_USED
    rec["mcp_measured"] = not a.no_mcp
    rec["audit_measured"] = a.profile == "rehearsal" and not a.no_audit
    write_json(a.out, scrub(rec, root))


# ---------------------------------------------------------------- verifying


class Verdict:
    def __init__(self, tag):
        self.tag, self.fails = tag, []

    def check(self, name, ok, detail):
        if not ok:
            self.fails.append("%s FAIL %s: %s" % (self.tag, name, detail))

    def finish(self, note=""):
        for f in self.fails:
            print(f)
        if FLOORS_USED:
            print(floors_line())
        if note:
            print(note)
        print("%s %s" % (self.tag, "PASS" if not self.fails else "FAIL"))
        return 0 if not self.fails else 1


def cmd_compare(a):
    x, y = read_json(a.first), read_json(a.second)
    if x == y:
        print("SAME")
        return 0
    for k in sorted(set(x) | set(y)):
        if x.get(k) != y.get(k):
            print("DIFF %s" % k)
    return 1


def cmd_verify(a):
    B, A, C = read_json(a.b), read_json(a.a), read_json(a.counts)
    frm, to = B["from"], B["to"]
    v = Verdict("CENSUS")
    bf, bt, at, af = B["trees"][frm], B["trees"][to], A["trees"][to], A["trees"][frm]
    holds = sum(1 for r in C.get("k0_rename", []) if "/premerge-stray-" in r["dst"])
    ndel = len(C.get("k0_delete", []))

    # Floors: B must have measured something real.
    v.check("C1t floor", bf["tracked"] >= floor("FROM_TRACKED", 2940), "B(%s) tracked %d below floor" % (frm, bf["tracked"]))
    for k in ("sessions", "manifests", "zst", "tasks_active", "memory"):
        v.check("C1t floor", bf[k] >= floor("CLASS", 1), "B(%s) %s is %d" % (frm, k, bf[k]))
    v.check("C1t floor", sum(bf["drawer_rows"].values()) >= floor("DRAWERS", 35620), "B(%s) drawer rows %d" % (frm, sum(bf["drawer_rows"].values())))

    # C1t / C2: tracked arithmetic, and nothing left under the old slug.
    v.check("C2", af["tracked"] == 0, "A(%s) still tracks %d files" % (frm, af["tracked"]))
    v.check("C1t", not af["exists"]["projects"] and not af["exists"]["palace"], "A still has a %s directory" % frm)
    want = bf["tracked"] + bt["tracked"] - ndel - holds
    v.check("C2", at["tracked"] == want, "A(%s) tracked %d, want %d = %d + %d - %d deleted - %d held" % (to, at["tracked"], want, bf["tracked"], bt["tracked"], ndel, holds))
    src_del = sum(1 for d in C.get("k0_delete", []) if d.startswith("Projects/%s/" % frm) or d.startswith("palace/%s/" % frm))
    v.check("C2", C.get("k1_tracked") == bf["tracked"] - src_del, "counts k1_tracked %s, want B(%s) %d - %d source deletes" % (C.get("k1_tracked"), frm, bf["tracked"], src_del))
    for k in ("sessions", "manifests", "zst", "tasks_active", "tasks_done", "tasks_cancelled", "memory", "notes"):
        v.check("C1t", at[k] == bf[k] + bt[k], "A(%s) %s %d, want %d + %d" % (to, k, at[k], bf[k], bt[k]))
    rooms = set(bf["drawer_rows"]) | set(bt["drawer_rows"]) | set(at["drawer_rows"])
    for r in sorted(rooms):
        w = bf["drawer_rows"].get(r, 0) + bt["drawer_rows"].get(r, 0)
        v.check("C1t", at["drawer_rows"].get(r, 0) == w, "room %s rows %d, want %d" % (r, at["drawer_rows"].get(r, 0), w))
    if B["profile"] == "rehearsal":
        v.check("C1 bak", at["bak_on_disk"] == bf["bak_on_disk"] + bt["bak_on_disk"] and af["bak_on_disk"] == 0,
                ".bak A(%s)=%d A(%s)=%d, want %d and 0" % (to, at["bak_on_disk"], frm, af["bak_on_disk"], bf["bak_on_disk"] + bt["bak_on_disk"]))

    # C2: the three commits sit directly on B and have the planned shapes.
    v.check("C2 commits", A.get("base3") == B["head"], "HEAD~3 is %s, want B HEAD %s" % (A.get("base3"), B["head"]))
    ns = A.get("name_status", {})
    k0 = ns.get("K0", {})
    v.check("C2 K0", k0 == {k: n for k, n in (("D", ndel), ("R100", len(C.get("k0_rename", [])))) if n},
            "K0 name-status %s, want D=%d R100=%d" % (k0, ndel, len(C.get("k0_rename", []))))
    v.check("C2 K1", ns.get("K1") == {"R100": C.get("k1_tracked")}, "K1 name-status %s, want R100=%s only" % (ns.get("K1"), C.get("k1_tracked")))
    k2 = ns.get("K2", {})
    v.check("C2 K2", set(k2) <= {"M", "D"} and k2.get("D", 0) == holds, "K2 name-status %s, want M only plus D=%d" % (k2, holds))
    outside = [f for f in A.get("k2_files", []) if not in_scope(f, frm, to)]
    v.check("C3 scope", not outside, "K2 changed files outside the counted scope: %s" % outside[:5])

    # C3: identifiers.
    for k in PATTERNS:
        old = A["patterns"].get(k, {}).get(frm, 0)
        v.check("C3", old == 0, "A still has %d %s occurrences naming %s" % (old, k, frm))
        w = B["patterns"].get(k, {}).get(frm, 0) + B["patterns"].get(k, {}).get(to, 0)
        got = A["patterns"].get(k, {}).get(to, 0)
        v.check("C3", got == w, "A %s naming %s is %d, want %d" % (k, to, got, w))
    v.check("C3 floor", B["patterns"].get("project_fm", {}).get(frm, 0) >= floor("CLASS", 1), "B has no project: %s lines" % frm)
    # C3, Audits/baseline.json. W8 rewrites the accepted[] entries and nothing
    # else, so the census measures exactly that — and holds the reason prose to
    # EQUALITY, because a change there means the rewrite reached text it was
    # never supposed to touch (code review of R-dev, the C3 ruling).
    ba, aa = B.get("baseline", {}), A.get("baseline", {})
    v.check("C3 baseline", aa.get("accepted", {}).get(frm, -1) == 0,
            "A's baseline accepted[] still names %s %s times" % (frm, aa.get("accepted", {}).get(frm)))
    wantAcc = ba.get("accepted", {}).get(frm, 0) + ba.get("accepted", {}).get(to, 0)
    v.check("C3 baseline", aa.get("accepted", {}).get(to, -1) == wantAcc,
            "A's baseline accepted[] names %s %s times, want %d (B: %s + %s)" % (
                to, aa.get("accepted", {}).get(to), wantAcc,
                ba.get("accepted", {}).get(frm), ba.get("accepted", {}).get(to)))
    v.check("C3 baseline floor", ba.get("accepted", {}).get(frm, 0) >= floor("BASELINE", 1),
            "B's baseline accepts no %s path, so this check measured nothing" % frm)
    for s in (frm, to):
        v.check("C3 baseline prose", aa.get("prose", {}).get(s) == ba.get("prose", {}).get(s),
                "baseline reason prose naming %s is %s in A and %s in B; the migration must not touch prose" % (
                    s, aa.get("prose", {}).get(s), ba.get("prose", {}).get(s)))
    gone = set(B["literal_per_file"]) - set(A["literal_per_file"])
    deleted = set(C.get("k0_delete", [])) | {r["dst"] for r in C.get("k0_rename", []) if "/premerge-stray-" in r["dst"]}
    lost_f = sum(B["literal_per_file"][p][0] for p in gone & set(C.get("k0_delete", [])))
    lost_t = sum(B["literal_per_file"][p][1] for p in gone & set(C.get("k0_delete", [])))
    removed = B["literal_total"][frm] - lost_f - A["literal_total"][frm]
    added = A["literal_total"][to] - (B["literal_total"][to] - lost_t)
    v.check("C3 literal", removed == added, "%d %s literals removed but %d %s literals added (K0-deleted files: %s)" % (removed, frm, added, to, sorted(deleted)))

    # C6: KG untouched.
    bk, ak = bf["kg_files"], at["kg_files"]
    v.check("C6 floor", len(bk) >= floor("KG_FILES", 1100), "B kg files %d" % len(bk))
    v.check("C6", ak == {k: h for k, h in bk.items()} | {k: h for k, h in bt["kg_files"].items() if k not in bk}, "kg file hashes differ")
    # An absent input is a FAILURE, not a skip: a census that quietly stopped
    # measuring C5/C6/C8/C10/C11 would still print PASS (code review round 1, C7).
    v.check("inputs", B.get("mcp_measured") is not False and A.get("mcp_measured") is not False,
            "a census was measured with --no-mcp, so C5, C6, C8, C10 and C11 were not measured")
    bm, am = B["mcp"].get(frm), A["mcp"].get(to)
    v.check("inputs", bm is not None and am is not None,
            "MCP facts missing (B has %s, A has %s); C5, C6, C8, C10 and C11 cannot be checked" % (bm is not None, am is not None))
    if bm and am:
        v.check("C6", "error" not in bm["kg_stats"] and am["kg_stats"] == bm["kg_stats"], "kg_stats differ: %s vs %s" % (bm["kg_stats"], am["kg_stats"]))
        v.check("C6", am["kg_query"] == bm["kg_query"], "kg_query results differ")
        for name, q in bm["kg_query"].items():
            v.check("C6 floor", "error" not in q and kg_rows(q) > 0, "B kg query %r returned nothing: %s" % (name, q))
        # C8, C10, C11.
        v.check("C8", am["iter_n"] == bm["iter_n"] == B["last_heading"][frm] + 1, "iter_n A %s, B %s, B last heading %s" % (am["iter_n"], bm["iter_n"], B["last_heading"][frm]))
        v.check("C8 floor", (bm["iter_n"] or 0) > 1, "B iter_n %s" % bm["iter_n"])
        bt_t = dict(bm["tasks_by_status"])
        for k, n in B["mcp"].get(to, {}).get("tasks_by_status", {}).items():
            bt_t[k] = bt_t.get(k, 0) + n
        v.check("C10", am["tasks_by_status"] == bt_t, "tasks by status A %s, want %s" % (am["tasks_by_status"], bt_t))
        wm = bm["memory_count"] + B["mcp"].get(to, {}).get("memory_count", 0)
        v.check("C10", am["memory_count"] == wm, "memory A %d, want %d" % (am["memory_count"], wm))
        v.check("C11", am["bootstrap"]["resume_sha256"] == A["resume_sha256"][to] and am["bootstrap"]["complete"] is True,
                "bootstrap %s, committed resume %s" % (am["bootstrap"], A["resume_sha256"][to]))
        v.check("C11", am["bootstrap"]["active_task_count"] == bm["bootstrap"]["active_task_count"] + (B["mcp"].get(to, {}).get("bootstrap", {}).get("active_task_count") or 0),
                "active_task_count A %s, B %s" % (am["bootstrap"]["active_task_count"], bm["bootstrap"]["active_task_count"]))
        v.check("C5", frm not in A.get("projects", []) and to in A.get("projects", []), "projects after: %s" % A.get("projects"))
        for side, rec in (("B", B), ("A", A)):
            kd = rec.get("kg_filenames_dry_run", {})
            v.check("C6 kg-filenames", kd.get("exit") == 0 and kd.get("renames") == 0, "%s kg-filenames dry run %s" % (side, kd))
    for rel, h in B["task_samples"][frm].items():
        v.check("C10", A["task_samples"][to].get(rel) == h, "sampled task %s changed" % rel)
    v.check("C10 floor", len(B["task_samples"][frm]) >= min(3, floor("CLASS", 1)), "no sampled tasks")

    # C8: the collision group is contiguous with unique session_ids.
    g = A["stray_group"].get(to, [])
    if B.get("stray_prefix"):
        nums = [n for n, _ in g]
        ids = [i for _, i in g]
        v.check("C8 stray", nums == list(range(1, len(nums) + 1)) and len(set(ids)) == len(ids) and len(nums) == len(B["stray_group"][frm]) + len(B["stray_group"][to]),
                "group %s in A: numbers %s, %d unique ids" % (B["stray_prefix"], nums, len(set(ids))))
    if B["profile"] == "rehearsal":
        v.check("inputs", "audit" in B and "audit" in A,
                "the rehearsal profile requires the audit on both sides (B %s, A %s)" % ("audit" in B, "audit" in A))
    if B["profile"] == "rehearsal" and "audit" in B and "audit" in A:
        mapped = {line.replace(frm, to) for line in B["audit"]["lines"]}
        new = [line for line in A["audit"]["lines"] if line not in mapped]
        v.check("C4", not new, "audit verdict lines only in A (a new finding or a dimension that flipped): %s" % new[:5])
        # And the other direction: a finding that DISAPPEARS is also a change
        # the migration made to the audit's view of the vault, and the plan
        # says A's findings equal B's with paths mapped. Measured equal on the
        # live data (17 lines either way), so this is equality, not a hope.
        gone = [line for line in mapped if line not in set(A["audit"]["lines"])]
        v.check("C4", not gone, "audit verdict lines that B had and A does not: %s" % gone[:5])
        v.check("C4 floor", len(B["audit"]["lines"]) >= floor("AUDIT_LINES", 10), "B audit has %d verdict lines" % len(B["audit"]["lines"]))
    return v.finish()


# ---------------------------------------------------------------- cache


def cache_dir(root, s):
    return os.path.join(root, "palace", ".local", "embed-cache", s)


def snapshot(root, slugs):
    out = []
    for s in slugs:
        d = cache_dir(root, s)
        if not os.path.isdir(d):
            continue
        for n in sorted(os.listdir(d)):
            p = os.path.join(d, n)
            if n.endswith(".vec") and os.path.isfile(p):
                out.append((s, n, os.path.getsize(p), sha256_file(p)))
    return out


def write_snapshot(path, snap):
    with open(path, "w") as f:
        for s, n, z, h in snap:
            f.write("%s\t%s\t%d\t%s\n" % (s, n, z, h))


def read_snapshot(path):
    out = []
    with open(path) as f:
        for line in f:
            s, n, z, h = line.rstrip("\n").split("\t")
            out.append((s, n, int(z), h))
    return out


def drawer_ids(root, s):
    ids = []
    base = os.path.join(root, "palace", s, "drawers", s)
    if not os.path.isdir(base):
        return ids
    for room in sorted(os.listdir(base)):
        p = os.path.join(base, room, "drawers.jsonl")
        if os.path.isfile(p):
            with open(p, encoding="utf-8", errors="replace") as fh:
                for line in fh:
                    try:
                        ids.append(json.loads(line)["id"])
                    except (ValueError, KeyError):
                        pass
    return ids


def cmd_cache_warm(a):
    root = os.path.abspath(a.vault)
    # The warm check runs `vp search`, which has no --vault: pin it like every
    # other measuring path (code review round 2, D6).
    pin(root)
    slugs = [s for s in (a.frm, a.to) if os.path.isdir(os.path.join(root, "Projects", s))]
    missing = []
    for s in slugs:
        d = cache_dir(root, s)
        missing += ["%s/%s.vec" % (s, i) for i in drawer_ids(root, s) if not os.path.exists(os.path.join(d, i + ".vec"))]
    new = []
    if not a.no_search:
        before = {(s, n) for s, n, _, _ in snapshot(root, slugs)}
        for s in slugs:
            p = run([vp_bin(), "search", "-p", s, "-n", "1", "warm"], cwd=neutral_cwd(), check=False)
            if p.returncode != 0:
                die("CACHE COLD search failed for %s: %s" % (s, p.stderr.decode(errors="replace").strip()), 1)
        new = sorted("%s/%s" % x for x in {(s, n) for s, n, _, _ in snapshot(root, slugs)} - before)
    if missing or new:
        for m in missing[:10]:
            print("missing drawer vector: %s" % m)
        for n in new[:10]:
            print("embedded by the check itself: %s" % n)
        print("CACHE COLD %d" % (len(missing) + len(new)))
        return 1
    if a.snapshot:
        write_snapshot(a.snapshot, snapshot(root, slugs))
    print("CACHE WARM")
    return 0


def cmd_cache_report(a):
    root = os.path.abspath(a.vault)
    frm, to = a.frm, a.to
    v = Verdict("CACHE-REPORT")
    line = ""
    with open(a.log, errors="replace") as f:
        for ln in f:
            if ln.startswith("cache: "):
                line = ln.strip()
    m = re.search(r"stray-deleted (\d+) .*renamed (\d+), orphans-saved (\d+), duplicates-saved (\d+)", line)
    v.check("log", m is not None, "no 'cache: stray-deleted …' line in %s" % a.log)
    nd, nr, no, ndup = (int(x) for x in m.groups()) if m else (-1, -1, -1, -1)
    pre = read_snapshot(a.pre)
    pre_from = {n: (z, h) for s, n, z, h in pre if s == frm}
    pre_to = {n: (z, h) for s, n, z, h in pre if s == to}
    v.check("floor", len(pre_from) >= floor("CACHE_FROM", 40000), "pre list has %d %s vectors" % (len(pre_from), frm))
    post_now = {n: (z, h) for s, n, z, h in snapshot(root, [to])}
    stray = []
    with open(a.stray) as f:
        for ln in f:
            p = ln.split()
            if len(p) == 2:
                stray.append((p[0], p[1]))
    v.check("stray", nd == len(stray), "report says stray-deleted %d, list has %d" % (nd, len(stray)))
    for h, n in stray:
        v.check("stray", pre_to.get(n, (0, ""))[1] == h, "stray %s sha differs from the pre snapshot" % n)
        # The stray's BYTES must be gone. The name may legitimately come back:
        # the source project has its own note for the same date and writer, and
        # its vector renames into exactly this name.
        v.check("stray", post_now.get(n, (0, ""))[1] != h, "stray %s was not deleted (same sha256 still there)" % n)
    fd = cache_dir(root, frm)
    left = [n for n in os.listdir(fd) if n.endswith(".vec")] if os.path.isdir(fd) else []
    v.check("from", not left, "%d vectors remain under embed-cache/%s" % (len(left), frm))
    # Old -> new names: drawers by id re-hash of the migrated rows, notes and iterations by prefix.
    old2new = {}
    base = os.path.join(root, "palace", to, "drawers", to)
    if os.path.isdir(base):
        for room in os.listdir(base):
            p = os.path.join(base, room, "drawers.jsonl")
            if os.path.isfile(p):
                with open(p, encoding="utf-8", errors="replace") as fh:
                    for ln in fh:
                        try:
                            d = json.loads(ln)
                        except ValueError:
                            continue
                        old2new[drawer_id(frm, d["content"]) + ".vec"] = d["id"] + ".vec"
    post = {n: (z, h) for s, n, z, h in snapshot(root, [to])}
    renamed, orphans, by = 0, 0, {"drawer": 0, "note": 0, "iter": 0}
    for n, zh in pre_from.items():
        if n.startswith("note.%s." % frm):
            new, cls = "note.%s.%s" % (to, n[len("note.%s." % frm):]), "note"
        elif n.startswith("iter.%s." % frm):
            new, cls = "iter.%s.%s" % (to, n[len("iter.%s." % frm):]), "iter"
        elif n in old2new:
            new, cls = old2new[n], "drawer"
        else:
            orphans += 1
            continue
        v.check("rename", post.get(new) == zh, "%s -> %s missing or changed" % (n, new))
        renamed += 1
        by[cls] += 1
    # A mapped source whose destination already held identical bytes is saved
    # rather than renamed, so it counts under duplicates-saved.
    v.check("rename", renamed == nr+ndup, "report says renamed %d plus duplicates-saved %d, pre list maps %d (%s)" % (nr, ndup, renamed, by))
    v.check("orphans", orphans == no, "report says orphans-saved %d, pre list has %d unmapped" % (no, orphans))
    stray_names = {n for _, n in stray}
    for n, zh in pre_to.items():
        if n not in stray_names:
            v.check("target", post.get(n) == zh, "target vector %s missing or changed" % n)
    expected = {n for n in pre_to if n not in stray_names}
    for n in pre_from:
        if n.startswith("note.%s." % frm):
            expected.add("note.%s.%s" % (to, n[len("note.%s." % frm):]))
        elif n.startswith("iter.%s." % frm):
            expected.add("iter.%s.%s" % (to, n[len("iter.%s." % frm):]))
        elif n in old2new:
            expected.add(old2new[n])
    extra = sorted(set(post) - expected)
    v.check("extra", not extra, "unexpected vectors under embed-cache/%s: %s" % (to, extra[:5]))
    return v.finish("renamed by class: drawer=%d note=%d iter=%d" % (by["drawer"], by["note"], by["iter"]))


# ---------------------------------------------------------------- parity


def norm_ref(row):
    ref = row.get("source_ref", "")
    if row.get("source_type") == "decision":
        ref = re.sub(r"/[0-9a-f]{8}$", "", ref)
    return ref


def row_key(row):
    return [row.get("source_type", ""), norm_ref(row), row.get("room", ""), sha256_text(row.get("content", ""))]


# SLUG_SELFTEST_EMPTY_QUERY makes one query ask for a room that holds nothing,
# so the self-test can prove the "this query is broken" refusal fires.
if os.environ.get("SLUG_SELFTEST_EMPTY_QUERY"):
    QUERIES = QUERIES + [{"q": "a query that matches nothing at all", "room": "no-such-room"}]


def query_n(q, phase):
    """The -n this query runs at. Filtered queries go deep in both phases."""
    if "room" in q or "wing" in q:
        return N_FILTERED
    return N_PLAIN_B1 if phase == "b1" else N_PLAIN_A


def search_run(slug, phase, frm, root):
    res = []
    for q in QUERIES:
        cmd = [vp_bin(), "search", "--json", "-n", str(query_n(q, phase)), "-p", slug]
        if "wing" in q:
            cmd += ["-w", slug if q["wing"] == "FROM" else q["wing"]]
        if "room" in q:
            cmd += ["-r", q["room"]]
        cmd.append(q["q"])
        p = run(cmd, cwd=neutral_cwd())
        rows = json.loads(p.stdout.decode() or "[]") or []
        res.append([{"key": row_key(r), "score": r.get("score"), "project": r.get("project")} for r in rows])
    return scrub(res, root)


def groups(rows):
    out = []
    for r in rows:
        if out and out[-1][0] == r["score"]:
            out[-1][1].append(tuple(r["key"]))
        else:
            out.append((r["score"], [tuple(r["key"])]))
    return out


def compare_query(b1, a_rows, stray, b1_n=10):
    """The plan's pass rule. Returns a failure string or ''.

    Drop the stray rows from A and keep the first r = |B1| rows. The tie-groups
    (bit-identical scores) must match B1's in sequence and as sets, except the
    last one, which may cross the cut at r: there A's members must be a subset
    of B1's group. When B1 itself was cut at its -n (it returned b1_n rows),
    B1's last group is incomplete too, so only its size and score must match.
    """
    a = [r for r in a_rows if r["key"][3] not in stray][:len(b1)]
    gb, ga = groups(b1), groups(a)
    if len(ga) > len(gb):
        return "A has %d tie-groups in the first %d rows, B1 has %d" % (len(ga), len(b1), len(gb))
    for i, (sa, ka) in enumerate(ga):
        sb, kb = gb[i]
        if sa != sb:
            return "tie-group %d score %r != B1 %r" % (i, sa, sb)
        last = i == len(ga) - 1
        if not last and set(ka) != set(kb):
            return "tie-group %d members differ" % i
        if last:
            if len(ga) != len(gb):
                return "A has %d tie-groups, B1 has %d" % (len(ga), len(gb))
            b1_cut = len(b1) >= b1_n
            if not (set(ka) <= set(kb) or (b1_cut and len(ka) == len(kb))):
                return "last tie-group is not a subset of B1's"
    return ""


def new_vecs(root, s, before):
    return sorted({n for _, n, _, _ in snapshot(root, [s])} - before)


def cmd_parity_baseline(a):
    root = os.path.abspath(a.vault)
    pin(root)
    C = read_json(a.counts)
    b1a = search_run(a.frm, "b1", a.frm, root)
    b1b = search_run(a.frm, "b1", a.frm, root)
    b2 = search_run(a.to, "a", a.frm, root)
    stray = sorted({r["key"][3] for q in b2 for r in q})
    renames = {}
    pre = "Projects/%s/sessions/" % a.to
    for r in C.get("k0_rename", []):
        if r["src"].startswith(pre):
            renames[r["src"][len(pre):-3]] = r["dst"][len(pre):-3]
    expect = []
    d = cache_dir(root, a.to)
    for n in sorted(os.listdir(d)) if os.path.isdir(d) else []:
        for old, new in renames.items():
            p = "note.%s.%s." % (a.to, old)
            if n.startswith(p):
                expect.append("note.%s.%s.%s" % (a.to, new, n[len(p):]))
    out = {"queries": QUERIES, "from": a.frm, "to": a.to, "b1": b1a, "b1_repeat": b1b, "b2": b2,
           "stray_hashes": stray, "expect_new_vec": sorted(expect),
           "n": {"plain_b1": N_PLAIN_B1, "plain_a": N_PLAIN_A, "filtered": N_FILTERED}}
    v = Verdict("BASELINE")
    v.check("determinism", b1a == b1b, "the two B1 runs differ")
    # F3: the ONE absolute floor. Its only job is to catch a query set that
    # measures nothing — a query returning 0 rows is broken, not shallow — plus
    # the independent global total. Everything else is derived from B1 itself.
    for i, q in enumerate(b1a):
        v.check("floor", len(q) >= 1, "B1 query %d returned 0 rows: the query is broken, not merely shallow (%r)" % (i + 1, QUERIES[i]["q"]))
    total = sum(len(q) for q in b1a)
    v.check("floor", total >= floor("TOTAL", 150), "B1 total %d rows" % total)
    out["b1_counts"] = [len(q) for q in b1a]
    print("B1 rows per query: %s (total %d)" % (out["b1_counts"], total))
    v.check("stray", bool(expect), "no stray note vectors found to predict the re-embed")
    out["floors"] = FLOORS_USED
    write_json(a.out, out)
    return v.finish()


def cmd_parity_check(a):
    root = os.path.abspath(a.vault)
    got = pin(root)
    base = read_json(a.b1)
    to, frm = base["to"], base["from"]
    stray = set(base["stray_hashes"])
    v = Verdict("PARITY")
    v.check("baseline", base["b1"] == base["b1_repeat"], "the baseline's two B1 runs differ")
    # A and B1 must search a filtered query at the SAME depth, or the
    # comparison is not like for like and F2's floor measures the difference
    # between two depths rather than between two corpora (imp3 R4-N2).
    for qi, q in enumerate(QUERIES):
        if "room" in q or "wing" in q:
            v.check("depth symmetry q%d" % (qi + 1), query_n(q, "b1") == query_n(q, "a"),
                    "filtered query %d runs at -n %d in B1 and -n %d in A" % (qi + 1, query_n(q, "b1"), query_n(q, "a")))
    runs = []
    for i in range(a.runs):
        before = {n for _, n, _, _ in snapshot(root, [to])}
        r = search_run(to, "a", frm, root)
        created = new_vecs(root, to, before)
        want = base["expect_new_vec"] if i == 0 else []
        v.check("vectors run %d" % (i + 1), created == want, "new vectors %s, want exactly %s" % (created[:6], want))
        runs.append(r)
    for i, r in enumerate(runs[1:], start=2):
        v.check("determinism", r == runs[0], "run %d differs from run 1" % i)
    a1 = runs[0]
    # SLUG_SELFTEST_TRUNCATE_A drops rows from every A query, so the self-test
    # can prove the depth floor below actually fires.
    if os.environ.get("SLUG_SELFTEST_TRUNCATE_A"):
        a1 = [q[:1] for q in a1]
    for qi, (b1, rows) in enumerate(zip(base["b1"], a1)):
        v.check("project q%d" % (qi + 1), all(x["project"] == to for x in rows), "a row is not in project %s" % to)
        # F2: the floor for this query is what B1 itself measured. A migration
        # that LOSES retrieval shows up here; a query that is merely shallow
        # does not, because its own baseline is shallow too.
        kept = [r for r in rows if r["key"][3] not in stray]
        v.check("depth q%d" % (qi + 1), len(kept) >= len(b1),
                "query %d returned %d rows after dropping strays, B1 measured %d" % (qi + 1, len(kept), len(b1)))
        msg = compare_query(b1, rows, stray, b1_n=query_n(QUERIES[qi], "b1"))
        v.check("query %d %r" % (qi + 1, QUERIES[qi]["q"]), not msg, msg)
    if a.out:
        write_json(a.out, {"runs": runs})
    return v.finish("searched vault: %s" % got)


# ---------------------------------------------------------------- rehearsal helpers


def cmd_mcp_call(a):
    # This is the harness's only writing path (the C12 backfills run with
    # apply=true), and it chose its vault entirely by XDG_CONFIG_HOME and the
    # caller's --cwd. Prove the vault first, like every other mode.
    pin(os.path.abspath(a.vault))
    mcp = MCP(cwd=a.cwd)
    try:
        r = mcp.call(a.tool, json.loads(a.args))
    finally:
        mcp.close()
    write_json("-", r)
    return 0


def decisions_rows(path):
    out = []
    if os.path.exists(path):
        with open(path, encoding="utf-8", errors="replace") as fh:
            for line in fh:
                if line.strip():
                    out.append(line.rstrip("\n"))
    return out


def cmd_c12(a):
    """Compare the rows the decision backfill appends before and after.

    BFA covers the MERGED project, so its set is BFB (the source's own notes)
    plus BFT (the target's own notes, measured on a pre-migration copy), with
    the target's renamed session stems mapped through counts.json. Assuming
    the target contributes nothing would let a lost target decision pass.
    """
    def appended(before, after):
        b = set(decisions_rows(before))
        rows = []
        for line in decisions_rows(after):
            if line in b:
                continue
            d = json.loads(line)
            m = re.match(r"session/(.+)#decision/(\d+)", d.get("source_ref", ""))
            rows.append((m.group(1) if m else d.get("source_ref", ""), m.group(2) if m else "", d.get("content", "")))
        return rows
    C = read_json(a.counts)
    pre = "Projects/%s/sessions/" % a.to
    renames = {}
    for r in C.get("k0_rename", []):
        if r["src"].startswith(pre) and r["src"].endswith(".md"):
            renames[r["src"][len(pre):-3]] = r["dst"][len(pre):-3]
    src = sorted(appended(a.bfb_before, a.bfb_after))
    tgt = sorted((renames.get(stem, stem), idx, content) for stem, idx, content in appended(a.bft_before, a.bft_after))
    got = sorted(appended(a.bfa_before, a.bfa_after))
    want = sorted(set(src) | set(tgt))
    v = Verdict("C12")
    v.check("sets", got == want, "after the migration the backfill appended %d, want %d = %d source + %d target; missing %s; unexpected %s"
            % (len(got), len(want), len(src), len(tgt), sorted(set(want) - set(got))[:3], sorted(set(got) - set(want))[:3]))
    v.check("floor", len(want) >= floor("C12", 1), "the backfill appended nothing anywhere, so C12 measured nothing")
    print("C12 appended source=%d target=%d after=%d" % (len(src), len(tgt), len(got)))
    return v.finish()


def is_live(vault):
    """M3's classification, fail-closed: a remote, or the configured vault."""
    p = run(["git", "-C", vault, "remote"], check=False)
    if p.returncode != 0 or p.stdout.strip():
        return True
    cfg = os.path.join(os.path.expanduser("~"), ".config", "vibe-palace", "config.toml")
    try:
        import tomllib
        with open(cfg, "rb") as f:
            vp = os.path.expanduser(tomllib.load(f).get("vault_path", ""))
        return os.path.samefile(vp, vault)
    except (OSError, ValueError):
        return True


def cmd_pin(a):
    print(pin(os.path.abspath(a.vault)))
    return 0


def cmd_is_live(a):
    live = is_live(os.path.abspath(a.vault))
    print("LIVE" if live else "COPY")
    return 0 if live else 1


def cmd_snapshot(a):
    write_snapshot(a.out, snapshot(os.path.abspath(a.vault), a.slugs))
    return 0


def main():
    ap = argparse.ArgumentParser(prog="slugcheck.py")
    sp = ap.add_subparsers(dest="cmd", required=True)

    def common(p):
        p.add_argument("--vault", required=True)
        p.add_argument("--from", dest="frm", default=FROM_DEFAULT)
        p.add_argument("--to", default=TO_DEFAULT)

    p = sp.add_parser("measure")
    common(p)
    p.add_argument("--profile", choices=["live", "rehearsal"], required=True)
    p.add_argument("--phase", choices=["A", "B"], required=True)
    p.add_argument("--out", required=True)
    p.add_argument("--stray-prefix", default="2026-08-22-0f0d1eb5")
    p.add_argument("--mcp-cwd", default=None)
    p.add_argument("--no-mcp", action="store_true")
    p.add_argument("--no-audit", action="store_true")
    p = sp.add_parser("compare")
    p.add_argument("first")
    p.add_argument("second")
    p = sp.add_parser("verify")
    p.add_argument("--b", required=True)
    p.add_argument("--a", required=True)
    p.add_argument("--counts", required=True)
    p = sp.add_parser("cache-warm")
    common(p)
    p.add_argument("--snapshot")
    p.add_argument("--no-search", action="store_true")
    p = sp.add_parser("cache-report")
    common(p)
    p.add_argument("--log", required=True)
    p.add_argument("--pre", required=True)
    p.add_argument("--stray", required=True)
    p = sp.add_parser("parity-baseline")
    common(p)
    p.add_argument("--counts", required=True)
    p.add_argument("--out", required=True)
    p = sp.add_parser("parity-check")
    p.add_argument("--vault", required=True)
    p.add_argument("--b1", required=True)
    p.add_argument("--runs", type=int, default=1)
    p.add_argument("--out")
    p = sp.add_parser("mcp-call")
    p.add_argument("tool")
    p.add_argument("args")
    p.add_argument("--vault", required=True)
    p.add_argument("--cwd")
    p = sp.add_parser("c12")
    for k in ("bfb-before", "bfb-after", "bft-before", "bft-after", "bfa-before", "bfa-after"):
        p.add_argument("--" + k, required=True)
    p.add_argument("--counts", required=True)
    p.add_argument("--to", default=TO_DEFAULT)
    p = sp.add_parser("pin")
    p.add_argument("--vault", required=True)
    p = sp.add_parser("is-live")
    p.add_argument("--vault", required=True)
    p = sp.add_parser("snapshot")
    p.add_argument("--vault", required=True)
    p.add_argument("--out", required=True)
    p.add_argument("slugs", nargs="+")
    a = ap.parse_args()
    fn = {"measure": cmd_measure, "compare": cmd_compare, "verify": cmd_verify, "cache-warm": cmd_cache_warm,
          "cache-report": cmd_cache_report, "parity-baseline": cmd_parity_baseline,
          "parity-check": cmd_parity_check, "mcp-call": cmd_mcp_call, "c12": cmd_c12,
          "snapshot": cmd_snapshot, "is-live": cmd_is_live, "pin": cmd_pin}[a.cmd]
    try:
        rc = fn(a)
    except (OSError, ValueError, KeyError, RuntimeError) as e:
        die("slugcheck %s: %s: %s" % (a.cmd, type(e).__name__, e))
    sys.exit(rc or 0)


FROM_ARG, TO_ARG = FROM_DEFAULT, TO_DEFAULT

if __name__ == "__main__":
    main()
