package sherpavoice

import "testing"

func TestFrozenCompiledWake(t *testing.T) {
	for _, text := range []string{"HH EY1 G IH1 NG K OW1", "HH EY1 G IH1 NG K OW1 :1 #0.25 @NOTE_ALIAS"} {
		if _, err := compiledWakeLabel(text); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"Hey Ginkgo", "HH EY1 G IH1 NG K OW2", "HH EY1 G IH1 NG K", "HH EY1 G IH1 NG K OW1 :2", "HH EY1 G IH1 NG K OW1 #0.1", "HH EY1 G IH1 NG K OW1 @Hey.Ginkgo", "HH EY1 G IH1 NG K OW1 @one @two", "HH EY1 G IH1 NG K OW1\nHH EY1 G IH1 NG K OW1"} {
		if _, err := compiledWakeLabel(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	if err := attestKWS([]byte("wrong"), 0); err == nil {
		t.Fatal("accepted unverified model")
	}
	if err := attestKWS(nil, -1); err == nil {
		t.Fatal("accepted invalid artifact index")
	}
}
