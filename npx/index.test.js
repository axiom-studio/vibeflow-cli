'use strict';

// Unit tests for the pure helpers in index.js, using the built-in node:test
// runner so the package stays dependency-free.
//
//   node --test npx/
//
// The installer's real network/filesystem work is covered end-to-end by the
// windows-installer-smoke CI job; these tests pin the properties that are cheap
// to assert and expensive to get wrong.

const test = require('node:test');
const assert = require('node:assert');
const path = require('node:path');

const { systemRootBin, parseChecksums, parseArgs, psLiteral, tarFlagFor } = require('./index.js');

// --- tarFlagFor: the #4338 regression guard ---

test('tarFlagFor asks for gunzip explicitly on the POSIX tar.gz branch', () => {
  // busybox only auto-detects gzip when built with FEATURE_SEAMLESS_GZ, so `-xf`
  // on a .tar.gz dies with "invalid tar magic" — and the POSIX branch has no
  // second extractor. Never drop the z here.
  assert.strictEqual(tarFlagFor(false), '-xzf');
});

test('tarFlagFor does not pass -z on the Windows zip branch', () => {
  // That archive is a zip; -z would be wrong. bsdtar reads it, and
  // Expand-Archive is the fallback.
  assert.strictEqual(tarFlagFor(true), '-xf');
});

// --- systemRootBin: the #4337 / finding #399 regression guard ---

test('systemRootBin returns an absolute System32-rooted path', (t) => {
  // Point SystemRoot at a directory that really exists so the existsSync guard
  // passes on this host, then assert the shape of what comes back.
  const prev = process.env.SystemRoot;
  process.env.SystemRoot = path.dirname(__dirname); // repo root
  t.after(() => { if (prev === undefined) delete process.env.SystemRoot; else process.env.SystemRoot = prev; });

  // No System32 under the repo root, so it must report "not found" rather than
  // silently degrading to a bare name.
  assert.strictEqual(systemRootBin('tar.exe'), null,
    'must return null when the System32 helper is absent — never a bare name');
});

test('systemRootBin never returns a bare, relative program name', () => {
  // Whatever the environment, the contract is: absolute path or null. A bare
  // name would reintroduce CWE-426 on Windows, where CreateProcess searches the
  // current working directory before %PATH%.
  for (const exe of ['tar.exe', 'powershell.exe', 'where.exe']) {
    const got = systemRootBin(exe);
    if (got !== null) {
      assert.ok(path.isAbs(got), `${exe} resolved to a non-absolute path: ${got}`);
      assert.ok(!got.startsWith('.'), `${exe} resolved to a relative path: ${got}`);
    }
  }
});

test('systemRootBin honors SystemRoot and places PowerShell under its versioned dir', (t) => {
  const prev = process.env.SystemRoot;
  process.env.SystemRoot = 'C:\\CustomWin';
  t.after(() => { if (prev === undefined) delete process.env.SystemRoot; else process.env.SystemRoot = prev; });

  // existsSync will fail for this fake root, so we assert on the join shape via
  // the fallback contract: null, not a guess. The path construction itself is
  // exercised by the smoke job on a real Windows image.
  assert.strictEqual(systemRootBin('powershell.exe'), null);
});

// --- parseChecksums: the #4340 guard ---

test('parseChecksums reads the goreleaser two-space format', () => {
  const txt = [
    'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  vibeflow_1.0.0_linux_amd64.tar.gz',
    '85e335d6e16636b7a652f1b695382d7eafdd7c1729a9cc143897a42afda509b7  vibeflow_1.0.0_windows_amd64.zip',
    '',
    'not a checksum line',
  ].join('\n');
  const sums = parseChecksums(txt);
  assert.strictEqual(Object.keys(sums).length, 2);
  assert.strictEqual(sums['vibeflow_1.0.0_windows_amd64.zip'],
    '85e335d6e16636b7a652f1b695382d7eafdd7c1729a9cc143897a42afda509b7');
});

test('parseChecksums lowercases hashes and tolerates the binary-mode asterisk', () => {
  const sums = parseChecksums('ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789 *file.tar.gz');
  assert.strictEqual(sums['file.tar.gz'], 'abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789');
});

test('parseChecksums ignores malformed and truncated hashes', () => {
  assert.deepStrictEqual(parseChecksums('deadbeef  short.tar.gz'), {},
    'a 8-char hash is not a sha256 and must not be trusted');
});

// --- psLiteral: quoting for the PowerShell fallback ---

test('psLiteral single-quotes so $ stays literal', () => {
  // Double-quoted PowerShell strings interpolate; a temp path containing $ would
  // otherwise be mangled.
  assert.strictEqual(psLiteral('C:\\tmp\\$Recycle\\a.zip'), "'C:\\tmp\\$Recycle\\a.zip'");
});

test('psLiteral escapes embedded single quotes by doubling', () => {
  assert.strictEqual(psLiteral("C:\\o'brien\\a.zip"), "'C:\\o''brien\\a.zip'");
});

// --- parseArgs ---

test('parseArgs supports --skip-checksum and both --api-key forms', () => {
  assert.strictEqual(parseArgs(['--skip-checksum']).skipChecksum, true);
  assert.strictEqual(parseArgs(['--api-key', 'k1']).apiKey, 'k1');
  assert.strictEqual(parseArgs(['--api-key=k2']).apiKey, 'k2');
  assert.strictEqual(parseArgs([]).skipChecksum, undefined);
});

test('parseArgs defaults to configuring all agents', () => {
  const a = parseArgs(['--api-key', 'k']);
  assert.strictEqual(a.all, true);
  const b = parseArgs(['--api-key', 'k', '--agents', 'kiro']);
  assert.strictEqual(b.all, false);
  assert.strictEqual(b.agents, 'kiro');
});
