package securestore

import "testing"

func TestBoxRoundTrip(t *testing.T) {
	box, err := Open(t.TempDir() + "/master.key")
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := box.Encrypt("secret-value")
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == "secret-value" || encrypted == "" {
		t.Fatalf("secret was not encrypted: %q", encrypted)
	}
	plain, err := box.Decrypt(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "secret-value" {
		t.Fatalf("got %q", plain)
	}
}
