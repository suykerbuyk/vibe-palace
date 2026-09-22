#!/usr/bin/env python3
# Copyright (c) 2026 John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0
"""ONE-SHOT: build the fixture that selftest.sh runs the harness against.

Usage: selftest_fixture.py DIR

Writes, all under DIR and nowhere else:
  DIR/vault           a git vault with a quantum-ng source (every rewrite
                      class) and a qa-metabuild-system target that collides
                      (scaffolds, the -01 session, stray decisions);
  DIR/xdg             a vp config whose vault_path is DIR/vault;
  DIR/project-repo    a git repo standing in for ~/code/qa-metabuild-system
                      (its .vibe-palace.toml still names quantum-ng).
It never reads or writes the live vault.
"""

import hashlib
import json
import os
import subprocess
import sys

FROM, TO = "quantum-ng", "qa-metabuild-system"
STEM = "2026-08-22-0f0d1eb5"


def w(root, rel, text, mode="w"):
    p = os.path.join(root, rel)
    os.makedirs(os.path.dirname(p), exist_ok=True)
    with open(p, mode) as f:
        f.write(text)


def did(wing, content):
    return hashlib.md5((wing + content).encode()).hexdigest()[:8]


def row(wing, content, room, stype, ref):
    i = did(wing, content)
    if ref.endswith("#decision/"):
        ref = ref + "0/" + i
    # Compact, as Go's encoding/json writes drawers.jsonl in the real vault.
    return json.dumps({"id": i, "hall": "facts", "content": content, "source_type": stype,
                       "source_ref": ref, "filed_at": "2026-08-22T00:00:00Z"}, separators=(",", ":")) + "\n"


def git(root, *a):
    subprocess.run(["git", "-C", root] + list(a), check=True, stdout=subprocess.DEVNULL)


def session(slug, n, body, archive=None, decisions=()):
    sid = "%s-%02d" % (STEM, n)
    fm = ["---", "session_id: " + sid, "project: " + slug, "date: 2026-08-22", "iteration: %d" % n,
          "note_path: Projects/%s/sessions/%s.md" % (slug, sid)]
    if archive:
        fm.append("archive: Projects/%s/transcripts/%s" % (slug, archive))
    if decisions:
        # A YAML decisions list is the ONLY thing the decision backfill files,
        # so C12 has something to compare before and after the migration.
        fm.append("decisions:")
        fm += ["  - " + d for d in decisions]
    return "\n".join(fm + ["---", "", body, ""])


