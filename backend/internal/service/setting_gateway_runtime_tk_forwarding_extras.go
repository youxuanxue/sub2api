package service

// TokenKey extras for the shared gatewayForwardingCache: Anthropic request
// normalize + canonical ingress/mimicry toggles. Getters live in
// setting_service_tk_anthropic_normalize.go / setting_service_tk_canonical_ingress.go;
// this companion owns key list + parse so setting_gateway_runtime.go only
// appends keys and copies fields into the unexported cache structs.
//
// Cache struct field declarations stay in setting_gateway_runtime.go — they are
// part of the shared unexported cache shape, not movable without rewriting the
// loader.

func tkGatewayForwardingExtraSettingKeys() []string {
	return []string{
		SettingKeyAnthropicRequestNormalizeEnabled,
		SettingKeyAnthropicCanonicalIngressStrictEnabled,
		SettingKeyAnthropicCanonicalHaikuMimicryEnabled,
	}
}

type tkGatewayForwardingExtras struct {
	anthropicRequestNormalize bool
	canonicalIngressStrict    bool
	canonicalHaikuMimicry     bool
}

func tkParseGatewayForwardingExtras(values map[string]string) tkGatewayForwardingExtras {
	return tkGatewayForwardingExtras{
		anthropicRequestNormalize: !isFalseSettingValue(values[SettingKeyAnthropicRequestNormalizeEnabled]),
		canonicalIngressStrict:    values[SettingKeyAnthropicCanonicalIngressStrictEnabled] == "true",
		canonicalHaikuMimicry:     values[SettingKeyAnthropicCanonicalHaikuMimicryEnabled] == "true",
	}
}

func tkDefaultGatewayForwardingExtras() tkGatewayForwardingExtras {
	return tkGatewayForwardingExtras{
		anthropicRequestNormalize: true,
		canonicalIngressStrict:    false,
		canonicalHaikuMimicry:     false,
	}
}
