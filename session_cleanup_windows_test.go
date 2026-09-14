//go:build windows

package ptyx

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestWinCleanupHelper(t *testing.T) {
	if os.Getenv("PTYX_CLEANUP_HELPER") == "1" {
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}

func spawnCleanupSession(t *testing.T, ctx context.Context) Session {
	t.Helper()
	s, err := Spawn(ctx, SpawnOpts{
		Prog: os.Args[0],
		Args: []string{"-test.run=^TestWinCleanupHelper$"},
		Env:  append(os.Environ(), "PTYX_CLEANUP_HELPER=1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	reader := s.PtyReader()
	go io.Copy(io.Discard, reader)
	return s
}

func TestWinSession_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := spawnCleanupSession(t, ctx)
	cancel()
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	select {
	case err := <-done:
		var exitErr *ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode != 1 {
			t.Fatalf("Wait() = %v, want exit code 1 after cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}
}

func TestWinSession_ConcurrentShutdown(t *testing.T) {
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		s := spawnCleanupSession(t, ctx)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, action := range []func(){
			cancel,
			func() { _ = s.Kill() },
			func() { _ = s.Close() },
			func() {
				err := s.Wait()
				var exitErr *ExitError
				if err != nil && !errors.As(err, &exitErr) {
					t.Errorf("Wait failed during shutdown: %v", err)
				}
			},
		} {
			wg.Add(1)
			go func(action func()) {
				defer wg.Done()
				<-start
				action()
			}(action)
		}
		close(start)
		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent shutdown did not complete")
		}
	}
}

func TestWinSession_CancelAfterClose(t *testing.T) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	prog := filepath.Join(dir, "cmd.exe")
	line, err := windows.UTF16PtrFromString(`"` + prog + `" /d /c exit 0`)
	if err != nil {
		t.Fatal(err)
	}
	startup := windows.StartupInfo{Cb: uint32(unsafe.Sizeof(windows.StartupInfo{}))}
	victim := new(windows.ProcessInformation)
	if err := windows.CreateProcess(nil, line, nil, nil, false, windows.CREATE_SUSPENDED, nil, nil, &startup, victim); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(victim.Thread)
	defer windows.CloseHandle(victim.Process)
	defer windows.TerminateProcess(victim.Process, 99)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session, err := Spawn(ctx, SpawnOpts{Prog: prog, Args: []string{"/d", "/c", "exit", "0"}, Cols: 80, Rows: 25})
	if err != nil {
		t.Fatal(err)
	}
	oldHandle := session.(*winSession).process
	oldPID := session.Pid()
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, session.PtyReader())
		close(drained)
	}()
	if err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	<-drained

	var aliases []windows.Handle
	defer func() {
		for _, handle := range aliases {
			windows.CloseHandle(handle)
		}
	}()
	reused := false
	for i := 0; i < 16384; i++ {
		var alias windows.Handle
		if err := windows.DuplicateHandle(windows.CurrentProcess(), victim.Process, windows.CurrentProcess(), &alias, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			t.Fatal(err)
		}
		aliases = append(aliases, alias)
		if alias == oldHandle {
			reused = true
			break
		}
	}
	if !reused {
		t.Fatal("Could not reproduce handle reuse")
	}
	currentPID, err := windows.GetProcessId(oldHandle)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Closed session PID %d handle %d now refers to unrelated suspended PID %d", oldPID, oldHandle, currentPID)
	status, err := windows.WaitForSingleObject(victim.Process, 100)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("Victim must be alive before cancellation: status=%d err=%v", status, err)
	}
	cancel()
	status, err = windows.WaitForSingleObject(victim.Process, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if status == windows.WAIT_OBJECT_0 {
		var code uint32
		if err := windows.GetExitCodeProcess(victim.Process, &code); err != nil {
			t.Fatal(err)
		}
		t.Fatalf("cancel after Close killed unrelated PID %d through reused handle %d, exit code %d", currentPID, oldHandle, code)
	}
	if status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("unexpected wait status %d", status)
	}
}
