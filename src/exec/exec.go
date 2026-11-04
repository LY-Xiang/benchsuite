// Package exec runs one child process under a deadline, bounded capture and an
// optional peak-memory measurement.
//
// It is the only place in the harness that starts a process, so the timeout
// discipline, the process-group kill and the capture ceilings hold for every
// caller rather than being re-derived at each site.
package exec

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync/atomic"
	"time"
)

// RunResult is one child process's outcome. Code is 127 when the process could
// not be spawned at all, so a missing binary flows into the gate branches like
// any other non-zero exit instead of needing a separate path.
//
// It holds no measurement. What a child used is an observer's answer and arrives
// in Observed.Readings: this package knows a process, not a benchmark, and adding
// a field here for every thing worth measuring would make it the benchmark's
// struct instead of the process's.
//
// The bools sit at the tail so they pack into one word rather than each padding
// its neighbours.
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
}

// A Process is a child that has just started, handed to whatever wants to observe
// it.
//
// This package owns process creation, so it owns the one moment at which a handle
// can still be taken: a measurement module opening its own handle would race the
// child's exit. Rather than deciding here what is worth measuring, exec opens one
// handle with every privilege a reader could need and hands it out; each module
// then reads what it knows how to read. exec knows a process, not a benchmark.
//
// The handle stays valid until the child has been waited for, so every reader runs
// after the run finishes and sees final accounting.
type Process struct {
	PID int
	// Handle is the platform's own handle, already opened with the rights every
	// reader needs. It is a HANDLE on Windows and the pid on Unix, where the
	// accounting calls take a pid. Readers must not close it: Release does, once,
	// after every reader has run.
	Handle uintptr
	// Release drops the handle. It is set by openProcess and is nil only when the
	// child never started, which is why the caller checks it.
	Release func()
	// Sampler is the state a platform needs to sample a live child, for readings
	// the OS does not keep a high-water mark for. It is nil where one late read is
	// enough -- Windows keeps the peak in the kernel, Linux does not.
	Sampler any
}

// Run runs a child in its own process group and kills the whole group on timeout,
// so a solution's own children do not outlive it.
func Run(argv []string, input []byte, captureStdout bool, timeout time.Duration) RunResult {
	return RunIn("", argv, input, captureStdout, timeout)
}

// RunIn is Run with an explicit working directory, which a Go build needs: the
// module root decides which imports resolve. An empty dir keeps the caller's.
func RunIn(dir string, argv []string, input []byte, captureStdout bool, timeout time.Duration) RunResult {
	return run(dir, argv, input, captureStdout, timeout)
}

// Observer reads one fact about a child that has already exited.
//
// It runs after the wait, so the child's accounting is final: a peak is the whole
// peak, not the peak so far, and a counter has stopped. Returning false means the
// observation did not happen -- an unsupported platform, a failed syscall, a
// handle that no longer answers -- and the caller reports absence rather than a
// zero it cannot defend.
type Observer func(Process) (any, bool)

// RunProbed is Run with the child exposed to observers.
//
// The handle is opened once, immediately after start, with every privilege a
// reader needs; each observer then reads what it knows how to read and the handle
// is released after the last of them. That is the point of the seam: this package
// knows a process, and the modules that understand memory or CPU time do their own
// reading, without each having to open a handle of its own and race the exit.
//
// Observers see the child only after it has exited. Nothing observes a nil Process,
// and no observer runs when the child never started.
func RunProbed(argv []string, input []byte, timeout time.Duration, observe ...Observer) Observed {
	return runObserved("", argv, input, false, timeout, observe)
}

// Observed is one probed run: the result, plus whatever each observer reported,
// in the order the observers were given.
type Observed struct {
	RunResult
	// Readings holds one entry per observer. A nil entry is an observation that
	// did not happen; the caller checks it rather than reading a zero value.
	Readings []any
}

func run(dir string, argv []string, input []byte, captureStdout bool, timeout time.Duration) RunResult {
	return runObserved(dir, argv, input, captureStdout, timeout, nil).RunResult
}

