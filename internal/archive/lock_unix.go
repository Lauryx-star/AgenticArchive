//go:build darwin || linux

package archive

import (
	"errors"
	"os"
	"syscall"
)

var ErrWorkerBusy = errors.New("another worker is already processing this index")

// The OS releases this lock on process death, allowing immediate crash recovery.
func acquireWorkerLock(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrWorkerBusy
		}
		return nil, err
	}
	return func() { syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }, nil
}
