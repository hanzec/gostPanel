package main

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

const (
	toolReleaseIdentity  = "https://github.com/hanzec/yagp/.github/workflows/release.yml@refs/heads/master"
	toolReleaseIssuer    = "https://token.actions.githubusercontent.com"
	toolReleasePredicate = "https://slsa.dev/provenance/v1"
)

// This snapshot is updated with the updater; verification never downloads trust
// material. See sigstore/README.md for its pinned upstream source and license.
//
//go:embed sigstore/trusted_root.json
var toolReleaseTrustedRoot []byte

func verifyToolAttestation(archivePath, bundlePath string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, archive); err != nil {
		return err
	}
	attestations, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer attestations.Close()
	trustedRoot, err := root.NewTrustedRootFromJSON(toolReleaseTrustedRoot)
	if err != nil {
		return fmt.Errorf("load embedded Sigstore trust root: %w", err)
	}
	return verifyToolAttestationDigest(attestations, digest.Sum(nil), trustedRoot)
}

func verifyToolAttestationDigest(attestations io.Reader, digest []byte, trusted root.TrustedMaterial) error {
	verifier, err := verify.NewVerifier(trusted,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1))
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(attestations)
	var failures error
	for {
		var attestation bundle.Bundle
		if err := decoder.Decode(&attestation); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("decode tool release attestation: %w", err)
		}
		if err := verifyToolReleaseBundle(verifier, &attestation, digest); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		return nil
	}
	return fmt.Errorf("no trusted tool release attestation: %w", errors.Join(errors.New("no matching bundle"), failures))
}

func verifyToolReleaseBundle(verifier *verify.Verifier, attestation verify.SignedEntity, digest []byte) error {
	identity, err := verify.NewShortCertificateIdentity(toolReleaseIssuer, "", toolReleaseIdentity, "")
	if err != nil {
		return err
	}
	result, err := verifier.Verify(attestation, verify.NewPolicy(
		verify.WithArtifactDigest("sha256", digest), verify.WithCertificateIdentity(identity)))
	if err != nil {
		return err
	}
	if result.Statement == nil || result.Statement.PredicateType != toolReleasePredicate {
		return errors.New("tool release attestation must contain SLSA provenance v1")
	}
	return nil
}