def main():
    d = os.path.abspath(sys.argv[1])
    v = os.path.join(d, "vault")
    P, Q = "Projects/%s/" % FROM, "Projects/%s/" % TO
    R, S = "palace/%s/" % FROM, "palace/%s/" % TO
    w(v, ".gitignore", "*.bak\npalace/.local/\n.vp-locks/\n")
    w(v, ".vibe-palace/vault.toml", "format = 2\n")
    bodies = [
        "Decided the nested hypervisor for the Proxmox cluster runs under pveforge. quantum-ng prose stays.",
        "One build at a time: the WSL2 host dies if two builds run together.",
        "TFTP RRQ was not sent during PXE boot until the D1 mgmt nginx stopped serving stale content.",
    ]
    for n, body in enumerate(bodies, start=1):
        arch = "2026-08-22-aaaa%d.manifest.json" % n
        w(v, P + "sessions/%s-%02d.md" % (STEM, n), session(FROM, n, body, arch,
          decisions=["Backfill decision %d - pin the PXE boot path for session %d." % (n, n)]))
        w(v, P + "transcripts/" + arch, json.dumps({
            "session_id": "aaaa%d" % n, "project_slug": FROM,
            "vault_rel_session_note": P + "sessions/%s-%02d.md" % (STEM, n)}, indent=2) + "\n")
        raw = os.path.join(d, "t%d.jsonl" % n)
        w(d, "t%d.jsonl" % n, json.dumps({"type": "user", "message": {"role": "user", "content": body}}) + "\n")
        subprocess.run(["zstd", "-q", "-f", raw, "-o", os.path.join(v, P + "transcripts/2026-08-22-aaaa%d.jsonl.zst" % n)], check=True)
        os.remove(raw)
    w(v, P + "transcripts/2026-08-22-aaaa1.manifest.json.1234.bak", "old manifest backup\n")
    w(v, P + "tasks/active-task.md", "# Active\n\n**Status:** planning\n\n## Plan\n\nKnown-flake retry is reported as FLAKE, never PASS.\n")
    w(v, P + "tasks/second-task.md", "# Second\n\n**Status:** in_progress\n\n## Plan\n\nWorkspace root contaminated by a subproject.\n")
    w(v, P + "tasks/done/done-task.md", "# Done\n\n**Status:** done\n\n## Plan\n\nSONiC VS emulator parity baseline.\n")
    w(v, P + "tasks/cancelled/cancelled-task.md", "# Cancelled\n\n**Status:** cancelled\n\n## Plan\n\nEdgeCore switch fabric discovery.\n")
    w(v, P + "memory/one-build-at-a-time.md", "---\nname: one-build-at-a-time\ndescription: builds\n---\nquantum-ng runs one build at a time.\n")
    w(v, P + "notes/escalation.md", "---\ntype: security\nproject: %s\ntracked_by: %stasks/active-task.md\n---\nRedact credentials before writing to the vault.\n" % (FROM, P))
    w(v, P + "resume.md", "---\nproject: %s\n---\n\n# %s — Working Context\n\nResume exceeds the core floor and blocks ADR-009 arming.\n" % (FROM, FROM))
    w(v, P + "workflow.md", "# %s — Workflow\n\nsteps\n" % FROM)
    w(v, P + "iterations.md", "".join("## Iteration %d — step %d\n\nNexus asset cache for Proxmox deploys, part %d.\n\n" % (i, i, i) for i in range(1, 6)))
    w(v, P + "commit-log.md", "## commit abc\n")
    w(v, P + "commit-log.anchor", "f01cbe4\n")
    w(v, P + "commands/README.md", "commands for quantum-ng\n")
    w(v, P + "skills/README.md", "skills for quantum-ng\n")
    w(v, P + ".surface", "surface = 7\n")
    w(v, R + ".surface", "surface = 7\n")
    w(v, R + "kg/entities.jsonl", json.dumps({"id": "project-quantum-ng", "name": FROM, "type": "project"}) + "\n")
    # The canonical flat triple filename, so the kg-filenames dry run is 0.
    w(v, R + "kg/triples/quantum-ng_0ce7606f--mentioned_in_efba2031--s1_e8bc163c.json",
      json.dumps({"subject": FROM, "predicate": "mentioned_in", "object": "s1"}) + "\n")
    dec = ["Nested Proxmox belongs to pveforge, not quantum-ng.", "Keep the shell facade after the pveforge adoption decision.",
           "Scaler protobuf config is optional on the host."]
    w(v, R + "drawers/%s/decisions/drawers.jsonl" % FROM,
      "".join(row(FROM, c, "decisions", "decision", "session/%s-%02d#decision/" % (STEM, i + 1)) for i, c in enumerate(dec)))
    gen = ["Spectrum ASIC emulation under normal hypervisors is slow.", "tapemanager qa/dev-johns forwardable patch archive.",
           "Cluster teardown fails on a two-node PVE cluster without PVE_NODE.", "A subagent claim is not evidence until someone opens the file.",
           "vp_manage_task amend replaces the whole section."]
    w(v, R + "drawers/%s/general/drawers.jsonl" % FROM, "".join(row(FROM, c, "general", "session", "aaaa1") for c in gen))
    w(v, R + "drawers/%s/security/drawers.jsonl" % FROM,
      row(FROM, "The unauthenticated control plane committed secrets.", "security", "session", "aaaa2"))

    w(v, Q + ".surface", "surface = 6\n")
    w(v, Q + "commands/README.md", "new commands readme\n")
    w(v, Q + "skills/README.md", "new skills readme\n")
    w(v, Q + "sessions/%s-01.md" % STEM, session(TO, 1, "Stray Grok session: archive-then-retire tapemanager leftover QA topics.",
      decisions=["Backfill decision S - retire the leftover QA topics."]))
    w(v, S + ".surface", "surface = 3\n")
    w(v, S + "drawers/%s/decisions/drawers.jsonl" % TO,
      row(TO, "Archive then retire the tapemanager leftover QA topics.", "decisions", "decision", "session/%s-01#decision/" % STEM))
    w(v, "Audits/baseline.json", json.dumps({"dimensions": {"archive-roundtrip": {
        "reason": "accepted during the fixture", "accepted": [P + "transcripts/2026-08-22-aaaa1.manifest.json"]}}}, indent=2) + "\n")
    w(v, "Audits/.surface", "surface = 3\n")
    w(v, "Projects/other/resume.md", "---\nproject: other\n---\n\n# other — Working Context\n\nMentions quantum-ng in prose.\n")
    git(v, "init", "-q", "-b", "main")
    git(v, "config", "user.email", "fixture@localhost")
    git(v, "config", "user.name", "fixture")
    git(v, "add", "-A")
    git(v, "commit", "-q", "-m", "fixture")

    w(d, "xdg/vibe-palace/config.toml", 'vault_path = "%s"\n' % v)
    pr = os.path.join(d, "project-repo")
    w(pr, ".vibe-palace.toml", '[project]\nname = "%s"\n' % FROM)
    w(pr, "README.md", "stand-in for ~/code/qa-metabuild-system\n")
    git(pr, "init", "-q", "-b", "main")
    git(pr, "add", "-A")
    git(pr, "-c", "user.email=fixture@localhost", "-c", "user.name=fixture", "commit", "-q", "-m", "fixture")
    print(v)


if __name__ == "__main__":
    main()
