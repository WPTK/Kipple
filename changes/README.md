# Changelog fragments

Every behavior change (a feature, a fix, a security change, a removal) adds **one small file here** instead of editing
`CHANGELOG.md`. Two branches then never conflict on the changelog. At release the fragments are folded into
`CHANGELOG.md` under the new version heading and deleted (`docs/maintainers/RELEASING.md`, step 4).

## Adding one

Create `changes/<slug>.<kind>.md`:

- `<slug>`: lowercase letters, digits, `.`, `_`, `-`; unique. The branch name without its prefix works
  (`fix/health-selection` -> `health-selection`); use `pr<number>-<words>` if you know the PR number. Entries within a
  kind are listed in slug order (numbers compare as numbers).
- `<kind>`: `added`, `changed`, `deprecated`, `removed`, `fixed` or `security` (the Keep a Changelog 1.1.0 kinds).
- The file is **one paragraph**: the sentence(s) for the entry, no leading `- `, no blank lines, ending with `(#123)`
  when there is an issue or PR to point at. It is written for a person reading the changelog, not for the diff.

```
changes/health-selection.fixed.md
Feed Health: a search or filter that hid feeds you had ticked no longer leaves them selected. (#61)
```

### Style

Short and flat. A reader scanning a release should get each entry in one glance.

- One sentence, two at most. Lead with where (`Reader:`, `Settings:`) and what changed, in the product's words.
- No explaining why it was wrong, no "now", no "so that", no "this means". State the new behavior.
- No filler openers ("This release", "We're excited"), no "improved/enhanced/streamlined", no triplets, no
  contrast pairs ("not X, but Y"), no em dashes, no bold lead-ins.
- No file, package or function names unless the reader types them. Merge entries that say one thing.
- An intro (`_intro.md`) is one plain sentence naming the theme, or none.

```
Before: Settings: the what-if number of days under mark-all-read now says what it does and that nothing is saved.
After:  Settings: the mark-all-read preview says that nothing is saved.
```

A change with two entries of different kinds gets two files. A change nobody would notice (a refactor, a test, CI)
needs none.

## Checking

```
node scripts/changelog.mjs check      # names and contents; also that CHANGELOG.md [Unreleased] is untouched (CI runs this)
node scripts/changelog.mjs preview    # the section the pending fragments would produce
```

## Releasing

```
node scripts/changelog.mjs release 0.3.0-beta.2 [--date 2026-10-01] [--dry-run]
```

Writes `## [0.3.0-beta.2] - date` into `CHANGELOG.md` (kinds in Keep a Changelog order), updates the compare links and
deletes the fragments. An optional `changes/_intro.md` (one paragraph) becomes the release's intro text and is deleted
too. `--dry-run` prints the section without touching anything. `node scripts/changelog.mjs notes 0.3.0-beta.2` prints
an existing version's section for the GitHub Release notes.

`CHANGELOG.md` under `[Unreleased]` holds only a pointer to this directory; `check` fails if anything else is added there.
