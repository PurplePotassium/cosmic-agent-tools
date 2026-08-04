package statedir

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
)

type payload struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestWriteReadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "data.json")
	want := payload{Name: "héllo — ünïcode ✓", Count: 42}
	if err := WriteJSON(path, want); err != nil {
		t.Fatal(err)
	}
	var got payload
	if err := ReadJSON(path, &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("roundtrip mismatch: got %+v want %+v", got, want)
	}
}

func TestWriteIsBOMFreeLFTerminated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	if err := WriteJSON(path, payload{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(raw, utf8BOM) {
		t.Fatal("output starts with a UTF-8 BOM")
	}
	if bytes.Contains(raw, []byte("\r")) {
		t.Fatal("output contains CR bytes")
	}
	if !bytes.HasSuffix(raw, []byte("\n")) {
		t.Fatal("output missing trailing newline")
	}
}

func TestReadStripsBOM(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bom.json")
	body, _ := json.Marshal(payload{Name: "bom", Count: 7})
	if err := os.WriteFile(path, append(append([]byte{}, utf8BOM...), body...), 0o644); err != nil {
		t.Fatal(err)
	}
	var got payload
	if err := ReadJSON(path, &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "bom" || got.Count != 7 {
		t.Fatalf("got %+v", got)
	}
}

func TestReadMissingAndEmpty(t *testing.T) {
	dir := t.TempDir()
	var v payload
	if err := ReadJSON(filepath.Join(dir, "missing.json"), &v); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: got %v, want fs.ErrNotExist", err)
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReadJSON(empty, &v); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty file: got %v, want ErrEmpty", err)
	}
}

// TestRenameRetryPredicates pins which errno values each path rides out:
// sharing/lock violations (32/33) everywhere, plus — rename only, Windows
// only — ERROR_ACCESS_DENIED (5), which MoveFileEx reports when the
// destination is held open without FILE_SHARE_DELETE (CRT/.NET/PowerShell
// readers). Reads must NOT retry errno 5: there it is a real permission error.
func TestRenameRetryPredicates(t *testing.T) {
	for _, errno := range []syscall.Errno{32, 33} {
		if !isSharingViolation(errno) || !isRenameRetryable(errno) {
			t.Errorf("errno %d must be retryable on both paths", errno)
		}
	}
	if isSharingViolation(syscall.Errno(5)) {
		t.Error("reads must not retry ERROR_ACCESS_DENIED")
	}
	if got, want := isRenameRetryable(syscall.Errno(5)), runtime.GOOS == "windows"; got != want {
		t.Errorf("isRenameRetryable(5) = %v on %s, want %v", got, runtime.GOOS, want)
	}
	// Wrapped the way os.Rename actually returns it.
	wrapped := &os.LinkError{Op: "rename", Old: "a", New: "b", Err: syscall.Errno(32)}
	if !isRenameRetryable(wrapped) {
		t.Error("wrapped sharing violation must be retryable")
	}
	if isRenameRetryable(errors.New("plain")) {
		t.Error("non-errno errors must not be retried")
	}
}

// TestConcurrentWritesNeverTearReads hammers one path with rewrites while a
// reader continuously parses it; atomic rename means every read must yield a
// complete, valid document.
func TestConcurrentWritesNeverTearReads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hot.json")
	big := payload{Name: string(bytes.Repeat([]byte("x"), 64*1024))}
	if err := WriteJSON(path, big); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			doc := big // per-goroutine copy; sharing big would race on Count
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				doc.Count = n*1_000_000 + i
				// Windows can transiently refuse the rename while the
				// reader holds the destination open; atomicity only
				// promises readers never see a torn file.
				_ = WriteJSON(path, doc)
			}
		}(w)
	}

	for i := 0; i < 200; i++ {
		var got payload
		if err := ReadJSON(path, &got); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("read %d failed: %v", i, err)
		}
		if len(got.Name) != 64*1024 {
			close(stop)
			wg.Wait()
			t.Fatalf("read %d saw torn content (len %d)", i, len(got.Name))
		}
	}
	close(stop)
	wg.Wait()
}
