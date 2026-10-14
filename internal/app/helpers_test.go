package app

import (
	"os"
	"testing"
	"time"

	"github.com/fr0gtech/frogtech-mod-loader/internal/fetch"
)

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

// Some tests talk to servers that are down on purpose; don't wait the real
// 1s/3s/7s between retries.
func TestMain(m *testing.M) {
	fetch.RetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	os.Exit(m.Run())
}
