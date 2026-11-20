package fake

import (
	"errors"
	"syscall"
)

// errorSharingViolation is what Windows reports for a file another process
// still has open.
const errorSharingViolation syscall.Errno = 32

// held reports whether err means the continue file is still open in the
// process that touched it: removing it can succeed on a later poll.
func held(err error) bool { return errors.Is(err, errorSharingViolation) }
