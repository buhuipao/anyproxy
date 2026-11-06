package client

import "testing"

func TestGenerateClientIDIsRandom(t *testing.T) {
	// Replicas started in the same second used to get the same ID
	if generateClientID("client", 1) == generateClientID("client", 1) {
		t.Error("client IDs generated at the same time should differ")
	}
}
