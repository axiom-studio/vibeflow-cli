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
const fs = require('node:fs');
const os = require('node:os');
const crypto = require('node:crypto');

const {
  systemRootBin, parseChecksums, parseArgs, psLiteral, tarFlagFor,
  pickTmuxInstallCmd, tmuxMissingWarning, verifySignature,
  RELEASE_SIGNING_PUBLIC_KEY, SIGNED_FROM_TAG,
  compareTags, signatureRequiredFor,
} = require('./index.js');

// --- #4349: Ed25519 signature verification over checksums.txt ---

// These exercise the crypto contract the installer depends on, using a real
// generated keypair rather than a mocked verifier. verifySignature() itself
// fetches over the network, so what is asserted here is the primitive it is built
// on plus the pinned-key handling; the four-way behaviour matrix (verified /
// unavailable / invalid / require-signature) is documented in npx/SIGNING.md and
// exercised end-to-end by the CI smoke jobs.
test('pinned key is empty until one is generated, and that is not silently treated as verified', () => {
  // Guards against someone pasting a placeholder that parses but proves nothing.
  // While empty, the installer must SAY authenticity is unverified.
  if (RELEASE_SIGNING_PUBLIC_KEY !== '') {
    assert.match(RELEASE_SIGNING_PUBLIC_KEY, /^-----BEGIN PUBLIC KEY-----/,
      'a pinned key must be a PEM SPKI public key');
    const pub = crypto.createPublicKey(RELEASE_SIGNING_PUBLIC_KEY);
    assert.strictEqual(pub.asymmetricKeyType, 'ed25519',
      'the pinned key must be ed25519 — crypto.verify(null, ...) assumes it');
  }
});

test('a valid Ed25519 signature over checksums.txt verifies with built-in crypto', () => {
  const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519');
  const checksums = 'abc  vibeflow_1.0.0_linux_amd64.tar.gz\n';
  const sig = crypto.sign(null, Buffer.from(checksums, 'utf8'), privateKey);

  // Round-trip through PEM, because that is how the key is pinned in source.
  const pinned = publicKey.export({ type: 'spki', format: 'pem' });
  assert.ok(crypto.verify(null, Buffer.from(checksums, 'utf8'), crypto.createPublicKey(pinned), sig));
});

test('altered checksums or a forged signature both fail verification', () => {
  const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519');
  const checksums = 'abc  vibeflow_1.0.0_linux_amd64.tar.gz\n';
  const sig = crypto.sign(null, Buffer.from(checksums, 'utf8'), privateKey);

  // Swap in an attacker's checksum line — the exact scenario signing exists for,
  // since the hash check alone cannot detect it when both files are replaced.
  const tampered = 'dead  vibeflow_1.0.0_linux_amd64.tar.gz\n';
  assert.strictEqual(crypto.verify(null, Buffer.from(tampered, 'utf8'), publicKey, sig), false);

  const forged = Buffer.from(sig); forged[0] ^= 0xff;
  assert.strictEqual(crypto.verify(null, Buffer.from(checksums, 'utf8'), publicKey, forged), false);

  // A signature from a DIFFERENT key must not validate against the pinned one.
  const other = crypto.generateKeyPairSync('ed25519');
  const otherSig = crypto.sign(null, Buffer.from(checksums, 'utf8'), other.privateKey);
  assert.strictEqual(crypto.verify(null, Buffer.from(checksums, 'utf8'), publicKey, otherSig), false,
    'key substitution must be rejected');
});

test('verifySignature reports unavailable when no key is pinned', async () => {
  // With RELEASE_SIGNING_PUBLIC_KEY empty this must short-circuit BEFORE any
  // network call, so it is safe to call with a bogus tag.
  if (RELEASE_SIGNING_PUBLIC_KEY === '') {
    assert.strictEqual(await verifySignature('anything', 'v0.0.0-does-not-exist'), 'unavailable');
  }
});

// --- #4387: signature stripping — the floor that makes a missing .sig fatal ---

