#!/usr/bin/env node
'use strict';

/*
 * VibeFlow one-command setup.
 *
 * Replaces the manual Setup-page steps (per-agent `claude mcp add`, tmux
 * install, download vibeflow-cli, run bootstrap) with a single command:
 *
 *   npx @axiom-studio/vibeflow-setup --api-key <API_KEY>
 *
 * What it does, in order:
 *   1. Detect the platform and download the matching vibeflow-cli release binary
 *      (tar.gz on linux/darwin, zip on windows).
 *   2. Install tmux if it is missing (apt / dnf / brew). Skipped on Windows,
 *      where tmux has no port — only `vibeflow launch` needs it, not the MCP
 *      config written in step 3.
 *   3. Run `vibeflow bootstrap --all --api-key <key>` to write the MCP config
 *      for every supported agent (Claude CLI/Desktop, Gemini, Cursor, Codex).
 *
 * Dependency-free on purpose (built-in modules only) so `npx` stays fast.
 */

const os = require('os');
const fs = require('fs');
const path = require('path');
const https = require('https');
const crypto = require('crypto');
const { spawnSync, execSync } = require('child_process');

const REPO = 'axiom-studio/vibeflow-cli';
const DEFAULT_BASE_URL = 'https://cloud.axiomstudio.ai';

// RELEASE_SIGNING_PUBLIC_KEY is the Ed25519 public key that release
// `checksums.txt` files are signed with, pinned here on purpose: a key fetched at
// runtime would come from the same origin as the artifact and inherit exactly the
// weakness signing exists to remove (issue #4349, finding #403).
//
// EMPTY UNTIL A KEY IS GENERATED. While empty, the installer verifies checksums
// as before and says plainly that authenticity is unverified — it does not
// pretend to check a signature. See npx/SIGNING.md for the one-time setup.
//
// Rotation is cheap for `npx` users because npx always fetches the latest
// package, so a new pinned key reaches them on their next run.
const RELEASE_SIGNING_PUBLIC_KEY = '';

// SIGNED_FROM_TAG is the first release tag published WITH a checksums.txt.sig.
//
// It closes signature stripping (issue #4387): an adversary able to rewrite the
// archive and checksums.txt can also simply not serve the .sig, and without a
// floor the installer would shrug and fall back to same-origin checksum trust —
// the exact state signing exists to replace. Forging a signature is hard;
// withholding one is free.
//
// At or after this tag a missing signature is FATAL. Before it, a missing
// signature is expected and only warns, so genuinely older releases stay
// installable and nothing breaks retroactively.
//
// EMPTY UNTIL THE FIRST SIGNED RELEASE. Set it to that tag (e.g. 'v1.0.24') in
// the same change that pins RELEASE_SIGNING_PUBLIC_KEY. See npx/SIGNING.md.
const SIGNED_FROM_TAG = '';

// ---------------------------------------------------------------- output helpers
const c = {
  reset: '\x1b[0m', bold: '\x1b[1m', dim: '\x1b[2m',
  green: '\x1b[32m', yellow: '\x1b[33m', red: '\x1b[31m', cyan: '\x1b[36m',
};
const step = (m) => console.log(`${c.cyan}${c.bold}==>${c.reset} ${m}`);
const ok = (m) => console.log(`${c.green}   ✓${c.reset} ${m}`);
const warn = (m) => console.warn(`${c.yellow}   !${c.reset} ${m}`);
function fail(m) {
  console.error(`${c.red}${c.bold}error:${c.reset} ${m}`);
  process.exit(1);
}

// ---------------------------------------------------------------- arg parsing
function parseArgs(argv) {
  const out = { all: true, baseURL: DEFAULT_BASE_URL };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    const val = () => argv[++i];
    switch (a) {
      case '--api-key': out.apiKey = val(); break;
      case '--base-url': out.baseURL = val(); break;
      case '--agents': out.agents = val(); out.all = false; break;
      case '--all': out.all = true; break;
      case '--version': out.version = val(); break; // pin a vibeflow-cli release
      case '--skip-checksum': out.skipChecksum = true; break;
      case '--require-signature': out.requireSignature = true; break;
      case '--allow-unsigned': out.allowUnsigned = true; break;
      case '-h': case '--help': out.help = true; break;
      default:
        if (a.startsWith('--api-key=')) out.apiKey = a.slice('--api-key='.length);
        else if (a.startsWith('--base-url=')) out.baseURL = a.slice('--base-url='.length);
        else warn(`ignoring unknown argument: ${a}`);
    }
  }
  return out;
}

