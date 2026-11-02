package updater

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type testOutputFile struct{ closeErr error }

func (f testOutputFile) Write(p []byte) (int, error) { return len(p), nil }
func (f testOutputFile) Close() error                { return f.closeErr }

func writeTestTarGz(t *testing.T, path, name string, contents []byte, declaredSize int64, closeTar bool) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: declaredSize, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(contents); err != nil {
		t.Fatal(err)
	}
	if closeTar {
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExtractTarGzSuccess(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "update.tar.gz")
	writeTestTarGz(t, archive, "bin/radar", []byte("binary"), int64(len("binary")), true)
	dest := t.TempDir()
	if err := extractTarGz(archive, dest); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "bin", "radar"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binary" {
		t.Fatalf("extracted bytes = %q", got)
	}
}

func TestExtractTarGzTruncatedArchive(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "truncated.tar.gz")
	writeTestTarGz(t, archive, "radar", []byte("ab"), 8, false)
	if err := extractTarGz(archive, t.TempDir()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("truncated extraction error = %v, want unexpected EOF", err)
	}
}

func TestExtractTarGzRetainsCopyAndCloseErrors(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "update.tar.gz")
	writeTestTarGz(t, archive, "radar", []byte("binary"), int64(len("binary")), true)
	copyErr := errors.New("copy failed")
	closeErr := errors.New("close failed")
	err := extractTarGzWith(
		archive,
		t.TempDir(),
		func(string, os.FileMode) (io.WriteCloser, error) { return testOutputFile{closeErr: closeErr}, nil },
		func(io.Writer, io.Reader) (int64, error) { return 0, copyErr },
	)
	if !errors.Is(err, copyErr) || !errors.Is(err, closeErr) {
		t.Fatalf("joined extraction error = %v, want copy and close errors", err)
	}
}