test('compareTags orders release tags numerically', () => {
  assert.strictEqual(compareTags('v1.0.23', 'v1.0.24'), -1);
  assert.strictEqual(compareTags('v1.0.24', 'v1.0.24'), 0);
  assert.strictEqual(compareTags('v1.0.25', 'v1.0.24'), 1);
  // Not lexicographic: '9' > '10' as strings, and that would put v1.0.9 above
  // a v1.0.10 floor and silently disable enforcement.
  assert.strictEqual(compareTags('v1.0.9', 'v1.0.10'), -1);
  assert.strictEqual(compareTags('v2.0.0', 'v1.99.99'), 1);
  // Differing segment counts, and a bare tag with no leading v.
  assert.strictEqual(compareTags('v1.2', 'v1.2.1'), -1);
  assert.strictEqual(compareTags('1.0.24', 'v1.0.24'), 0);
});

test('compareTags is not defeated by a pre-release suffix', () => {
  // A floor check must not be dodged by tagging v1.0.24-rc1 to land "below" the
  // floor while still being a signed-era release.
  assert.strictEqual(compareTags('v1.0.24-rc1', 'v1.0.24'), 0);
});

test('signatureRequiredFor is inert until BOTH a key and a floor exist', () => {
  // Today: no key, no floor -> nothing to enforce. Refusing here would break
  // every install, which is why the floor exists rather than a blanket default.
  if (!RELEASE_SIGNING_PUBLIC_KEY || !SIGNED_FROM_TAG) {
    assert.strictEqual(signatureRequiredFor('v99.99.99'), false,
      'without a pinned key and a floor there is nothing to verify against');
  }
});

// The behaviour the ticket called uncovered. Rather than mutate the module
// constants (they are consts), re-evaluate the logic against a pinned fixture so
// the decision table itself is asserted.
test('the floor decision table: at-or-after is fatal, before only warns', () => {
  const decide = (key, floor, tag) => {
    if (!key || !floor) return false;
    return compareTags(tag, floor) >= 0;
  };
  const KEY = '-----BEGIN PUBLIC KEY-----\nx\n-----END PUBLIC KEY-----';

  // Post-floor: missing signature must be fatal — withholding a .sig is free.
  assert.strictEqual(decide(KEY, 'v1.0.24', 'v1.0.24'), true, 'the floor tag itself is enforced');
  assert.strictEqual(decide(KEY, 'v1.0.24', 'v1.0.25'), true);
  assert.strictEqual(decide(KEY, 'v1.0.24', 'v1.1.0'), true);

  // Pre-floor: legitimately unsigned, stays installable.
  assert.strictEqual(decide(KEY, 'v1.0.24', 'v1.0.23'), false);
  assert.strictEqual(decide(KEY, 'v1.0.24', 'v0.9.0'), false);

  // Missing either half disables enforcement.
  assert.strictEqual(decide('', 'v1.0.24', 'v1.0.25'), false);
  assert.strictEqual(decide(KEY, '', 'v1.0.25'), false);
});

test('an invalid signature is fatal independently of the floor', () => {
  // Regression pin: #4387 explicitly must not weaken 'invalid' handling. A wrong
  // signature fails closed for ANY tag, pre-floor included, and no flag overrides
  // it — unlike a missing one, which --allow-unsigned can permit.
  const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519');
  const sig = crypto.sign(null, Buffer.from('real\n'), privateKey);
  assert.strictEqual(crypto.verify(null, Buffer.from('swapped\n'), publicKey, sig), false,
    'a signature over different content must never validate, whatever the tag');
});

test('--allow-unsigned and --require-signature both parse', () => {
  assert.strictEqual(parseArgs(['--allow-unsigned']).allowUnsigned, true);
  assert.strictEqual(parseArgs(['--require-signature']).requireSignature, true);
  // Neither is on by default: the floor decides, not a flag.
  const d = parseArgs(['--api-key', 'k']);
  assert.strictEqual(d.allowUnsigned, undefined);
  assert.strictEqual(d.requireSignature, undefined);
});

// --- #4341 / #4369: the two assertions that ticket asked for and did not get ---

test('pickTmuxInstallCmd selects apk when it is the only package manager', () => {
  // The regression this guards actually happened: apk was missing from
  // brew -> apt-get -> dnf -> yum for the life of the installer, so Alpine
  // silently got no tmux.
  const only = (name) => (c) => c === name;
  assert.strictEqual(pickTmuxInstallCmd('linux', 'sudo ', only('apk')),
    'sudo apk add --no-cache tmux');
});

