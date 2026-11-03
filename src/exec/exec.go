// Package exec runs one child process under a deadline, bounded capture and an
// optional peak-memory measurement.
//
// It is the only place in the harness that starts a process, so the timeout
// discipline, the process-group kill and the capture ceilings hold for every
// caller rather than being re-derived at each site.
package exec

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"time"

	"lyxiang/benchsuite/src/peakmem"
)

// RunResult is one child process's outcome. Code is 127 when the process could
// not be spawned at all, so a missing binary flows into the gate branches like
// any other non-zero exit instead of needing a separate path.
type RunResult struct {
	Stdout   []byte
	Stderr   []byte
	Code     int
	TimedOut bool
	// StderrTruncated reports that the excess past maxCapturedStderr was dropped,
	// so a caller surfacing stderr can mark the diagnostic as clipped. Build
	// ignores it: there any stderr is already a failure signal, not a diagnostic.
	StderrTruncated bool
	// StdoutTruncated reports that Stdout is a prefix, not the whole answer.
	StdoutTruncated bool
	// PeakBytes is the child's peak resident set, or peakmem.Unknown when the
	// platform cannot report one or the read failed. It is measured only when the
	// caller asks, since it needs a handle held for the child's whole life.
	PeakBytes int64
}

// Run runs a child in its own process group and kills the whole group on timeout,
// so a solution's own children do not outlive it.
func Run(argv []string, input []byte, captureStdout bool, timeout time.Duration) RunResult {
	return RunIn("", argv, input, captureStdout, timeout)
}

// RunIn is Run with an explicit working directory, which a Go build needs: the
// module root decides which imports resolve. An empty dir keeps the caller's.
func RunIn(dir string, argv []string, input []byte, captureStdout bool, timeout time.Duration) RunResult {
	return run(dir, argv, input, captureStdout, timeout, false)
}

// RunMeasured is Run with the child's peak memory recorded as well. The handle is
// opened while the child is alive, so the measurement is the kernel's own
// high-water mark rather than a sample the harness happened to catch.
func RunMeasured(argv []string, input []byte, timeout time.Duration) RunResult {
	return run("", argv, input, false, timeout, true)
}