func runObserved(dir string, argv []string, input []byte, captureStdout bool, timeout time.Duration,
	observe []Observer,
) Observed {
	if len(argv) == 0 {
		return Observed{Code: 127, Stderr: []byte("no command")}
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
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
			return Observed{Code: 127, Stderr: []byte(err.Error())}
		}
		defer func() { _ = os.Remove(file.Name()) }()
		stdoutFile = file
		command.Stdout = file
	}
	stderr := &limitedBuffer{limit: maxCapturedStderr}
	command.Stderr = stderr
	configureProcessGroup(command)

	// Cancel runs when the deadline lapses before the child exits. It kills the
	// whole tree and returns nil, so Wait then waits at most WaitDelay for the
	// capture pipes to drain instead of letting a grandchild hold them open. A
	// child that exits on its own never reaches it.
	//
	// The flag it sets is atomic. Cancel runs on exec's watchdog goroutine, and
	// os/exec does not document which goroutine's completion makes a plain write
	// visible to the caller after Wait; an atomic store is synchronised by
	// construction, so the flag is correct however the wait path hands over. It is
	// also the precise signal: the context alone would race a child that exited a
	// moment before its deadline.
	var timedOutFlag atomic.Bool
	job := &jobState{}
	command.Cancel = func() error {
		timedOutFlag.Store(true)
		killProcessTree(command, job)
		return nil
	}
	// Without WaitDelay a grandchild holding stderr open keeps Wait blocked, so
	// the child would control how long the harness waits.
	command.WaitDelay = waitDelay
	// No environment dictionary is set: children inherit the parent environment.
	// CXX does not travel this way -- Build reads it with os.Getenv and puts it in
	// argv, so the child never needs to see it.

	if err := command.Start(); err != nil {
		if stdoutFile != nil {
			_ = stdoutFile.Close()
		}
		return Observed{Code: 127, Stderr: []byte(err.Error())}
	}
	// Adopted right after start: the kill can only reach descendants that already
	// belong to the job or the process group.
	job.begin(command.Process)
	defer job.end()

	// Opened here, not after Wait: the handle must address a live process. The
	// kernel keeps the high-water mark and the accounting that make one late read
	// sufficient, so every observer runs after the wait and still sees final values.
	proc := openProcess(command.Process.Pid)
	if proc.Release != nil {
		defer proc.Release()
	}
	// The observers need what the wait produced, so the state is attached once it
	// exists rather than at open: a platform that reads rusage gets it from here,
	// and one that reads a handle ignores it.

	// waitErr is Wait's own verdict. A non-zero exit is not a failure here, so it
	// is only consulted when ProcessState cannot supply a code.
	waitErr := command.Wait()

	// The deadline decides the timeout through the flag Cancel set, not through
	// the context: a child that exited a moment before its deadline leaves the
	// flag clear even if the deadline fires while Wait is returning.
	timedOut := timedOutFlag.Load()

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
			return Observed{
				TimedOut:        timedOut,
				Code:            code,
				Stderr:          appendCaptureFailure(stderr.Bytes(), err.Error()),
				StderrTruncated: stderr.truncated}
		}
	}

	result := RunResult{
		TimedOut:        timedOut,
		Stdout:          stdout,
		StdoutTruncated: stdoutTruncated,
		Stderr:          stderr.Bytes(),
		StderrTruncated: stderr.truncated,
	}
	if !timedOut {
		// Wait's error covers the one case ProcessState cannot: the child left no
		// usable status at all. Otherwise the status itself is authoritative.
		exitErr, isExit := errors.AsType[*exec.ExitError](waitErr)
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

	// The wait's own record goes to the observers, and only when the child was not
	// killed. A killed child still has a ProcessState, but its rusage describes the
	// kill rather than the work: a peak measured across a SIGKILL says nothing about
	// what the solution needed, and a CPU time across it is the time until the
	// deadline. The nil is what a Unix observer checks before reading.
	if !timedOut {
		proc.Sampler = command.ProcessState
	}

	// Observers run last, after the wait, so the accounting they read is final.
	// Each entry is its own fact: a memory observer that failed says so without
	// affecting a CPU observer beside it.
	readings := make([]any, len(observe))
	for i, observer := range observe {
		if value, ok := observer(proc); ok {
			readings[i] = value
		}
	}
	return Observed{RunResult: result, Readings: readings}
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
