package cli

import (
	"context"
	"testing"

	"github.com/spf13/cobra"
)

func TestCompleteTags(t *testing.T) {
	cancel, _, dataDir := startTestDaemon(t)
	defer cancel()

	cfgPath := writeConfigTOML(t, dataDir)

	// Seed some tags by adding notes
	root := NewRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "note", "add", "Note 1", "-t", "work,project,programming"})
	if err := root.Execute(); err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	root.SetArgs([]string{"--config", cfgPath, "note", "add", "Note 2", "-t", "personal,project,photography"})
	if err := root.Execute(); err != nil {
		t.Fatalf("seed 2: %v", err)
	}

	// Prepare a command with the right context for completion
	// We need to run PersistentPreRunE to initialize the app in context
	testCmd := NewRootCmd()
	testCmd.SetContext(context.Background())
	testCmd.SetArgs([]string{"--config", cfgPath, "note", "add"})
	// Execute just enough to trigger PersistentPreRunE but not the full command
	if err := testCmd.PersistentPreRunE(testCmd, nil); err != nil {
		t.Fatalf("setup context: %v", err)
	}

	noteCmd := findCmd(testCmd, "note")
	if noteCmd == nil {
		t.Fatal("could not find note command")
	}
	noteCmd.SetContext(testCmd.Context())

	tests := []struct {
		input    string
		expected []string
	}{
		{
			input:    "pr",
			expected: []string{"project", "programming"},
		},
		{
			input:    "photo",
			expected: []string{"photography"},
		},
		{
			input:    "work,pr",
			expected: []string{"work,project", "work,programming"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, _ := completeTags(noteCmd, nil, tt.input)

			// filter 'got' to only include things that should be there (fuzzy.Find is generous)
			// Actually, just check that all expected are in got
			for _, exp := range tt.expected {
				found := false
				for _, g := range got {
					if g == exp {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("input %q: expected %q not found in %v", tt.input, exp, got)
				}
			}
		})
	}
}

func findCmd(root *cobra.Command, name string) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}
