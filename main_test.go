package main

import (
	"bytes"
	"fmt"
	"runtime"
	"testing"
)

func TestVersionVariableIsNotEmpty(t *testing.T) {
	if Version == "" {
		t.Fatal("expected non-empty default version")
	}
}

func TestFormatVersionAddsVPrefixAndMetadata(t *testing.T) {
	got := formatVersion("mcp-shopping-list-firestore", "1.2.3")
	want := fmt.Sprintf("mcp-shopping-list-firestore version v1.2.3 (%s, %s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	if got != want {
		t.Fatalf("unexpected version output: got %q, want %q", got, want)
	}
}

func TestFormatVersionPreservesExistingVPrefix(t *testing.T) {
	got := formatVersion("mcp-shopping-list-firestore", "v1.2.3")
	want := fmt.Sprintf("mcp-shopping-list-firestore version v1.2.3 (%s, %s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	if got != want {
		t.Fatalf("unexpected version output: got %q, want %q", got, want)
	}
}

func TestFormatVersionAddsVPrefixToPrerelease(t *testing.T) {
	got := formatVersion("mcp-shopping-list-firestore", "1.2.3-rc1")
	want := fmt.Sprintf("mcp-shopping-list-firestore version v1.2.3-rc1 (%s, %s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	if got != want {
		t.Fatalf("unexpected version output: got %q, want %q", got, want)
	}
}

func TestFormatVersionPreservesDevVersion(t *testing.T) {
	got := formatVersion("mcp-shopping-list-firestore", "dev")
	want := fmt.Sprintf("mcp-shopping-list-firestore version dev (%s, %s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)

	if got != want {
		t.Fatalf("unexpected version output: got %q, want %q", got, want)
	}
}

func TestWriteVersionWritesTrailingNewline(t *testing.T) {
	var buf bytes.Buffer

	if err := writeVersion(&buf, "mcp-shopping-list-firestore", "1.2.3"); err != nil {
		t.Fatalf("writeVersion returned error: %v", err)
	}

	want := fmt.Sprintf("mcp-shopping-list-firestore version v1.2.3 (%s, %s/%s)\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	if buf.String() != want {
		t.Fatalf("unexpected written version output: got %q, want %q", buf.String(), want)
	}
}
