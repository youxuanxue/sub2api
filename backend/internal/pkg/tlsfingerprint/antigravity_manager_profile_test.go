package tlsfingerprint

import (
	"testing"

	utls "github.com/refraction-networking/utls"
)

func TestAntigravityManagerChromePresetUsesChromeSpec(t *testing.T) {
	profile := &Profile{Name: "tk_canonical_antigravity_manager_chrome123"}
	if !IsAntigravityManagerChromePreset(profile) {
		t.Fatal("manager profile was not recognized")
	}
	spec := buildClientHelloSpecFromProfile(profile)
	if len(spec.CipherSuites) == 0 || len(spec.Extensions) == 0 {
		t.Fatalf("Chrome compatibility preset is empty: ciphers=%d extensions=%d", len(spec.CipherSuites), len(spec.Extensions))
	}
	if !containsExtension(spec.Extensions, 16) {
		t.Fatal("Chrome preset did not include ALPN extension")
	}
}

func TestAntigravityManagerChromePresetDoesNotMatchCLI(t *testing.T) {
	if IsAntigravityManagerChromePreset(&Profile{Name: "tk_canonical_antigravity_cli"}) {
		t.Fatal("CLI profile incorrectly matched Manager preset")
	}
}

func containsExtension(extensions []utls.TLSExtension, typ uint16) bool {
	for _, extension := range extensions {
		if extensionType(extension) == typ {
			return true
		}
	}
	return false
}

func extensionType(extension utls.TLSExtension) uint16 {
	// uTLS does not expose a common extension type method; type switches cover
	// the Chrome preset extensions used by this regression test.
	switch extension.(type) {
	case *utls.ALPNExtension:
		return 16
	case *utls.SNIExtension:
		return 0
	case *utls.SupportedCurvesExtension:
		return 10
	case *utls.SupportedVersionsExtension:
		return 43
	case *utls.SignatureAlgorithmsExtension:
		return 13
	default:
		return 0xffff
	}
}
