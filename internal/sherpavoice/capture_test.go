package sherpavoice

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTrustedRecorderArgumentsAndReap(t *testing.T) {
	// This is a sleeping fake child, never a microphone or model invocation.
	recorder := filepath.Join(t.TempDir(), "fake-recorder")
	if err := os.WriteFile(recorder, []byte("#!/bin/sh\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	source, err := StartMicrophone(recorder, "chosen-source")
	if err != nil {
		t.Fatal(err)
	}
	s := source.(*pipeSource)
	want := []string{recorder, "--rate=16000", "--channels=1", "--format=s16", "--raw", "--target", "chosen-source", "-"}
	if !reflect.DeepEqual(s.cmd.Args, want) {
		t.Fatalf("arguments: %v", s.cmd.Args)
	}
	if err = source.Close(); err != nil {
		t.Fatal(err)
	}
	if err = source.Close(); err != nil {
		t.Fatal(err)
	}
	if s.cmd.ProcessState == nil {
		t.Fatal("child was not waited")
	}
}
