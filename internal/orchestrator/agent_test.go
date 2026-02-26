package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunAgent_PathTraversal(t *testing.T) {
	// Create a temp directory to use as agentDNADir
	dnaDir := t.TempDir()

	// Create a valid DNA file inside the dir
	validDNA := filepath.Join(dnaDir, "valid.md")
	if err := os.WriteFile(validDNA, []byte("test dna"), 0644); err != nil {
		t.Fatalf("failed to write test DNA: %v", err)
	}

	// Create a file OUTSIDE the DNA dir
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outsideFile, []byte("secret data"), 0644); err != nil {
		t.Fatalf("failed to write outside file: %v", err)
	}

	tests := []struct {
		name      string
		dnaFile   string
		wantError string
	}{
		{
			name:      "parent traversal",
			dnaFile:   "../../../etc/passwd",
			wantError: "DNA path traversal blocked",
		},
		{
			name:      "relative traversal to outside file",
			dnaFile:   "../" + filepath.Base(outsideDir) + "/secret.md",
			wantError: "DNA path traversal blocked",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ag := Agent{
				Name:    "TestAgent",
				DNAFile: tt.dnaFile,
				Tier:    4,
				Weight:  1,
			}

			v := RunAgent(nil, nil, ag, "test task", dnaDir, "/tmp", 0)
			if v.VerdictStr != "ERROR" {
				t.Errorf("VerdictStr = %q, want ERROR", v.VerdictStr)
			}
			if v.Error == "" {
				t.Error("Expected error message for traversal attempt")
			}
		})
	}
}
