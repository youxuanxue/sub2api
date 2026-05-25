package service

// tlsFingerprintProfileNameForAccount resolves the bound TLS profile name for HTTP
// fingerprint pinning (B1). Returns "" when TLS fingerprint is off or unbound.
func (s *GatewayService) tlsFingerprintProfileNameForAccount(account *Account) string {
	if s == nil || s.tlsFPProfileService == nil || account == nil || !account.IsTLSFingerprintEnabled() {
		return ""
	}
	id := account.GetTLSFingerprintProfileID()
	if id <= 0 {
		return ""
	}
	if p := s.tlsFPProfileService.GetProfileByID(id); p != nil {
		return p.Name
	}
	return ""
}
