package tlsfingerprint

import (
	"testing"

	utls "github.com/refraction-networking/utls"
)

func TestAntigravityCLIProfile_BuildClientHelloSpec(t *testing.T) {
	t.Parallel()
	// Values from deploy/aws/stage0/tk_canonical_antigravity_cli.json
	// (local agy 1.2.2 mitm ClientHello capture, 2026-09-18).
	profile := &Profile{
		Name:                "tk_canonical_antigravity_cli",
		EnableGREASE:        false,
		ShuffleExtensions:   false,
		CipherSuites:        []uint16{49195, 49199, 49196, 49200, 52393, 52392, 49161, 49171, 49162, 49172, 4865, 4866, 4867},
		Curves:              []uint16{4588, 4587, 4589, 29, 23, 24, 25},
		PointFormats:        []uint16{0},
		SignatureAlgorithms: []uint16{2308, 2309, 2310, 2052, 1027, 2055, 2053, 2054, 1025, 1281, 1537, 1283, 1539},
		ALPNProtocols:       []string{"h2", "http/1.1"},
		SupportedVersions:   []uint16{772, 771},
		KeyShareGroups:      []uint16{4588, 29},
		PSKModes:            []uint16{},
		Extensions:          []uint16{0, 11, 65281, 23, 18, 5, 10, 13, 50, 16, 43, 51},
	}
	spec := buildClientHelloSpecFromProfile(profile)
	if spec == nil {
		t.Fatal("nil ClientHelloSpec")
	}
	if len(spec.CipherSuites) != len(profile.CipherSuites) {
		t.Fatalf("cipher count: got %d want %d", len(spec.CipherSuites), len(profile.CipherSuites))
	}
	if len(spec.Extensions) != len(profile.Extensions) {
		t.Fatalf("extension count: got %d want %d", len(spec.Extensions), len(profile.Extensions))
	}

	var keyShare *utls.KeyShareExtension
	for _, ext := range spec.Extensions {
		if ks, ok := ext.(*utls.KeyShareExtension); ok {
			keyShare = ks
			break
		}
	}
	if keyShare == nil {
		t.Fatal("missing key_share extension")
	}
	if len(keyShare.KeyShares) != 2 {
		t.Fatalf("key_share groups: got %d want 2", len(keyShare.KeyShares))
	}
	if keyShare.KeyShares[0].Group != utls.X25519MLKEM768 {
		t.Fatalf("first key_share want X25519MLKEM768 (4588), got %v", keyShare.KeyShares[0].Group)
	}
	if keyShare.KeyShares[1].Group != utls.X25519 {
		t.Fatalf("second key_share want X25519, got %v", keyShare.KeyShares[1].Group)
	}

	// Stable order: rebuild must keep the same extension count (no shuffle).
	spec2 := buildClientHelloSpecFromProfile(profile)
	if len(spec2.Extensions) != len(spec.Extensions) {
		t.Fatal("unstable extension count across builds")
	}
}
