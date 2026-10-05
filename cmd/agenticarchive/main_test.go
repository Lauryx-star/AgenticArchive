package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPDFPathRejectsEscapingSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.pdf")
	if err := os.WriteFile(outside, []byte("PDF"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.pdf")); err != nil {
		t.Fatal(err)
	}
	if _, err := safePDFPath(root, "link.pdf"); err == nil {
		t.Fatal("escaping symlink accepted")
	}
	inside := filepath.Join(root, "document.pdf")
	if err := os.WriteFile(inside, []byte("PDF"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := safePDFPath(root, "document.pdf"); err != nil || got == "" {
		t.Fatalf("valid PDF rejected: %v", err)
	}
}
