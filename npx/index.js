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
  // cmd.exe has no `command` builtin; `where` is the Windows equivalent.
  const probe = process.platform === 'win32' ? `where ${cmd}` : `command -v ${cmd}`;
  try { execSync(probe, { stdio: 'ignore' }); return true; } catch { return false; }
}

function ensureTmux(isWindows) {
  step('Checking for tmux');
  if (isWindows) {
    // tmux has no Windows port, and only `vibeflow launch` needs it — the MCP
    // config that `vibeflow bootstrap` writes does not.
    warn('tmux is not available on Windows. MCP config is still written, so Claude Desktop');
    warn('and the other agents work; `vibeflow launch` needs tmux, so run it under WSL.');
    return;
  }
  if (haveCmd('tmux')) { ok('tmux already installed'); return; }

  const sudo = process.getuid && process.getuid() === 0 ? '' : 'sudo ';
  let installCmd = null;
  if (process.platform === 'darwin' && haveCmd('brew')) installCmd = 'brew install tmux';
  else if (haveCmd('apt-get')) installCmd = `${sudo}apt-get update && ${sudo}apt-get install -y tmux`;
  else if (haveCmd('dnf')) installCmd = `${sudo}dnf install -y tmux`;
  else if (haveCmd('yum')) installCmd = `${sudo}yum install -y tmux`;

  if (!installCmd) {
    warn('could not auto-install tmux (no known package manager). Install it manually, then re-run.');
    return;
  }
  step(`Installing tmux: ${installCmd}`);
  const r = spawnSync('bash', ['-lc', installCmd], { stdio: 'inherit' });
  if (r.status !== 0 || !haveCmd('tmux')) warn('tmux install may have failed; check the output above.');
  else ok('tmux installed');
}

// ---------------------------------------------------------------- binary

// psLiteral single-quotes a path for PowerShell. Single-quoted PowerShell
// strings do not interpolate, so a `$` in a path stays literal; embedded single
// quotes are escaped by doubling.
function psLiteral(s) {
  return `'${s.replace(/'/g, "''")}'`;
}

// extractArchive unpacks a release archive. goreleaser ships tar.gz for
// linux/darwin and zip for windows.
//
// `tar -xf` reads tar.gz everywhere. For zip it depends on the flavor: the
// tar.exe bundled with Windows 10 1803+ is bsdtar, which reads zip, but GNU tar
// (e.g. a Git Bash tar earlier on PATH) does not — verified: GNU tar 1.35 fails
// on our release zip. So on Windows the Expand-Archive fallback is load-bearing,
// not just a shim for pre-1803 builds.
function extractArchive(archivePath, destDir, isWindows) {
  const asset = path.basename(archivePath);
  const tar = spawnSync('tar', ['-xf', archivePath, '-C', destDir], { stdio: 'ignore' });
  if (!tar.error && tar.status === 0) return;

  if (isWindows) {
    const ps = spawnSync('powershell', [
      '-NoProfile', '-NonInteractive', '-Command',
      `Expand-Archive -LiteralPath ${psLiteral(archivePath)} -DestinationPath ${psLiteral(destDir)} -Force`,
    ], { stdio: 'ignore' });
    if (!ps.error && ps.status === 0) return;
    fail(`could not extract ${asset} with either tar or Expand-Archive`);
  }
  fail(`could not extract ${asset}: ${tar.error ? tar.error.message : `tar exited ${tar.status}`}`);
}

async function installBinary({ osName, goArch, isWindows }, version) {
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

  const binPath = await installBinary(plat, args.version);
  ensureTmux(plat.isWindows);

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
  console.log(`${c.dim}   Verify with: claude mcp list  (should show vibeflow ... ✓ Connected)${c.reset}`);
  if (plat.isWindows) {
    console.log(`${c.dim}   Session launching (vibeflow launch) needs tmux — run it under WSL.${c.reset}`);
  }
}

main().catch((e) => fail(e.message || String(e)));
