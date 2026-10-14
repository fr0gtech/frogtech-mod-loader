package secrets

import (
	"strings"
	"testing"
)

func TestSecretStore(t *testing.T) {
	sealed, err := Seal("sk_live_secret1234")
	if err != nil || strings.Contains(sealed, "secret") {
		t.Fatalf("%q %v", sealed, err)
	}
	if k, err := Open(sealed); err != nil || k != "sk_live_secret1234" {
		t.Fatalf("%q %v", k, err)
	}
	if _, err := Open("bm90LXByb3RlY3RlZA=="); err == nil {
		t.Fatal("unprotected blob accepted")
	}
}
