//go:build windows

package ptyx

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestWinDrainHelper(t *testing.T) {
	mode := os.Getenv("PTYX_DRAIN_MODE")
	if mode == "" {
		return
	}
	if err := os.WriteFile(os.Getenv("PTYX_DRAIN_READY"), []byte("ready"), 0600); err != nil {
		os.Exit(2)
	}
	switch mode {
	case "flood":
		for {
			if _, err := fmt.Fprintln(os.Stdout, strings.Repeat("output", 100)); err != nil {
				os.Exit(3)
			}
		}
	case "gated":
		for {
			if _, err := os.Stat(os.Getenv("PTYX_DRAIN_GATE")); err == nil {
				os.Exit(0)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func spawnDrainSession(t *testing.T, mode string) (Session, string) {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	gate := filepath.Join(dir, "gate")
	s, err := Spawn(context.Background(), SpawnOpts{
		Prog: os.Args[0],
		Args: []string{"-test.run=^TestWinDrainHelper$"},
		Env:  append(os.Environ(), "PTYX_DRAIN_MODE="+mode, "PTYX_DRAIN_READY="+ready, "PTYX_DRAIN_GATE="+gate),
	})
	if err != nil {
		t.Fatal(err)
	}
	reader := s.PtyReader()
	t.Cleanup(func() {
		go io.Copy(io.Discard, reader)
		_ = s.Close()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			return s, gate
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWinSession_CloseStdinAfterClose(t *testing.T) {
	s, gate := spawnDrainSession(t, "gated")
	drained := make(chan struct{})
	reader := s.PtyReader()
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(drained)
	}()
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	<-drained
	if err := s.CloseStdin(); err != nil {
		t.Fatalf("CloseStdin after Close changed from a no-op to an error: %v", err)
	}
}

func TestWinSession_CloseAfterStdinEOF(t *testing.T) {
	s, gate := spawnDrainSession(t, "gated")
	drained := make(chan struct{})
	reader := s.PtyReader()
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(drained)
	}()
	if err := s.CloseStdin(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitErr := s.Wait()
	<-drained
	// Let the automatic console cleanup finish before the explicit Close.
	time.Sleep(100 * time.Millisecond)
	closeErr := s.Close()
	t.Logf("Wait=%v Close=%v", waitErr, closeErr)
	if closeErr != nil {
		t.Fatalf("Close reports an already-closed stdin as a cleanup failure: %v", closeErr)
	}
}

func TestWinSession_KillWhileCloseDrains(t *testing.T) {
	if windows.RtlGetVersion().BuildNumber >= 26100 {
		t.Skip("ClosePseudoConsole no longer waits for output drain on this Windows version")
	}
	s, _ := spawnDrainSession(t, "flood")
	reader := s.PtyReader()
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(s.Pid()))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process)
	// Leave the output pipe backed up while Close shuts down the console.
	time.Sleep(200 * time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	status, err := windows.WaitForSingleObject(process, 3000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		go io.Copy(io.Discard, reader)
		t.Fatalf("Close did not terminate the helper: status=%d err=%v", status, err)
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
		t.Fatal("Close returned before the output pipe was drained")
	case <-time.After(100 * time.Millisecond):
	}
	drained := make(chan struct{})
	go func() {
		_ = s.Kill()
		_, _ = io.Copy(io.Discard, reader)
		close(drained)
	}()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
		<-drained
	case <-time.After(2 * time.Second):
		// A second reader breaks the deadlock so the test can clean up.
		rescued := make(chan struct{})
		go func() {
			_, _ = io.Copy(io.Discard, reader)
			close(rescued)
		}()
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatal("Close remained blocked after rescue reader started")
		}
		<-drained
		<-rescued
		t.Fatal("Close holds the session lock while waiting for output, so the reader blocks in Kill before it can drain")
	}
}
