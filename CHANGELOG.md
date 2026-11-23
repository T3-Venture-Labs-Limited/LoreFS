# Changelog

All notable changes are listed here. The format follows [Keep a Changelog](https://keepachangelog.com), and versions follow [Semantic Versioning](https://semver.org).

## [Unreleased]

## [0.1.0] - not yet released

The first public release. LoreFS began as a private tool for a two-person team and its agents; this release makes it usable by anyone.

### Added

- `lorefs create` for shared drives (any S3-compatible bucket and a Redis-compatible index) and `lorefs create --local` for trial drives with no account or network.
- `lorefs server init`: a ready-to-run, encrypted Valkey index bundle with its own certificates.
- Version history recorded from the drive's change log, for any program that saves; `lorefs admin changelog` to inspect or switch it.
- `lorefs backup` (setup, run, schedule, status) and `lorefs restore-index` (list, drill, restore, restore from the backup alone).
- Every computer acts as the user who created the drive, so differing user numbers no longer matter.
- Release archives for Linux amd64 and arm64 with a one-line installer, and `lorefs update` from releases.
- `lorefs rules diff`, `lorefs selftest` that names the distance to the index as a cause, optional desktop pieces detected and listed when skipped.
- Documentation, an agent skill, a rules file for agents, and a test script for each area.

### Changed

- Join codes use a new format (`lorefs2_`); codes from the private program still work.
- The command is `lorefs`.
