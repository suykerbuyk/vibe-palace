Add or update dual MIT/Apache-2.0 licensing for this project.

All rights attributed to: John Suykerbuyk <john@syketech.com> and SykeTech LTD

## Rules

Every step below MUST be idempotent — running this skill repeatedly produces
the identical result each time. Never duplicate content, never append to
existing correct content.

## Step 1: Create/overwrite the single LICENSE file

Write a single `LICENSE` file in the project root containing, in order:

1. A preamble explaining dual licensing and the user's choice
2. `===` separator + full MIT license text (copyright line below)
3. `===` separator + full Apache 2.0 license text with copyright appendix

Copyright line (use the current year from the system):

    Copyright (c) YYYY John Suykerbuyk and SykeTech LTD

This file is canonical — always overwrite it entirely so the content matches
this template exactly. There should be NO separate `LICENSE-MIT` or
`LICENSE-APACHE` files; delete them if they exist.

## Step 2: Ask about source-file banners

Prompt the user:

> Should I add a small copyright + SPDX banner to the top of every source
> file that doesn't already have one? (y/n)

If the user declines, stop here.

## Step 3: Add banners (idempotent)

### Banner templates by language

**C / C++ / Rust / Go / Java / JS / TS / CSS / SCSS** (line comments):

```
// Copyright (c) YYYY John Suykerbuyk and SykeTech LTD
// SPDX-License-Identifier: MIT OR Apache-2.0
```

**Python / Ruby / Shell / YAML / Toml / Makefile** (hash comments):

```
# Copyright (c) YYYY John Suykerbuyk and SykeTech LTD
# SPDX-License-Identifier: MIT OR Apache-2.0
```

**HTML / XML / SVG** (block comments):

```
<!-- Copyright (c) YYYY John Suykerbuyk and SykeTech LTD -->
<!-- SPDX-License-Identifier: MIT OR Apache-2.0 -->
```

**Lua / SQL** (double-dash):

```
-- Copyright (c) YYYY John Suykerbuyk and SykeTech LTD
-- SPDX-License-Identifier: MIT OR Apache-2.0
```

### File selection

Include: all source files matching typical extensions for the detected language
(.h, .cpp, .c, .rs, .go, .py, .js, .ts, .java, .rb, .sh, .lua, .sql, etc.)

Exclude:

- vendor/, third_party/, node_modules/, target/, build/, dist/
- prototype/, .git/
- Generated files (_.pb.go, _\_generated.\*, etc.)
- Non-source files (LICENSE, README, Makefile unless it has project logic)

### Idempotency rules

1. If a file already starts with the exact banner (matching comment style and
   content), skip it — do not modify the file.
2. If a file has a banner with a **different year** or **different wording**,
   replace the old banner with the current one (same two lines, in place).
3. For files with a shebang (#!) on line 1, place the banner on lines 2-3
   (with a blank line between shebang and banner if not already present).
4. Always leave exactly one blank line between the banner and the first line
   of real code.

## Step 4: Validate

1. Confirm LICENSE exists and contains both license texts.
2. If banners were added, spot-check 2-3 files to confirm correct placement.
3. Build the project (if a build system exists) to confirm nothing broke.
4. Run tests (if they exist) to confirm nothing broke.

## Step 5: Report

Summarize:

- LICENSE file status (created / updated / unchanged)
- Number of files with banners added / updated / already correct
- Build + test results
