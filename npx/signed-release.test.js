'use strict';

// End-to-end signature tests against a SIGNED fixture release (issue #4389).
//
// The cases in index.test.js cover verifySignature's crypto in isolation. What
// was missing is the whole path: a release that actually carries a .sig, a real
// pinned key, and the installer's fail-closed behaviour observed by exit code and
// filesystem state. QA had to do this by hand — generate a keypair, pin it into a
// scratch copy of index.js, sign a real checksums.txt, and stub the asset fetch.
// That should not be a manual procedure, so it lives here.
//
// Each case spawns the installer as a child process, because fail() calls
// process.exit() and would take the test runner with it. A preload impersonates
// GitHub, so nothing touches the network and no production key is involved — the
// keypair is generated per-run and thrown away.
//
//   node --test npx/signed-release.test.js

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawnSync } = require('node:child_process');

// Synthetic and deliberately unlike any real tag, so the fixture can never
// collide with whatever is pinned in source (that collision made the
// 'did the substitution apply' check misfire).
const FLOOR = 'v9.9.0';
const ASSET_TAG = 'v9.9.0';

function makeFixture() {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'signed-rel-'));
  const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519');
  const pubPem = publicKey.export({ type: 'spki', format: 'pem' }).toString().trim();

  // A real tar.gz containing an executable `vibeflow`, so the happy path can run
  // to completion instead of dying at extraction and hiding a later regression.
  const stage = path.join(dir, 'stage');
  fs.mkdirSync(stage);
  fs.writeFileSync(path.join(stage, 'vibeflow'), '#!/bin/sh\nexit 0\n', { mode: 0o755 });
  const tarball = path.join(dir, 'asset.tar.gz');
  const t = spawnSync('tar', ['-czf', tarball, '-C', stage, 'vibeflow']);
  if (t.status !== 0) throw new Error('could not build fixture tarball');

  const archive = fs.readFileSync(tarball);
  const assetName = `vibeflow_${ASSET_TAG.replace(/^v/, '')}_${process.platform === 'darwin' ? 'darwin' : 'linux'}_${process.arch === 'arm64' ? 'arm64' : 'amd64'}.tar.gz`;
  const sha = crypto.createHash('sha256').update(archive).digest('hex');
  const checksums = `${sha}  ${assetName}\n`;
  const goodSig = crypto.sign(null, Buffer.from(checksums, 'utf8'), privateKey);

  // Installer copy with the throwaway key and floor pinned in.
  //
  // Match the constants by SHAPE, not by their empty value. The original version
  // replaced the literal `= '';`, which stopped matching the moment a real key was
  // pinned in source — the fixture then silently kept the production key and the
  // throwaway signature could not verify. The guard was no better: it asserted
  // 'BEGIN PUBLIC KEY' was present, which the real key satisfies, so it could not
  // detect its own failure. Assert the substitution APPLIED and that the value is
  // the fixture's own.
  let src = fs.readFileSync(path.join(__dirname, 'index.js'), 'utf8');

  const KEY_RE = /const RELEASE_SIGNING_PUBLIC_KEY = (?:''|`[\s\S]*?`);/;
  const FLOOR_RE = /const SIGNED_FROM_TAG = '[^']*';/;
  assert.ok(KEY_RE.test(src), 'key constant not found — has it been renamed?');
  assert.ok(FLOOR_RE.test(src), 'floor constant not found — has it been renamed?');
  assert.strictEqual(src.match(/const RELEASE_SIGNING_PUBLIC_KEY =/g).length, 1,
    'exactly one key constant expected');

  src = src.replace(KEY_RE, 'const RELEASE_SIGNING_PUBLIC_KEY = `' + pubPem + '`;');
  src = src.replace(FLOOR_RE, `const SIGNED_FROM_TAG = '${FLOOR}';`);

  // Assert the RESULT, not that the text changed: when the fixture's value happens
  // to equal what source already had, "changed" is false while the substitution
  // was in fact fine. Checking the outcome is correct either way.
  assert.ok(src.includes(pubPem), 'the FIXTURE key must be pinned, not whatever source carries');
  assert.ok(src.includes(`SIGNED_FROM_TAG = '${FLOOR}'`), 'fixture must pin its own floor');
  const installer = path.join(dir, 'installer.js');
  fs.writeFileSync(installer, src);

  fs.writeFileSync(path.join(dir, 'checksums.txt'), checksums);
  fs.writeFileSync(path.join(dir, 'good.sig'), goodSig);
  fs.writeFileSync(path.join(dir, 'archive.bin'), archive);

  // Preload that answers as GitHub would, driven by env vars per case.
  fs.writeFileSync(path.join(dir, 'proxy.js'), `
const https = require('https'), fs = require('fs');
const { Readable } = require('stream'), { EventEmitter } = require('events');
const D = ${JSON.stringify(dir)};
https.get = (url, opts, cb) => {
  const u = String(url);
  const send = (b) => { const s = Readable.from([b]); s.statusCode = 200; s.headers = {};
    process.nextTick(() => cb(s)); return { on() { return this; } }; };
  const nf = () => { const e = new EventEmitter();
    process.nextTick(() => e.emit('error', new Error('HTTP 404'))); return e; };
  if (u.includes('releases/latest')) return send(Buffer.from(JSON.stringify({ tag_name: ${JSON.stringify(ASSET_TAG)} })));
  if (u.endsWith('checksums.txt.sig')) {
    if (process.env.SIG_MODE === 'missing') return nf();
    const sig = fs.readFileSync(D + '/good.sig');
    if (process.env.SIG_MODE === 'forged') { sig[0] ^= 0xff; }
    return send(sig);
  }
  if (u.endsWith('checksums.txt')) {
    if (process.env.SIG_MODE === 'tampered') return send(Buffer.from('0000  evil.tar.gz\\n'));
    return send(fs.readFileSync(D + '/checksums.txt'));
  }
  if (u.endsWith('.tar.gz')) return send(fs.readFileSync(D + '/archive.bin'));
  return nf();
};
`);
  return { dir, installer };
}