function usage() {
  console.log(`VibeFlow setup

Usage:
  npx @axiom-studio/vibeflow-setup --api-key <API_KEY> [options]

Options:
  --api-key <key>    VibeFlow API key (required; from Account > API Keys)
  --base-url <url>   VibeFlow base URL (default: ${DEFAULT_BASE_URL})
  --agents <csv>     Only configure these agents (default: all)
  --all              Configure all supported agents (default)
  --version <tag>    Pin a specific vibeflow-cli release (default: latest)
  --skip-checksum    Skip SHA-256 verification of the download (not recommended)
  --require-signature  Fail unless the release checksums carry a valid signature
  --allow-unsigned   Permit a signed-era release that publishes no signature (unsafe)
`);
}

// ---------------------------------------------------------------- platform
function detectPlatform() {
  const osName = { linux: 'linux', darwin: 'darwin', win32: 'windows' }[process.platform];
  const goArch = { x64: 'amd64', arm64: 'arm64' }[process.arch];
  if (!osName || !goArch) {
    fail(`unsupported platform: ${process.platform}/${process.arch}`);
  }
  return { osName, goArch, isWindows: process.platform === 'win32' };
}

// ---------------------------------------------------------------- https helpers
function httpsGet(url, opts = {}) {
  return new Promise((resolve, reject) => {
    const req = https.get(url, { headers: { 'User-Agent': 'vibeflow-setup', ...(opts.headers || {}) } }, (res) => {
      // follow redirects (GitHub release assets redirect to a CDN)
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        res.resume();
        return resolve(httpsGet(res.headers.location, opts));
      }
      if (res.statusCode !== 200) {
        res.resume();
        // The GitHub API allows 60 unauthenticated requests per hour per IP, so
        // a shared/NAT/CI address hits 403 well before anything is wrong with
        // the install. Say so, instead of leaving a bare status code.
        const rateLimited = (res.statusCode === 403 || res.statusCode === 429) && url.startsWith('https://api.github.com/');
        const hint = rateLimited
          ? ' (GitHub API rate limit — retry later, or pin a release with --version <tag>)'
          : '';
        return reject(new Error(`GET ${url} -> HTTP ${res.statusCode}${hint}`));
      }
      resolve(res);
    });
    req.on('error', reject);
  });
}

async function getText(url) {
  const res = await httpsGet(url);
  const chunks = [];
  for await (const ch of res) chunks.push(ch);
  return Buffer.concat(chunks).toString('utf8');
}

