# Sigstore trust snapshot

`trusted_root.json` is copied without modification from the Sigstore project's
[`root-signing` repository, commit `c3310c530ba900dec2a2f1ee093f35c5d2de37f4`](https://github.com/sigstore/root-signing/blob/c3310c530ba900dec2a2f1ee093f35c5d2de37f4/targets/trusted_root.json).
Its SHA-256 is `6494e21ea73fa7ee769f85f57d5a3e6a08725eae1e38c755fc3517c9e6bc0b66`.
The upstream Apache-2.0 license is preserved in `LICENSE`.

The updater embeds this public trust material and uses the official
`github.com/sigstore/sigstore-go` verifier. It validates the certificate chain,
certificate transparency timestamp, artifact transparency evidence and trusted
signing time entirely from the snapshot and downloaded attestation bundle.
It does not contact Fulcio, Rekor, TUF, GitHub or another service during verification.
The bundle must attest the downloaded archive's SHA-256, use SLSA provenance v1,
and identify `.github/workflows/release.yml` on `hanzec/yagp`'s `master` branch
with the GitHub Actions OIDC issuer.

Refresh the snapshot from the official Sigstore repository when its trust
material rotates, record the immutable source commit and checksum here, and run
the offline real-release test before distributing an updated updater. Unknown
future signing keys fail verification until an updated trusted snapshot ships.

The real attestation fixture in `../testdata/yagp-depoly-0.1.147-attestations.jsonl`
comes from this MIT-licensed repository's
[`0.1.147-4713edf` release](https://github.com/hanzec/yagp/releases/tag/0.1.147-4713edf).
It is the unmodified `yagp-depoly-attestations.jsonl` asset, downloaded through
the release cache. Its SHA-256 is
`827b3655909a73ec1451d20f3705d7414dce2d8affb46f6d378ba5b0cfda70cc`.
The test verifies the amd64 archive digest
`32906a47fc33d9437cdd970d1b5aecf699010fcf62d4e71f2fb05ab1ae4f6242`,
also independently checked against the actual 6,158,597-byte release archive.
The large binary archive is deliberately not stored as a test fixture.