func run(dir string, argv []string, input []byte, captureStdout bool, timeout time.Duration, measurePeak bool) RunResult {
	if len(argv) == 0 {
		return RunResult{Code: 127, Stderr: []byte("no command"), PeakBytes: peakmem.Unknown}
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	command := exec.Command(argv[0], argv[1:]...)
	if dir != "" {
		command.Dir = dir
	}
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	// Stdout goes to a file, not a buffer, so a looping solution cannot grow the
	// heap for the life of the timeout. The read-back is bounded by
	// maxCapturedStdout and a longer output is reported as clipped.
	var stdoutFile *os.File
	if captureStdout {
		file, err := os.CreateTemp("", "bench-stdout-*")
		if err != nil {
			return RunResult{Code: 127, Stderr: []byte(err.Error()), PeakBytes: peakmem.Unknown}
		}
		defer func() { _ = os.Remove(file.Name()) }()
		stdoutFile = file
		command.Stdout = file
	}
	stderr := &limitedBuffer{limit: maxCapturedStderr}
	command.Stderr = stderr
	configureProcessGroup(command)
	// No environment dictionary is set: children inherit the parent environment.
	// CXX does not travel this way -- Build reads it with os.Getenv and puts it in
	// argv, so the child never needs to see it.

	if err := command.Start(); err != nil {
		if stdoutFile != nil {
			_ = stdoutFile.Close()
		}
		return RunResult{Code: 127, Stderr: []byte(err.Error()), PeakBytes: peakmem.Unknown}
	}
	// Opened here, not after Wait: the handle must address a live process, and the
	// kernel keeps the high-water mark that makes one late read sufficient.
	peak := peakmem.Unsupported()
	if measurePeak {
		peak = peakmem.Begin(command.Process.Pid)
	}
	defer peak.Release()

	// Without WaitDelay a grandchild holding stderr open keeps Wait blocked, so
	// the child would control how long the harness waits.
	command.WaitDelay = waitDelay
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	// waitErr is Wait's own verdict. A non-zero exit is not a failure here, so it
	// is only consulted when ProcessState cannot supply a code.
	timedOut := false
	var waitErr error
	select {
	case waitErr = <-done:
	case <-time.After(timeout):
		timedOut = true
		killProcessTree(command)
		waitErr = <-done
	}

	var stdout []byte
	stdoutTruncated := false
	if stdoutFile != nil {
		var err error
		stdout, stdoutTruncated, err = readBounded(stdoutFile, maxCapturedStdout)
		_ = stdoutFile.Close()
		if err != nil {
			// The child ran, so its exit code is the real one; leaving Code at zero
			// would report a harness failure as a clean exit.
			code := 127
			if command.ProcessState != nil {
				if c := command.ProcessState.ExitCode(); c >= 0 {
					code = c
				}
			}
			return RunResult{
				TimedOut:        timedOut,
				Code:            code,
				Stderr:          appendCaptureFailure(stderr.Bytes(), err.Error()),
				StderrTruncated: stderr.truncated,
				PeakBytes:       peakmem.Unknown,
			}
		}
	}

	result := RunResult{
		TimedOut:        timedOut,
		Stdout:          stdout,
		StdoutTruncated: stdoutTruncated,
		Stderr:          stderr.Bytes(),
		StderrTruncated: stderr.truncated,
		PeakBytes:       peakmem.Unknown,
	}
	if measurePeak {
		if value, ok := peak.Read(); ok {
			result.PeakBytes = value
		}
	}
	if !timedOut {
		// Wait's error covers the one case ProcessState cannot: the child left no
		// usable status at all. Otherwise the status itself is authoritative.
		var exitErr *exec.ExitError
		isExit := errors.As(waitErr, &exitErr)
		switch {
		case command.ProcessState != nil:
			result.Code = command.ProcessState.ExitCode()
			if result.Code < 0 {
				result.Code = 127
			}
		case isExit:
			result.Code = exitErr.ExitCode()
		default:
			result.Code = 127
		}
	}
	return result
}

// DefaultTimeout bounds one run and `--timeout` can shorten it; a
// compile gets the longer budget because cc1plus on a template-heavy solution
// takes seconds where the solution itself takes milliseconds.
const (
	DefaultTimeout = 60 * time.Second
)

const waitDelay = 2 * time.Second

// maxCapturedStdout bounds how much of a child's standard output is handed back.
// A solution that writes without end used to grow the harness's buffer until the
// deadline; writing to a file removes that, and this ceiling bounds the read-back
// so a child that produced gigabytes is clipped rather than allocated whole. A
// caller that sees StdoutTruncated must treat the bytes as a prefix, not as the
// answer.
const maxCapturedStdout = 16 << 20 // 16 MiB

// MaxCapturedStdout is the read-back ceiling a caller checks before treating a
// captured stream as the whole answer.
const MaxCapturedStdout = maxCapturedStdout

// readBounded reads at most `limit` bytes of an already-written file from its
// start, reporting whether anything past the limit was dropped.
func readBounded(file *os.File, limit int) ([]byte, bool, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, false, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, false, err
	}
	size := info.Size()
	truncated := size > int64(limit)
	if truncated {
		size = int64(limit)
	}
	data := make([]byte, size)
	// errors.Is rather than ==: a wrapper around ErrUnexpectedEOF still means the
	// file was shorter than its stat size, which is the case tolerated here.
	if _, err := io.ReadFull(file, data); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, truncated, err
	}
	return data, truncated, nil
}

// appendCaptureFailure records that stdout could not be read back, keeping
// whatever stderr the child already produced.
func appendCaptureFailure(out []byte, note string) []byte {
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return append(out, "bench: reading the captured stdout: "+note+"\n"...)
}

const maxCapturedStderr = 1 << 20 // 1 MiB

// MaxCapturedStderr is the ceiling a caller checks before reporting that a
// child's diagnostic output was clipped.
const MaxCapturedStderr = maxCapturedStderr

// limitedBuffer keeps the first `limit` bytes written to it and discards the
// rest, recording that it did. It exists so a captured stream has a ceiling
// without giving up the exit code or the process-group kill, which is what the
// unbounded alternative would cost.
type limitedBuffer struct {
	limit     int
	buf       bytes.Buffer
	truncated bool
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.limit - l.buf.Len(); room > 0 {
		if len(p) <= room {
			l.buf.Write(p)
		} else {
			l.buf.Write(p[:room])
			l.truncated = true
		}
	} else {
		l.truncated = true
	}
	// Report the full length: a short write would make the child's own write
	// fail or retry, turning our ceiling into the child's problem.
	return len(p), nil
}

func (l *limitedBuffer) Bytes() []byte { return l.buf.Bytes() }