async function getJSON(url) {
  const res = await httpsGet(url, { headers: { Accept: 'application/vnd.github+json' } });
  const chunks = [];
  for await (const ch of res) chunks.push(ch);
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

async function downloadTo(url, dest) {
  const res = await httpsGet(url);
  await new Promise((resolve, reject) => {
    const f = fs.createWriteStream(dest);
    res.pipe(f);
    f.on('finish', () => f.close(resolve));
    f.on('error', reject);
  });
}

// ---------------------------------------------------------------- tmux
function haveCmd(cmd) {
  // cmd.exe has no `command` builtin; `where` is the Windows equivalent. Invoke
  // it by absolute path — see systemRootBin() for why a bare name is unsafe here.
  let probe;
  if (process.platform === 'win32') {
    const where = systemRootBin('where.exe');
    // Be explicit rather than interpolating null into the shell string, where it
    // would become the literal `"null" tmux`. Unresolvable probe => unknown =>
    // report absent.
    if (!where) return false;
    probe = `"${where}" ${cmd}`;
  } else {
    probe = `command -v ${cmd}`;
  }
  try { execSync(probe, { stdio: 'ignore' }); return true; } catch { return false; }
}

// pickTmuxInstallCmd chooses the package-manager command, or null when none is
// available. Pure (the `has` probe is injected) so the selection order is
// testable — `apk` was absent from this chain for the life of the installer,
// which is exactly the kind of omission a unit test catches. (issues #4341, #4369.)
function pickTmuxInstallCmd(platform, sudo, has) {
  if (platform === 'darwin' && has('brew')) return 'brew install tmux';
  if (has('apt-get')) return `${sudo}apt-get update && ${sudo}apt-get install -y tmux`;
  if (has('dnf')) return `${sudo}dnf install -y tmux`;
  if (has('yum')) return `${sudo}yum install -y tmux`;
  // Alpine: everything else in the installer works there, including the static
  // binary — only this step was missing a branch.
  if (has('apk')) return `${sudo}apk add --no-cache tmux`;
  return null;
}

// tmuxMissingWarning is the closing line shown when tmux is not present. Kept
// separate so a test can assert the message names tmux and `vibeflow launch`
// rather than trusting that an unqualified "Setup complete" was qualified.
function tmuxMissingWarning(isWindows) {
  return isWindows
    ? 'tmux is not available on Windows, so `vibeflow launch` will not work here — use WSL.'
    : 'tmux is still missing, so `vibeflow launch` will not work until you install it.';
}

// ensureTmux returns true when tmux is present on the system afterwards. main()
// uses that to qualify the closing message: reporting an unconditional "Setup
// complete" while tmux is missing pushes the failure out to the user's first
// `vibeflow launch`, far from its cause. (issue #4341.)
function ensureTmux(isWindows) {
  step('Checking for tmux');
  if (isWindows) {
    // tmux has no Windows port, and only `vibeflow launch` needs it — the MCP
    // config that `vibeflow bootstrap` writes does not.
    warn('tmux is not available on Windows. MCP config is still written, so Claude Desktop');
    warn('and the other agents work; `vibeflow launch` needs tmux, so run it under WSL.');
    return false;
  }
  if (haveCmd('tmux')) { ok('tmux already installed'); return true; }

  const sudo = process.getuid && process.getuid() === 0 ? '' : 'sudo ';
  const installCmd = pickTmuxInstallCmd(process.platform, sudo, haveCmd);

  if (!installCmd) {
    warn('could not auto-install tmux (no known package manager). Install it manually, then re-run.');
    return false;
  }
  step(`Installing tmux: ${installCmd}`);
  const r = spawnSync('bash', ['-lc', installCmd], { stdio: 'inherit' });
  if (r.status !== 0 || !haveCmd('tmux')) {
    warn('tmux install may have failed; check the output above.');
    return haveCmd('tmux');
  }
  ok('tmux installed');
  return true;
}

// ---------------------------------------------------------------- integrity

function sha256File(file) {
  const h = crypto.createHash('sha256');
  h.update(fs.readFileSync(file));
  return h.digest('hex');
}

// parseChecksums maps filename -> sha256 from a goreleaser checksums.txt, whose
// lines are "<hex>  <filename>".
function parseChecksums(text) {
  const out = {};
  for (const line of text.split('\n')) {
    const m = line.trim().match(/^([0-9a-fA-F]{64})\s+\*?(.+)$/);
    if (m) out[m[2].trim()] = m[1].toLowerCase();
  }
  return out;
}

// verifyChecksum checks the downloaded archive against the release's published
// checksums.txt. This runs BEFORE extraction, because the extracted binary is
// chmod 0755'd and then executed — an unverified artifact here is arbitrary code
// execution. Fails closed: any doubt removes the download and exits non-zero.
//
// The release workflow already holds itself to this bar, verifying the Go
// toolchain tarball against go.dev's published SHA256 before extracting it
// (.github/workflows/release.yml). This is the client-side counterpart.
//
// SCOPE — integrity, not authenticity (finding #403, issue #4349). checksums.txt
// comes from the same origin and channel as the archive, so an adversary able to
// alter one can alter the other: a TLS-intercepting proxy trusted by the user's
// own store, or a release-asset compromise, defeats this. What it does close is
// corruption and tampering with the archive alone. Closing the rest needs a
// signature over checksums.txt verified against a key PINNED here — never
// fetched, since a fetched key inherits the same weakness.
// verifySignature checks an Ed25519 signature over the raw checksums.txt bytes
// using the pinned public key. Returns:
//   'verified'    — signature present and valid
//   'unavailable' — no pinned key configured, or no .sig published for this tag
//   'invalid'     — signature present and WRONG (caller must fail closed)
//
// Raw Ed25519 rather than the minisign container format: Node's built-in crypto
// verifies it in a few lines with no parsing of comment lines, key IDs or
// prehash-mode bytes, and no dependency on the user's machine. `openssl pkeyutl
// -sign -rawin` on the release runner produces exactly this.
// compareTags orders release tags numerically: -1 if a < b, 0 if equal, 1 if
// a > b. Tolerates a leading `v` and differing segment counts (v1.2 < v1.2.1).
// Non-numeric suffixes (pre-releases) are ignored for ordering, which is
// deliberate: a floor check must not be defeated by a `-rc1` suffix.
function compareTags(a, b) {
  const parts = (t) => String(t).replace(/^v/, '').split('.')
    .map((s) => parseInt(s, 10)).map((n) => (Number.isFinite(n) ? n : 0));
  const x = parts(a), y = parts(b);
  for (let i = 0; i < Math.max(x.length, y.length); i++) {
    const d = (x[i] || 0) - (y[i] || 0);
    if (d !== 0) return d < 0 ? -1 : 1;
  }
  return 0;
}

// isValidReleaseTag matches the tag shape we publish. An API-supplied tag that
// does not match is not a release we made, and must not be fed to compareTags —
// parseInt('X') is NaN, mapped to 0, so an unparseable tag would sort below every
// floor and silently disable enforcement. (issue #4391.)
function isValidReleaseTag(tag) {
  return /^v?\d+(\.\d+)*(-[A-Za-z0-9.]+)?$/.test(String(tag));
}

// signatureRequiredFor reports whether a MISSING signature must be fatal.
//
// The relaxation is keyed on PROVENANCE, not on the tag's value. The floor in
// #4387 was decided from `tag`, but with no --version that value comes from the
// releases/latest API — i.e. from the adversary this control exists for. Serving
// `{"tag_name":"v0.0.1"}` put every install below the floor and degraded it to
// checksum-only. Forging a signature is hard; withholding one is free; and
// choosing the tag was also free.
//
// So: once a key is pinned, a missing signature is fatal for ANY tag that arrived
// over the network. The pre-floor exception applies only when the OPERATOR pinned
// an old tag with --version — a decision made locally, never handed over the
// wire. (issue #4391.)
function signatureRequiredFor(tag, tagWasPinnedByOperator) {
  if (!RELEASE_SIGNING_PUBLIC_KEY) return false;
  if (!tagWasPinnedByOperator) return true;
  return !SIGNED_FROM_TAG || compareTags(tag, SIGNED_FROM_TAG) >= 0;
}

async function verifySignature(checksumsText, tag) {
  if (!RELEASE_SIGNING_PUBLIC_KEY) return 'unavailable';

  let sig;
  try {
    const res = await httpsGet(`https://github.com/${REPO}/releases/download/${tag}/checksums.txt.sig`);
    const chunks = [];
    for await (const ch of res) chunks.push(ch);
    sig = Buffer.concat(chunks);
  } catch {
    // No signature asset — releases published before signing was introduced.
    return 'unavailable';
  }

  try {
    const pub = crypto.createPublicKey(RELEASE_SIGNING_PUBLIC_KEY);
    return crypto.verify(null, Buffer.from(checksumsText, 'utf8'), pub, sig) ? 'verified' : 'invalid';
  } catch {
    // A malformed signature or key is indistinguishable from a bad one here, and
    // must not be treated as merely "unavailable".
    return 'invalid';
  }
}

async function verifyChecksum(archivePath, asset, tag, skip, requireSignature, allowUnsigned, tagWasPinnedByOperator) {
  if (skip) {
    // This flag disables the only integrity control standing between a network
    // download and `chmod 0755` + execution, so state the consequence rather
    // than just naming the flag.
    warn(`--skip-checksum: NOT verifying ${asset}.`);
    warn('The downloaded binary will be executed without any integrity check.');
    return;
  }
  step('Verifying checksum');

  let checksumsText;
  try {
    checksumsText = await getText(`https://github.com/${REPO}/releases/download/${tag}/checksums.txt`);
  } catch (e) {
    fs.rmSync(archivePath, { force: true });
    fail(`could not fetch checksums.txt for ${tag}: ${e.message}\n` +
      `       Refusing to run an unverified binary. Re-run with --skip-checksum to override.`);
  }

  // Authenticate checksums.txt BEFORE trusting any hash inside it. Without this,
  // the hashes only prove the archive matches what the same server described.
  const sigState = await verifySignature(checksumsText, tag);
  if (sigState === 'invalid') {
    // Never negotiable: a present-but-wrong signature means someone altered
    // either the checksums or the signature. Fail closed regardless of flags.
    fs.rmSync(archivePath, { force: true });
    fail(`SIGNATURE VERIFICATION FAILED for ${tag}'s checksums.txt.\n` +
      `       The signature does not match the pinned release key, so the checksum\n` +
      `       list cannot be trusted and neither can the download.\n` +
      `       The archive has been deleted. Do not run it. Report this.`);
  }
  if (sigState === 'unavailable') {
    const why = RELEASE_SIGNING_PUBLIC_KEY
      ? `${tag} publishes no checksums.txt.sig`
      : 'no release signing key is pinned in this installer yet';

    // A missing signature is fatal by DEFAULT for any tag at or after the
    // signing floor. Withholding a .sig costs an attacker nothing, so this must
    // not be an opt-in protection — an opt-in control does not defend the
    // population being targeted. (issue #4387.)
    if (signatureRequiredFor(tag, tagWasPinnedByOperator) && !allowUnsigned) {
      fs.rmSync(archivePath, { force: true });
      fail(`NO SIGNATURE published for ${tag}, but releases from ${SIGNED_FROM_TAG} onward are signed.\n` +
        `       An absent signature is how a tampered download avoids being caught, so this\n` +
        `       is treated exactly like a bad one. The archive has been deleted.\n` +
        `       If you are certain this release is legitimately unsigned, re-run with\n` +
        `       --allow-unsigned. Otherwise report it.`);
    }
    // Pre-floor, or no key pinned yet: nothing to enforce. --require-signature
    // lets a caller demand strictness anyway.
    if (requireSignature) {
      fs.rmSync(archivePath, { force: true });
      fail(`--require-signature was given but ${why}.\n` +
        `       Refusing to continue on checksum alone.`);
    }
    warn(`authenticity NOT verified: ${why}.`);
    warn('Continuing with checksum only — that detects corruption and tampering');
    warn('with the archive, but does not prove it came from us.');
  }

  const sums = parseChecksums(checksumsText);

  const want = sums[asset];
  if (!want) {
    fs.rmSync(archivePath, { force: true });
    fail(`${asset} is not listed in checksums.txt for ${tag}\n` +
      `       Refusing to run an unverified binary. Re-run with --skip-checksum to override.`);
  }

  const got = sha256File(archivePath);
  if (got !== want) {
    fs.rmSync(archivePath, { force: true });
    fail(`checksum mismatch for ${asset} — the download does not match the published release.\n` +
      `       expected ${want}\n` +
      `       actual   ${got}\n` +
      `       The archive has been deleted. Do not run it. This can mean a corrupted\n` +
      `       download or a tampered artifact; retry, and if it repeats, report it.`);
  }
  if (sigState === 'verified') {
    ok(`sha256 ${got.slice(0, 16)}… matches checksums.txt (signature verified)`);
  } else {
    ok(`sha256 ${got.slice(0, 16)}… matches checksums.txt`);
  }
}

// ---------------------------------------------------------------- binary

// psLiteral single-quotes a path for PowerShell. Single-quoted PowerShell
// strings do not interpolate, so a `$` in a path stays literal; embedded single
// quotes are escaped by doubling.
function psLiteral(s) {
  return `'${s.replace(/'/g, "''")}'`;
}

// systemRootBin resolves a Windows system binary to an absolute path.
//
// Windows must never be handed a bare program name: libuv spawns via
// CreateProcess, which searches the CURRENT WORKING DIRECTORY before %PATH%.
// A `tar.exe` / `powershell.exe` / `where.exe` planted in whatever directory the
// user happened to run `npx` from would therefore execute instead of the system
// one — and it would run while a live API key is on its way to
// `vibeflow bootstrap`. POSIX is immune because execvp consults PATH only, which
// is why this asymmetry is easy to miss. (CWE-426; finding #399, issue #4337.)
//
// `shell: true` is deliberately NOT the fix — it widens the surface instead.
function systemRootBin(exe) {
  const root = process.env.SystemRoot || process.env.windir || 'C:\\Windows';
  const candidates = exe === 'powershell.exe'
    ? [path.join(root, 'System32', 'WindowsPowerShell', 'v1.0', 'powershell.exe')]
    : [path.join(root, 'System32', exe)];
  for (const p of candidates) {
    // path.join preserves relativity, so a relative SystemRoot (e.g. ".") would
    // otherwise yield a relative "system path" — and spawnSync resolves a
    // relative program against the CWD, reinstating the very hijack this
    // function exists to prevent. Enforce the documented contract instead of
    // only stating it. Returning null is the safe outcome: every call site
    // already handles it (tar -> PowerShell -> specific fail). (issue #4364.)
    if (path.isAbsolute(p) && fs.existsSync(p)) return p;
  }
  return null;
}

// extractArchive unpacks a release archive. goreleaser ships tar.gz for
// linux/darwin and zip for windows.
//
// Compression flags are NOT interchangeable, so each branch is explicit:
//   * tar.gz -> `-xzf`. `-xf` alone relies on the local tar auto-detecting gzip.
//     GNU tar, bsdtar and alpine's busybox do; a busybox built WITHOUT
//     FEATURE_SEAMLESS_GZ (e.g. Debian/Ubuntu busybox-static 1.36.1) does not —
//     it fails with `invalid tar magic`, and the POSIX branch has no second
//     extractor, so the install dies. (issue #4338.)
//   * zip -> `-xf`. `-z` would be wrong; only bsdtar reads zip at all, so
//     Expand-Archive is a load-bearing fallback here, not a pre-1803 shim
//     (GNU tar 1.35 was verified to fail on our release zip).
// tarFlagFor picks the extraction flag from the archive format. Extracted so the
// mapping is unit-testable and cannot silently revert — dropping the `z` on the
// POSIX branch is what caused issue #4338.
function tarFlagFor(isWindows) {
  return isWindows ? '-xf' : '-xzf';
}

function extractArchive(archivePath, destDir, isWindows) {
  const asset = path.basename(archivePath);
  const tarFlag = tarFlagFor(isWindows);
  const tarBin = isWindows ? systemRootBin('tar.exe') : 'tar';

  let tar = { error: new Error('tar.exe not found under %SystemRoot%\\System32') };
  if (tarBin) {
    tar = spawnSync(tarBin, [tarFlag, archivePath, '-C', destDir], { stdio: 'ignore' });
    if (!tar.error && tar.status === 0) return;
  }

  if (isWindows) {
    const psBin = systemRootBin('powershell.exe');
    if (psBin) {
      const ps = spawnSync(psBin, [
        '-NoProfile', '-NonInteractive', '-Command',
        `Expand-Archive -LiteralPath ${psLiteral(archivePath)} -DestinationPath ${psLiteral(destDir)} -Force`,
      ], { stdio: 'ignore' });
      if (!ps.error && ps.status === 0) return;
    }
    fail(`could not extract ${asset} with either tar.exe or Expand-Archive under %SystemRoot%\\System32`);
  }
  fail(`could not extract ${asset}: ${tar.error ? tar.error.message : `tar exited ${tar.status}`}`);
}

async function installBinary({ osName, goArch, isWindows }, version, skipChecksum, requireSignature, allowUnsigned) {
  step('Resolving vibeflow-cli release');
  // Provenance, not value: only an operator-pinned tag may relax enforcement.
  const tagWasPinnedByOperator = Boolean(version);
  let tag = version;
  if (!tag) {
    const latest = await getJSON(`https://api.github.com/repos/${REPO}/releases/latest`);
    tag = latest.tag_name;
    // This value arrives over the network and is used to pick the asset URL AND
    // (before #4391) to decide signature enforcement. Reject anything that is not
    // a tag shape we publish rather than letting it reach compareTags, where an
    // unparseable value floors to 0. (issue #4391.)
    if (tag && !isValidReleaseTag(tag)) {
      fail(`refusing an implausible release tag from the GitHub API: ${JSON.stringify(String(tag))}\n` +
        `       Expected something like v1.2.3. Pin a known release with --version <tag>.`);
    }
    // A 200 response with no tag_name (error body, or a repo with no published
    // release) would otherwise surface as `TypeError: Cannot read properties of
    // undefined (reading 'replace')` on the line below.
    if (!tag) {
      fail(
        `could not determine the latest ${REPO} release${latest.message ? `: ${latest.message}` : ' (no tag_name in the API response)'}\n` +
          `       Retry later, or pin a known release with --version <tag>.`,
      );
    }
  }
  const ver = tag.replace(/^v/, '');
  ok(`version ${tag}`);

  // goreleaser's format_overrides ships a zip for windows, tar.gz elsewhere.
  const binName = isWindows ? 'vibeflow.exe' : 'vibeflow';
  const asset = `vibeflow_${ver}_${osName}_${goArch}.${isWindows ? 'zip' : 'tar.gz'}`;
  const url = `https://github.com/${REPO}/releases/download/${tag}/${asset}`;
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'vibeflow-'));
  const archivePath = path.join(tmpDir, asset);

  step(`Downloading ${asset}`);
  await downloadTo(url, archivePath);
  await verifyChecksum(archivePath, asset, tag, skipChecksum, requireSignature, allowUnsigned, tagWasPinnedByOperator);
  extractArchive(archivePath, tmpDir, isWindows);

  const extracted = path.join(tmpDir, binName);
  if (!fs.existsSync(extracted)) fail(`${binName} not found in ${asset} after extraction`);

  const binDir = path.join(os.homedir(), '.vibeflow', 'bin');
  fs.mkdirSync(binDir, { recursive: true });
  const binPath = path.join(binDir, binName);
  fs.copyFileSync(extracted, binPath);
  // Windows derives executability from the file extension, not a mode bit.
  if (!isWindows) fs.chmodSync(binPath, 0o755);
  ok(`installed to ${binPath}`);
  return binPath;
}

