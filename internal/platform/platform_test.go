package platform

import "testing"

func TestProtocolCommand(t *testing.T) {
	got := ProtocolCommand(`C:\Users\Frog\Downloads\FrogLoader.exe`)
	want := `"C:\Users\Frog\Downloads\FrogLoader.exe" --url "%1"`
	if got != want {
		t.Fatalf("%s", got)
	}
}
