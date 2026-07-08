package service

func resolveTLSProfileNameForAccount(svc *TLSFingerprintProfileService, account *Account) string {
	if svc == nil || account == nil {
		return ""
	}
	profile := svc.ResolveTLSProfile(account)
	if profile == nil {
		return ""
	}
	return profile.Name
}
