# TODO

Things that are planned or worth doing, roughly in priority order. Nothing here
is promised, it is a place to keep ideas from getting lost.

## Code signing of releases

The programs are not signed yet, so Windows and macOS show a warning on first
run. See "Code signing" in the README for what users do about it today.

- Windows: apply to the [SignPath Foundation](https://signpath.org/apply.html)
  for a free open source certificate. Needs a public repo, the MIT license
  (done), and a "Code signing policy" section in the README listing the roles
  and a privacy statement. Then add the SignPath step to the release workflow.
- macOS: a Developer ID signature and notarization need the paid Apple
  Developer Program ($99 a year). No free route was found. Decide whether it is
  worth it.
- Linux: add detached signatures (Sigstore cosign or GPG) next to the existing
  GitHub build attestation.
- Pin the GitHub Actions to commit hashes instead of version tags. Dependabot
  already keeps them updated.

## PKI signing and key based encryption

Today the sender and recipient share a passphrase, and nothing proves who made
a file. Ideas for using public keys instead of, or as well as, the passphrase:

- Sign archives with the sender's private key (Ed25519 or an X.509 certificate)
  so the recipient can check who made the archive and that it was not changed.
- Encrypt to the recipient's public key, so no shared passphrase has to be
  agreed or sent. Several recipients could be supported by wrapping the archive
  key once per recipient.
- Let the recipient trust a sender by key fingerprint or by a certificate chain
  to a CA they already use (for example a company CA).
- Support certificate expiry and revocation checks, or at least show the
  validity period.
- Hardware keys and smart cards (PKCS#11) for the signing step.
- Keep the key handling in the packer and the verification in the unpacker, and
  show the signer's name and fingerprint on the unpacker page before asking for
  anything.
- Open question: the unpacker itself is still an unsigned program, so a swapped
  unpacker can skip any check. A real fix probably means a signed, unmodified
  unpacker that opens a separate archive file. That gives up the single file
  design, so decide if it is worth it (maybe as a second mode).

## Hardening left over from the security review

- Check free disk space against the size the archive declares before
  extracting, and warn when it will not fit.
- Warn in the preview about risky names such as `.bashrc` or `.ssh/...`.
- Wipe the passphrase and derived keys from memory where Go allows it.
- Run `govulncheck` in CI (added, needs a first run on GitHub to confirm) and
  look at the results.

## Testing and platforms

- Run the Windows and macOS builds on real machines. So far only Linux has
  been run, the others are cross compiled.
- Check that a macOS binary still runs after the archive is attached to it,
  including on Apple silicon.
- Add an end to end test in CI that packs and unpacks with the built binaries
  on all three systems.
- Linux and Windows on ARM builds exist but are untested.

## Nice to have

- Choose an output file name suffix or icon for the generated programs.
- A progress bar with an estimated time for large archives.
- Option to split very large archives into several files.
- Dark mode screenshots in the README.