// ---------------------------------------------------------------- main
async function main() {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) { usage(); return; }
  if (!args.apiKey) { usage(); fail('--api-key is required'); }

  console.log(`${c.bold}VibeFlow setup${c.reset}\n`);
  const plat = detectPlatform();

  const binPath = await installBinary(plat, args.version, args.skipChecksum, args.requireSignature, args.allowUnsigned);
  const haveTmux = ensureTmux(plat.isWindows);

  step('Configuring MCP for your agents (vibeflow bootstrap)');
  const bootstrapArgs = ['bootstrap', '--api-key', args.apiKey, '--base-url', args.baseURL];
  if (args.all) bootstrapArgs.push('--all');
  else if (args.agents) bootstrapArgs.push('--agents', args.agents);
  const r = spawnSync(binPath, bootstrapArgs, { stdio: 'inherit' });
  if (r.status !== 0) fail('vibeflow bootstrap failed (see output above)');

  console.log('');
  ok(`${c.bold}Setup complete.${c.reset}`);
  const binDirHint = plat.isWindows ? '%USERPROFILE%\\.vibeflow\\bin' : '~/.vibeflow/bin';
  console.log(`${c.dim}   The vibeflow binary is at ${binPath} — add ${binDirHint} to your PATH to use it directly.${c.reset}`);
  // The MCP config references ${MCP_TOKEN}; vibeflow injects it at launch, so a
  // hand-started agent needs the variable exported or it reports the server as
  // unconfigured. Showing the bare command here reads as a failed setup.
  console.log(`${c.dim}   Verify with: MCP_TOKEN=<your-api-key> claude mcp list   (should show vibeflow ... ✓ Connected)${c.reset}`);
  console.log(`${c.dim}   Agents started by 'vibeflow launch' get MCP_TOKEN automatically — only hand-started ones need it.${c.reset}`);
  if (!haveTmux) {
    warn(tmuxMissingWarning(plat.isWindows));
  }
}

// Only run when invoked as a program, so the pure helpers below can be unit
// tested. `npx @axiom-studio/vibeflow-setup` and `node npx/index.js` both take
// this branch; `require()` from a test does not.
if (require.main === module) {
  main().catch((e) => fail(e.message || String(e)));
}

// Exported for tests only. package.json `files` ships index.js alone, so the
// test file is not published.
module.exports = {
  systemRootBin, parseChecksums, parseArgs, psLiteral, tarFlagFor,
  pickTmuxInstallCmd, tmuxMissingWarning, verifySignature,
  RELEASE_SIGNING_PUBLIC_KEY, SIGNED_FROM_TAG,
  compareTags, signatureRequiredFor, isValidReleaseTag,
};
