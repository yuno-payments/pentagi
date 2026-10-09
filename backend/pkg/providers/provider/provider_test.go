package provider

import "testing"

func TestModelCredential_Secret_PrefersAPIKeyThenOAuthThenEmpty(t *testing.T) {
	if got := (&ModelCredential{APIKey: "k", OAuthToken: "o"}).Secret(); got != "k" {
		t.Fatalf("Secret() = %q, want api key", got)
	}
	if got := (&ModelCredential{OAuthToken: "o"}).Secret(); got != "o" {
		t.Fatalf("Secret() = %q, want oauth token", got)
	}
	if got := (&ModelCredential{}).Secret(); got != "" {
		t.Fatalf("Secret() = %q, want empty", got)
	}
	var nilCred *ModelCredential
	if got := nilCred.Secret(); got != "" {
		t.Fatalf("nil.Secret() = %q, want empty", got)
	}
}
