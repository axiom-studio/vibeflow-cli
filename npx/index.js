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
 *   1. Detect the platform and download the matching vibeflow-cli release binary.
 *   2. Install tmux if it is missing (apt / dnf / brew).
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
        return reject(new Error(`GET ${url} -> HTTP ${res.statusCode}`));
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
  try { execSync(`command -v ${cmd}`, { stdio: 'ignore' }); return true; } catch { return false; }
}

function ensureTmux() {
  step('Checking for tmux');
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
async function installBinary({ osName, goArch, isWindows }, version) {
  if (isWindows) {
    fail('Windows is not supported by this installer yet. Use the manual steps on the Setup page.');
  }

  step('Resolving vibeflow-cli release');
  let tag = version;
  if (!tag) {
    const latest = await getJSON(`https://api.github.com/repos/${REPO}/releases/latest`);
    tag = latest.tag_name;
  }
  const ver = tag.replace(/^v/, '');
  ok(`version ${tag}`);

  const asset = `vibeflow_${ver}_${osName}_${goArch}.tar.gz`;
  const url = `https://github.com/${REPO}/releases/download/${tag}/${asset}`;
  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'vibeflow-'));
  const tarPath = path.join(tmpDir, asset);

  step(`Downloading ${asset}`);
  await downloadTo(url, tarPath);
  spawnSync('tar', ['-xzf', tarPath, '-C', tmpDir], { stdio: 'ignore' });

  const extracted = path.join(tmpDir, 'vibeflow');
  if (!fs.existsSync(extracted)) fail(`binary not found in ${asset} after extraction`);

  const binDir = path.join(os.homedir(), '.vibeflow', 'bin');
  fs.mkdirSync(binDir, { recursive: true });
  const binPath = path.join(binDir, 'vibeflow');
  fs.copyFileSync(extracted, binPath);
  fs.chmodSync(binPath, 0o755);
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
  ensureTmux();

  step('Configuring MCP for your agents (vibeflow bootstrap)');
  const bootstrapArgs = ['bootstrap', '--api-key', args.apiKey, '--base-url', args.baseURL];
  if (args.all) bootstrapArgs.push('--all');
  else if (args.agents) bootstrapArgs.push('--agents', args.agents);
  const r = spawnSync(binPath, bootstrapArgs, { stdio: 'inherit' });
  if (r.status !== 0) fail('vibeflow bootstrap failed (see output above)');

  console.log('');
  ok(`${c.bold}Setup complete.${c.reset}`);
  console.log(`${c.dim}   The vibeflow binary is at ${binPath} — add ~/.vibeflow/bin to your PATH to use it directly.${c.reset}`);
  console.log(`${c.dim}   Verify with: claude mcp list  (should show vibeflow ... ✓ Connected)${c.reset}`);
}

main().catch((e) => fail(e.message || String(e)));
