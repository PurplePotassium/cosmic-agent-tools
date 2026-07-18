package statedir

import "os"

// LockFile takes a blocking exclusive OS lock (flock / LockFileEx) on path,
// creating the file if needed, and returns the release func. The file itself
// is never removed — deleting a lock file reintroduces the very races an OS
// lock exists to prevent (a waiter blocked on a deleted inode/handle holds a
// lock nobody else can see).
func LockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}, nil
}
