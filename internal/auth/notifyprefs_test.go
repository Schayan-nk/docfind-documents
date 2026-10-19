package auth

import (
	"testing"
)

func mutedStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := tempDir(t) + "/users.json"
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("reader", "خواننده", "a-long-enough-password", RoleViewer); err != nil {
		t.Fatal(err)
	}
	return s, path
}
