package domain

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestSourceIdentityIsOrderedImmutableAndIncludesExecutableBit(t *testing.T) {
	original := []SourceFile{{Path: "src/你好.go", Content: []byte("package main\n")}, {Path: "README.md", Content: []byte("hello\n")}}
	normalized, digest, total, err := NormalizeSource(original)
	if err != nil {
		t.Fatal(err)
	}
	reversed := []SourceFile{original[1], original[0]}
	_, replayed, replayTotal, err := NormalizeSource(reversed)
	if err != nil || replayed != digest || total != replayTotal || normalized[0].Path != "README.md" {
		t.Fatalf("order changed identity: %s %s %v", digest, replayed, err)
	}
	original[0].Content[0] = 'X'
	if string(normalized[1].Content) != "package main\n" {
		t.Fatal("source retained caller-owned mutable bytes")
	}
	normalized[0].Executable = true
	_, changed, _, err := NormalizeSource(normalized)
	if err != nil || changed == digest {
		t.Fatal("executable bit did not affect identity")
	}
}
func TestSourceRejectsUnsafeAndConflictingPaths(t *testing.T) {
	for _, name := range []string{"", "/etc/passwd", "../a", "a/../b", "a/./b", "a//b", "a/", "C:/file", "a\\b", "a\x00b", "a\nb", ".git/config", "src/.GIT/config", strings.Repeat("x", 241), strings.Repeat("a/", 16) + "file"} {
		t.Run(name, func(t *testing.T) {
			if _, _, _, err := NormalizeSource([]SourceFile{{Path: name}}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("unsafe path accepted: %v", err)
			}
		})
	}
	for _, files := range [][]SourceFile{
		{{Path: "a"}, {Path: "a"}},
		{{Path: "a"}, {Path: "a-b"}, {Path: "a/b"}},
		{{Path: "src/a/file"}, {Path: "src/a"}},
	} {
		if _, _, _, err := NormalizeSource(files); !errors.Is(err, ErrInvalid) {
			t.Fatalf("file/dir collision accepted: %v", err)
		}
	}
}
func TestSourceEnforcesIndependentFileAndAggregateBudgets(t *testing.T) {
	if _, _, _, err := NormalizeSource(nil); err == nil {
		t.Fatal("empty package accepted")
	}
	if _, _, _, err := NormalizeSource([]SourceFile{{Path: "file", Content: bytes.Repeat([]byte{'x'}, MaxSourceFileBytes+1)}}); err == nil {
		t.Fatal("oversized file accepted")
	}
	files := []SourceFile{{Path: "a", Content: bytes.Repeat([]byte{'a'}, MaxSourceFileBytes)}, {Path: "b", Content: bytes.Repeat([]byte{'b'}, MaxSourceFileBytes)}}
	if _, _, size, err := NormalizeSource(files); err != nil || size != MaxSourceTotalBytes {
		t.Fatalf("legal boundary refused: size=%d %v", size, err)
	}
	files = append(files, SourceFile{Path: "c", Content: []byte("x")})
	if _, _, _, err := NormalizeSource(files); err == nil {
		t.Fatal("aggregate budget bypassed")
	}
}
