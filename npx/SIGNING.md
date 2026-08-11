# Release signing (Ed25519)

The installer verifies release integrity two ways:

1. **SHA-256 against `checksums.txt`** — always. Detects corruption and tampering
   with the archive.
2. **Ed25519 signature over `checksums.txt`** — proves the checksum list came from
   us. Without this, `checksums.txt` is fetched from the same origin as the
   archive, so anyone able to replace one can replace both (issue #4349).

Step 2 is **inert until a key is generated and pinned.** The installer says so
plainly rather than implying a check it is not performing.

## One-time setup

Requires OpenSSL 1.1.1+ (3.x on current runners). Run this on a trusted machine —
**not** in CI, and not in this repo's working tree.

```bash
# 1. Generate the keypair.
openssl genpkey -algorithm ed25519 -out vibeflow-release-signing.pem
chmod 600 vibeflow-release-signing.pem

# 2. Derive the public half.
openssl pkey -in vibeflow-release-signing.pem -pubout
```

That prints something like:

```
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAlDnrnwAl5cApoLfwm9nO2mrFh6dDk7kAEhHYBNdLDSs=
-----END PUBLIC KEY-----
```

### 3. Pin the public key

Paste it into `RELEASE_SIGNING_PUBLIC_KEY` in `npx/index.js`, newlines included:

```js
const RELEASE_SIGNING_PUBLIC_KEY = `-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAlDnrnwAl5cApoLfwm9nO2mrFh6dDk7kAEhHYBNdLDSs=
-----END PUBLIC KEY-----`;
```

It is pinned, never fetched at runtime. A key downloaded alongside the artifact
would inherit exactly the weakness signing removes.

### 3b. After the first signed release, set the floor

`SIGNED_FROM_TAG` must be the tag of that first signed release. Skipping this
leaves signature stripping open — see **Why the floor exists** below.

### 4. Store the private key as a repo secret

Add the **full PEM** of `vibeflow-release-signing.pem` as the Actions secret
`RELEASE_SIGNING_KEY`. Then destroy your local copy, or move it to whatever
secret store you already trust — you do not need it again except to rotate.

The release workflow signs only when that secret is present, so releases keep
working before it is added.

## Custody

- **Private key**: only in the `RELEASE_SIGNING_KEY` secret. Anyone who can read
  it can sign artifacts that the installer will accept as ours.
- **If it leaks**: generate a new keypair, pin the new public key, publish a new
  npm version. `npx` users pick it up on their next run because npx always
  fetches the latest package — only people pinning `--version` or installing
  globally lag behind.
- **Compromise ≠ silent failure**: signatures made with the old key start failing
  loudly against the new pinned key, which is the intended behaviour.

## What this does and does not prove

It proves `checksums.txt` was signed by whoever holds the private key. It does
**not** make the chain unconditionally trustworthy: the pinned public key ships
inside the npm package, so an attacker able to publish to
`@axiom-studio/vibeflow-setup` could replace the key and the verification code
together.

The gain is concrete but bounded — an attacker now needs **two independent**
compromises (npm publish credentials *and* the GitHub release) rather than one.

## Verifying a release by hand

```bash
TAG=v1.0.24
curl -fsSLO https://github.com/axiom-studio/vibeflow-cli/releases/download/$TAG/checksums.txt
curl -fsSLO https://github.com/axiom-studio/vibeflow-cli/releases/download/$TAG/checksums.txt.sig
openssl pkeyutl -verify -pubin -inkey pub.pem -rawin -in checksums.txt -sigfile checksums.txt.sig
# -> Signature Verified Successfully
sha256sum -c checksums.txt --ignore-missing
```

Or force the installer to refuse anything unsigned:

```bash
npx @axiom-studio/vibeflow-setup --api-key <key> --require-signature
```

## Behaviour matrix

`SIGNED_FROM_TAG` is the first tag published with a signature. At or after it, a
**missing** signature is fatal; before it, a missing signature is expected.

| Pinned key | Floor set | Tag vs floor | `.sig` | Signature | Result |
|---|---|---|---|---|---|
| absent | — | — | — | — | warn, continue on checksum (`--require-signature` → fail) |
| present | no | — | absent | — | warn, continue on checksum (`--require-signature` → fail) |
| present | yes | **before** floor | absent | — | warn, continue on checksum — legitimately unsigned |
| present | yes | **at/after** floor | absent | — | ❌ **fails closed by default** (`--allow-unsigned` to override) |
| present | — | — | present | valid | ✅ "signature verified" |
| present | — | — | present | **invalid** | ❌ **always fails closed** — no flag overrides it |

### Why the floor exists

Forging a signature is hard. **Withholding one is free.** An adversary able to
rewrite the archive and `checksums.txt` — a TLS-intercepting proxy, or anyone who
can write to the release — can simply serve a 404 for `checksums.txt.sig`. Without
a floor the installer would degrade to same-origin checksum trust, which is
exactly the state signing was introduced to replace, having printed a warning that
scrolls past in a noisy install (issue #4387).

`--require-signature` alone does not solve this: an opt-in control does not
protect the population an attacker is targeting. Hence the floor makes refusal the
**default** for every release published in the signed era, while keeping genuinely
older tags installable.

### Setting the floor

After the first signed release is published, set both constants in `npx/index.js`
in the same change:

```js
const RELEASE_SIGNING_PUBLIC_KEY = `-----BEGIN PUBLIC KEY-----
...
-----END PUBLIC KEY-----`;
const SIGNED_FROM_TAG = 'v1.0.24';   // the first tag with a checksums.txt.sig
```

Until `SIGNED_FROM_TAG` is set, enforcement is inert — a pinned key alone verifies
a signature when present but cannot tell a stripped one from a legitimately absent
one, because it has no idea which releases are supposed to have it.