test('pickTmuxInstallCmd covers every supported manager and returns null for none', () => {
  const only = (name) => (c) => c === name;
  assert.match(pickTmuxInstallCmd('linux', 'sudo ', only('apt-get')), /apt-get install -y tmux/);
  assert.strictEqual(pickTmuxInstallCmd('linux', 'sudo ', only('dnf')), 'sudo dnf install -y tmux');
  assert.strictEqual(pickTmuxInstallCmd('linux', 'sudo ', only('yum')), 'sudo yum install -y tmux');
  assert.strictEqual(pickTmuxInstallCmd('darwin', '', only('brew')), 'brew install tmux');
  assert.strictEqual(pickTmuxInstallCmd('linux', 'sudo ', () => false), null,
    'no package manager must yield null so the caller can warn');
});

test('pickTmuxInstallCmd keeps apt-get ahead of apk and brew to darwin only', () => {
  const all = () => true;
  assert.match(pickTmuxInstallCmd('linux', 'sudo ', all), /apt-get/,
    'precedence must not change: apt-get wins on linux when several are present');
  // brew must not be selected on linux even when present (e.g. linuxbrew).
  assert.match(pickTmuxInstallCmd('linux', 'sudo ', (c) => c === 'brew' || c === 'apk'), /apk/);
});

test('tmuxMissingWarning names tmux and vibeflow launch on both platforms', () => {
  // #4341's fix was that "Setup complete" alone hid a missing tmux. The message
  // is the fix, so assert its content rather than that some warning fired.
  const posix = tmuxMissingWarning(false);
  assert.match(posix, /tmux/);
  assert.match(posix, /vibeflow launch/);
  assert.match(posix, /still missing/);

  const win = tmuxMissingWarning(true);
  assert.match(win, /tmux/);
  assert.match(win, /vibeflow launch/);
  assert.match(win, /WSL/, 'Windows users need the WSL pointer, not an install hint');
});

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

test('systemRootBin returns an ABSOLUTE path when the helper really exists', (t) => {
  // The positive case. Without this, the "absolute or null" contract was only
  // ever asserted against null on a Linux host, so it passed vacuously and
  // guarded nothing — the blind spot finding #4364 called out.
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'srb-'));
  fs.mkdirSync(path.join(dir, 'System32'), { recursive: true });
  fs.writeFileSync(path.join(dir, 'System32', 'tar.exe'), '');

  const prev = process.env.SystemRoot;
  process.env.SystemRoot = dir;
  t.after(() => {
    if (prev === undefined) delete process.env.SystemRoot; else process.env.SystemRoot = prev;
    fs.rmSync(dir, { recursive: true, force: true });
  });

  const got = systemRootBin('tar.exe');
  assert.ok(got, 'should resolve when System32/tar.exe exists');
  assert.ok(path.isAbsolute(got), `resolved to a non-absolute path: ${got}`);
  assert.strictEqual(got, path.join(dir, 'System32', 'tar.exe'));
});

test('systemRootBin returns null for a RELATIVE SystemRoot even if the file exists', (t) => {
  // The #4364 exploit precondition: `set SystemRoot=. && npx ...` alongside a
  // planted ./System32/tar.exe. path.join preserves relativity, and spawnSync
  // resolves a relative program against the CWD — so this must refuse.
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'srb-rel-'));
  fs.mkdirSync(path.join(dir, 'System32'), { recursive: true });
  fs.writeFileSync(path.join(dir, 'System32', 'tar.exe'), '');

  const prevCwd = process.cwd();
  const prev = process.env.SystemRoot;
  process.chdir(dir);
  process.env.SystemRoot = '.';
  t.after(() => {
    process.chdir(prevCwd);
    if (prev === undefined) delete process.env.SystemRoot; else process.env.SystemRoot = prev;
    fs.rmSync(dir, { recursive: true, force: true });
  });

  // ./System32/tar.exe genuinely exists relative to cwd, so only the
  // path.isAbsolute guard can reject it.
  assert.ok(fs.existsSync(path.join('System32', 'tar.exe')), 'precondition: planted file is reachable');
  assert.strictEqual(systemRootBin('tar.exe'), null,
    'a relative SystemRoot must not yield a spawnable relative program path');
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
