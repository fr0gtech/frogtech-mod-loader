package app

import "testing"

func testApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	a, err := NewApp(dir)
	if err != nil {
		t.Fatal(err)
	}
	a.token = "secret-token-0123456789abcdef0123"
	return a
}
