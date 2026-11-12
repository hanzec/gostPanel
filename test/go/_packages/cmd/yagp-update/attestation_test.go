package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

func TestToolReleaseAttestationVerifiesRealReleaseOffline(t *testing.T) {
	previous := http.DefaultTransport
	http.DefaultTransport = offlineAttestationTransport{t}
	t.Cleanup(func() { http.DefaultTransport = previous })

	attestations, err := os.ReadFile("../../test/go/fixtures/attestations/yagp-depoly-0.1.147-attestations.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := root.NewTrustedRootFromJSON(toolReleaseTrustedRoot)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := hex.DecodeString("32906a47fc33d9437cdd970d1b5aecf699010fcf62d4e71f2fb05ab1ae4f6242")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyToolAttestationDigest(bytes.NewReader(attestations), digest, trusted); err != nil {
		t.Fatalf("verify real published release without network: %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"signature changed", func(b map[string]any) {
			envelope := b["dsseEnvelope"].(map[string]any)
			envelope["signatures"].([]any)[0].(map[string]any)["sig"] = "AAAA"
		}},
		{"transparency evidence removed", func(b map[string]any) {
			b["verificationMaterial"].(map[string]any)["tlogEntries"] = []any{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var modified map[string]any
			if err := json.Unmarshal(attestations, &modified); err != nil {
				t.Fatal(err)
			}
			tc.mutate(modified)
			data, err := json.Marshal(modified)
			if err != nil {
				t.Fatal(err)
			}
			if err := verifyToolAttestationDigest(bytes.NewReader(data), digest, trusted); err == nil {
				t.Fatal("accepted altered release proof")
			}
		})
	}
	for _, data := range [][]byte{nil, []byte("not JSON"), []byte("{}"), []byte("{\"mediaType\":")} {
		if err := verifyToolAttestationDigest(bytes.NewReader(data), digest, trusted); err == nil {
			t.Fatalf("accepted invalid bundle %q", data)
		}
	}
	digest[0] ^= 1
	if err := verifyToolAttestationDigest(bytes.NewReader(attestations), digest, trusted); err == nil {
		t.Fatal("accepted a different archive digest")
	}
}

func TestToolReleaseAttestationPolicy(t *testing.T) {
	virtual, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	// VirtualSigstore does not mint SCTs; the real release fixture above covers
	// the production verifier's required certificate transparency check.
	verifier, err := verify.NewVerifier(virtual, verify.WithTransparencyLog(1), verify.WithObserverTimestamps(1))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("signed tool archive"))
	for _, tc := range []struct {
		name, identity, issuer, predicate string
		wantError                         string
	}{
		{"release workflow", toolReleaseIdentity, toolReleaseIssuer, toolReleasePredicate, ""},
		{"different workflow", strings.Replace(toolReleaseIdentity, "release.yml", "other.yml", 1), toolReleaseIssuer, toolReleasePredicate, "identity"},
		{"different branch", strings.Replace(toolReleaseIdentity, "master", "main", 1), toolReleaseIssuer, toolReleasePredicate, "identity"},
		{"different issuer", toolReleaseIdentity, "https://issuer.example", toolReleasePredicate, "identity"},
		{"different predicate", toolReleaseIdentity, toolReleaseIssuer, "https://spdx.dev/Document", "SLSA provenance v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			statement := fmt.Sprintf(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"tools.tar.gz","digest":{"sha256":"%x"}}],"predicateType":%q,"predicate":{}}`, digest, tc.predicate)
			entity, err := virtual.Attest(tc.identity, tc.issuer, []byte(statement))
			if err != nil {
				t.Fatal(err)
			}
			err = verifyToolReleaseBundle(verifier, entity, digest[:])
			if tc.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("verification = %v; want error containing %q", err, tc.wantError)
			}
		})
	}
}

func TestToolReleaseAttestationReadsArchive(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "tools.tar.gz")
	if err := os.WriteFile(archive, []byte("untrusted replacement archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyToolAttestation(archive, "../../test/go/fixtures/attestations/yagp-depoly-0.1.147-attestations.jsonl"); err == nil {
		t.Fatal("accepted a file whose SHA-256 differs from the release attestation")
	}
	if err := verifyToolAttestation(filepath.Join(dir, "missing"), "unused"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing archive: %v", err)
	}
	if err := verifyToolAttestation(archive, filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing bundle: %v", err)
	}
}

type offlineAttestationTransport struct{ t *testing.T }

func (transport offlineAttestationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.t.Errorf("attestation verification attempted network access: %s", request.URL)
	return nil, errors.New("network disabled for offline attestation verification")
}
