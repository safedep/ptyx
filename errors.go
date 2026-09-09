package ptyx

import (
	"errors"
	"fmt"
)

var ErrMuxAlreadyStarted = errors.New("ptyx: mux already started")

// ErrCmdLineWithArgs reports SpawnOpts that carry both CmdLine and Args.
// Honouring one would silently drop the other.
var ErrCmdLineWithArgs = errors.New("ptyx: CmdLine and Args are mutually exclusive")

// ErrCmdLineUnsupported reports a non-empty CmdLine on a platform that has no
// Windows command line. Spawn fails instead of ignoring the field.
var ErrCmdLineUnsupported = errors.New("ptyx: CmdLine is supported on windows only")

type ExitError struct {
	ExitCode int
	waitStatus any
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("process exited with status %d", e.ExitCode)
}

func (e *ExitError) Sys() any {
	return e.waitStatus
}