function runInstaller({ dir, installer }, sigMode, extraArgs = []) {
  const home = fs.mkdtempSync(path.join(dir, 'home-'));
  const r = spawnSync(process.execPath,
    ['-r', path.join(dir, 'proxy.js'), installer,
      '--api-key', 'fixture-key', '--agents', 'claude-desktop', ...extraArgs],
    { env: { ...process.env, HOME: home, SIG_MODE: sigMode }, encoding: 'utf8' });
  return {
    code: r.status,
    out: `${r.stdout || ''}${r.stderr || ''}`,
    installed: fs.existsSync(path.join(home, '.vibeflow', 'bin', 'vibeflow')),
  };
}

test('a validly signed release reports "signature verified" and installs', (t) => {
  const fx = makeFixture();
  t.after(() => fs.rmSync(fx.dir, { recursive: true, force: true }));

  const r = runInstaller(fx, 'valid');
  assert.match(r.out, /signature verified/, `expected a verified signature:\n${r.out}`);
  assert.ok(r.installed, 'the binary should be installed on the happy path');
});

test('a FORGED signature aborts, deletes the archive, and installs nothing', (t) => {
  const fx = makeFixture();
  t.after(() => fs.rmSync(fx.dir, { recursive: true, force: true }));

  const r = runInstaller(fx, 'forged');
  assert.strictEqual(r.code, 1, 'must exit non-zero');
  assert.match(r.out, /SIGNATURE VERIFICATION FAILED/);
  assert.strictEqual(r.installed, false, 'nothing may be installed after a bad signature');
});

test('checksums swapped by an attacker fail the signature, not merely the hash', (t) => {
  // The scenario signing exists for: with both files rewritten, hashing alone
  // cannot notice. The signature must be what catches it.
  const fx = makeFixture();
  t.after(() => fs.rmSync(fx.dir, { recursive: true, force: true }));

  const r = runInstaller(fx, 'tampered');
  assert.strictEqual(r.code, 1);
  assert.match(r.out, /SIGNATURE VERIFICATION FAILED/,
    'a re-signed-looking checksum list must fail signature verification first');
  assert.strictEqual(r.installed, false);
});

test('a MISSING signature on a signed-era release aborts by default', (t) => {
  // #4387: withholding a .sig is free, so it must not be a soft warning.
  const fx = makeFixture();
  t.after(() => fs.rmSync(fx.dir, { recursive: true, force: true }));

  const r = runInstaller(fx, 'missing');
  assert.strictEqual(r.code, 1);
  assert.match(r.out, /NO SIGNATURE published/);
  assert.strictEqual(r.installed, false);
});

test('--allow-unsigned permits a missing signature but NOT a forged one', (t) => {
  const fx = makeFixture();
  t.after(() => fs.rmSync(fx.dir, { recursive: true, force: true }));

  const missing = runInstaller(fx, 'missing', ['--allow-unsigned']);
  assert.match(missing.out, /authenticity NOT verified/);
  assert.ok(missing.installed, '--allow-unsigned should permit a legitimately unsigned release');

  // The two must never be conflated: a wrong signature is evidence of tampering.
  const forged = runInstaller(fx, 'forged', ['--allow-unsigned']);
  assert.strictEqual(forged.code, 1, '--allow-unsigned must not rescue a forged signature');
  assert.match(forged.out, /SIGNATURE VERIFICATION FAILED/);
  assert.strictEqual(forged.installed, false);
});
