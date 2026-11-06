package transport

import "testing"

func TestValidateCredentials(t *testing.T) {
	t.Parallel()

	alwaysValid := func(_, _ string) bool { return true }
	alwaysInvalid := func(_, _ string) bool { return false }
	matchExact := func(id, key string) bool { return id == "client1" && key == "secret1" }

	tests := []struct {
		name         string
		authConfig   *AuthConfig
		clientID     string
		username     string
		password     string
		wantAuth     bool
		wantIsClient bool
	}{
		{
			name:         "nil auth config allows access (dev mode)",
			authConfig:   nil,
			clientID:     "any",
			username:     "",
			password:     "",
			wantAuth:     true,
			wantIsClient: false,
		},
		{
			name:         "empty auth config allows access (dev mode)",
			authConfig:   &AuthConfig{},
			clientID:     "any",
			username:     "",
			password:     "",
			wantAuth:     true,
			wantIsClient: false,
		},
		{
			name: "dynamic validator succeeds",
			authConfig: &AuthConfig{
				ClientValidator: alwaysValid,
			},
			clientID:     "client1",
			username:     "",
			password:     "secret1",
			wantAuth:     true,
			wantIsClient: true,
		},
		{
			name: "dynamic validator fails with no fixed auth falls through to reject",
			authConfig: &AuthConfig{
				ClientValidator: alwaysInvalid,
			},
			clientID:     "client1",
			username:     "",
			password:     "wrong",
			wantAuth:     false,
			wantIsClient: false,
		},
		{
			name: "dynamic validator fails but fixed auth succeeds",
			authConfig: &AuthConfig{
				Username:        "admin",
				Password:        "pass",
				ClientValidator: alwaysInvalid,
			},
			clientID:     "client1",
			username:     "admin",
			password:     "pass",
			wantAuth:     true,
			wantIsClient: false,
		},
		{
			name: "fixed auth succeeds",
			authConfig: &AuthConfig{
				Username: "admin",
				Password: "pass",
			},
			clientID:     "client1",
			username:     "admin",
			password:     "pass",
			wantAuth:     true,
			wantIsClient: false,
		},
		{
			name: "fixed auth fails with wrong username",
			authConfig: &AuthConfig{
				Username: "admin",
				Password: "pass",
			},
			clientID:     "client1",
			username:     "wrong",
			password:     "pass",
			wantAuth:     false,
			wantIsClient: false,
		},
		{
			name: "fixed auth fails with wrong password",
			authConfig: &AuthConfig{
				Username: "admin",
				Password: "pass",
			},
			clientID:     "client1",
			username:     "admin",
			password:     "wrong",
			wantAuth:     false,
			wantIsClient: false,
		},
		{
			name: "dynamic validator has priority over fixed auth",
			authConfig: &AuthConfig{
				Username:        "admin",
				Password:        "pass",
				ClientValidator: matchExact,
			},
			clientID:     "client1",
			username:     "admin",
			password:     "secret1",
			wantAuth:     true,
			wantIsClient: true,
		},
		{
			name: "dynamic validator fails then fixed auth checked",
			authConfig: &AuthConfig{
				Username:        "admin",
				Password:        "fixedpass",
				ClientValidator: matchExact,
			},
			clientID:     "other",
			username:     "admin",
			password:     "fixedpass",
			wantAuth:     true,
			wantIsClient: false,
		},
		{
			name: "both dynamic and fixed fail",
			authConfig: &AuthConfig{
				Username:        "admin",
				Password:        "pass",
				ClientValidator: matchExact,
			},
			clientID:     "other",
			username:     "wrong",
			password:     "wrong",
			wantAuth:     false,
			wantIsClient: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotAuth, gotIsClient := ValidateCredentials(tt.authConfig, tt.clientID, tt.username, tt.password)
			if gotAuth != tt.wantAuth {
				t.Errorf("ValidateCredentials() authenticated = %v, want %v", gotAuth, tt.wantAuth)
			}
			if gotIsClient != tt.wantIsClient {
				t.Errorf("ValidateCredentials() isClient = %v, want %v", gotIsClient, tt.wantIsClient)
			}
		})
	}
}
