package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadModuleSnapshotReadsOnlyTheModulesOwnRegularFiles(t *testing.T) {
	root := t.TempDir()
	write := func(name, content string) {
		p := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
	write("go.mod", "module example.com/design\n")
	write("go.sum", "")
	write("design/design.go", "package design\n")
	write("design/design_test.go", "package design\n")
	write("design/notes.md", "notes")
	write("internal/x/x.go", "package x\n")
	write("nested/go.mod", "module example.com/nested\n")
	write("nested/n.go", "package nested\n")
	write(".git/config", "")
	write("vendor/v/v.go", "package v\n")

	files, err := readModuleSnapshot(root)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{
		"go.mod":           "module example.com/design\n",
		"go.sum":           "",
		"design/design.go": "package design\n",
		"internal/x/x.go":  "package x\n",
	}, files)
}

func TestReadModuleSnapshotRejectsSymbolicLinks(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.go")
	require.NoError(t, os.WriteFile(outside, []byte("package secret\n"), 0o600))
	for name, link := range map[string]func(root string) error{
		"file link":      func(root string) error { return os.Symlink(outside, filepath.Join(root, "design", "linked.go")) },
		"directory link": func(root string) error { return os.Symlink(filepath.Dir(outside), filepath.Join(root, "linked")) },
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "design"), 0o750))
			require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/design\n"), 0o600))
			require.NoError(t, link(root))
			_, err := readModuleSnapshot(root)
			assert.ErrorContains(t, err, "symbolic link")
		})
	}
}
