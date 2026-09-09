package ptyx

import (
	"errors"
	"io"
	"os"
)

type Console interface {
	In() io.Reader
	Out() io.Writer
	Err() *os.File
	IsATTYOut() bool
	Size() (int, int)
	MakeRaw() (RawState, error)
	Restore(RawState) error
	EnableVT()
	OnResize() <-chan struct{}
	Close() error
}

func IsErrNotAConsole(err error) bool { return errors.Is(err, ErrNotAConsole) }

type RawState interface{}

type Session interface {
	PtyReader() io.Reader
	PtyWriter() io.Writer
	Resize(cols, rows int) error
	Wait() error
	Kill() error
	Close() error
	Pid() int
	CloseStdin() error
}

type SpawnOpts struct {
	Prog string
	Args []string
	Env  []string
	Dir  string
	Cols int
	Rows int

	// CmdLine is the raw Windows command line for the child. Spawn passes it
	// to CreateProcess without any escaping, so the caller owns every quote.
	// Set it only when the CommandLineToArgvW rules that Args follows are the
	// wrong rules, for example to start a batch file through cmd.exe.
	//
	// Windows only. Every other platform rejects a non-empty CmdLine, so a
	// caller fails loudly instead of getting a different result per platform.
	// CmdLine and Args are mutually exclusive.
	CmdLine string
}

type Mux interface {
	Start(c Console, s Session) error
	Stop() error
}
