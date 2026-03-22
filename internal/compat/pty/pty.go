package pty

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Session manages a process attached to a pseudo-terminal.
type Session struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	mu  sync.Mutex
	buf bytes.Buffer

	waitCh chan error
}

// Start launches cmd under a PTY with a fixed terminal size.
func Start(ctx context.Context, cmd *exec.Cmd, rows, cols uint16) (*Session, error) {
	_ = rows
	_ = cols

	args := append([]string{"-q", "/dev/null", cmd.Path}, cmd.Args[1:]...)
	wrapped := exec.CommandContext(ctx, "script", args...)
	wrapped.Dir = cmd.Dir
	wrapped.Env = cmd.Env

	in, err := wrapped.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := wrapped.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errOut, err := wrapped.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := wrapped.Start(); err != nil {
		return nil, err
	}

	s := &Session{
		cmd:    wrapped,
		in:     in,
		stdout: out,
		stderr: errOut,
		waitCh: make(chan error, 1),
	}

	readPipe := func(r io.ReadCloser) {
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				s.mu.Lock()
				_, _ = s.buf.Write(buf[:n])
				s.mu.Unlock()
			}
			if err != nil {
				if err != io.EOF {
					s.mu.Lock()
					_, _ = s.buf.WriteString(err.Error())
					s.mu.Unlock()
				}
				return
			}
		}
	}

	go readPipe(out)
	go readPipe(errOut)

	go func() {
		err := wrapped.Wait()
		s.waitCh <- err
		close(s.waitCh)
	}()

	go func() {
		<-ctx.Done()
		_ = out.Close()
		_ = errOut.Close()
	}()

	return s, nil
}

// WriteString sends bytes to the PTY.
func (s *Session) WriteString(text string) error {
	_, err := io.WriteString(s.in, text)
	return err
}

// Snapshot returns the bytes captured so far.
func (s *Session) Snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// WaitFor waits until the captured output contains substr.
func (s *Session) WaitFor(substr string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		got := s.Snapshot()
		if bytes.Contains([]byte(got), []byte(substr)) {
			return got, nil
		}
		select {
		case err, ok := <-s.waitCh:
			if !ok {
				return got, fmt.Errorf("process exited before %q appeared", substr)
			}
			return got, fmt.Errorf("process exited before %q appeared: %w", substr, err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	return s.Snapshot(), fmt.Errorf("timed out waiting for %q", substr)
}

// WaitExit waits for the process to terminate.
func (s *Session) WaitExit(timeout time.Duration) error {
	select {
	case err, ok := <-s.waitCh:
		if !ok {
			return nil
		}
		return err
	case <-time.After(timeout):
		return fmt.Errorf("timed out waiting for process exit")
	}
}

// Close tears down the PTY and process.
func (s *Session) Close() error {
	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.in.Close()
	_ = s.stdout.Close()
	return s.stderr.Close()
}
