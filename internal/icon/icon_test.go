package icon

import "testing"

func TestIconICO(t *testing.T) {
	ico := Favicon()
	if len(ico) < 100 || ico[2] != 1 || ico[4] != 3 {
		t.Fatalf("bad ico header % x", ico[:6])
	}
}
