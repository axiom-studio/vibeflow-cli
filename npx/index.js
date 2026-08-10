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
  const probe = process.platform === 'win32'
    ? `"${systemRootBin('where.exe')}" ${cmd}`
    : `command -v ${cmd}`;
  try { execSync(probe, { stdio: 'ignore' }); return true; } catch { return false; }
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
  let installCmd = null;
  if (process.platform === 'darwin' && haveCmd('brew')) installCmd = 'brew install tmux';
  else if (haveCmd('apt-get')) installCmd = `${sudo}apt-get update && ${sudo}apt-get install -y tmux`;
  else if (haveCmd('dnf')) installCmd = `${sudo}dnf install -y tmux`;
  else if (haveCmd('yum')) installCmd = `${sudo}yum install -y tmux`;
  // Alpine: everything else in the installer works there, including the static
  // binary — only this step was missing a branch.
  else if (haveCmd('apk')) installCmd = `${sudo}apk add --no-cache tmux`;

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
async function verifyChecksum(archivePath, asset, tag, skip) {
  if (skip) {
    warn(`skipping checksum verification for ${asset} (--skip-checksum)`);
    return;
  }
  step('Verifying checksum');

  let sums;
  try {
    sums = parseChecksums(await getText(`https://github.com/${REPO}/releases/download/${tag}/checksums.txt`));
  } catch (e) {
    fs.rmSync(archivePath, { force: true });
    fail(`could not fetch checksums.txt for ${tag}: ${e.message}\n` +
      `       Refusing to run an unverified binary. Re-run with --skip-checksum to override.`);
  }

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
  ok(`sha256 ${got.slice(0, 16)}… matches checksums.txt`);
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
    if (fs.existsSync(p)) return p;
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
function extractArchive(archivePath, destDir, isWindows) {
  const asset = path.basename(archivePath);
  const tarFlag = isWindows ? '-xf' : '-xzf';
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

async function installBinary({ osName, goArch, isWindows }, version, skipChecksum) {
  step('Resolving vibeflow-cli release');
  let tag = version;
  if (!tag) {
    const latest = await getJSON(`https://api.github.com/repos/${REPO}/releases/latest`);
    tag = latest.tag_name;
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
  await verifyChecksum(archivePath, asset, tag, skipChecksum);
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

  const binPath = await installBinary(plat, args.version, args.skipChecksum);
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
    warn(plat.isWindows
      ? 'tmux is not available on Windows, so `vibeflow launch` will not work here — use WSL.'
      : 'tmux is still missing, so `vibeflow launch` will not work until you install it.');
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
module.exports = { systemRootBin, parseChecksums, parseArgs, psLiteral };
