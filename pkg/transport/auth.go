package transport

// ValidateCredentials validates client credentials against auth configuration.
// Returns (authenticated bool, isClient bool) where isClient indicates
// whether the credentials matched via ClientAuthValidator (dynamic) vs fixed gateway auth.
//
// Authentication priority:
//  1. Dynamic client auth (if validator configured)
//  2. Fixed gateway auth (username/password match)
//  3. No auth configured - allow access (development mode)
func ValidateCredentials(authConfig *AuthConfig, clientID, username, password string) (bool, bool) {
	// 1. Try dynamic client authentication (if validator is configured)
	if authConfig != nil && authConfig.ClientValidator != nil {
		if authConfig.ClientValidator(clientID, password) {
			return true, true
		}
	}

	// 2. Try fixed gateway authentication
	if authConfig != nil && authConfig.Username != "" {
		if username == authConfig.Username && password == authConfig.Password {
			return true, false
		}
	}

	// 3. No auth configured - allow access (development mode)
	if authConfig == nil || (authConfig.Username == "" && authConfig.ClientValidator == nil) {
		return true, false
	}

	return false, false
}
