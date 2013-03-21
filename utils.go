package docker

import (
	"bytes"
	"container/list"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// HumanDuration returns a human-readable approximation of a duration
// (eg. "About a minute", "4 hours ago", etc.)
func HumanDuration(d time.Duration) string {
	if seconds := int(d.Seconds()); seconds < 1 {
		return "Less than a second"
	} else if seconds < 60 {
		return fmt.Sprintf("%d seconds", seconds)
	} else if minutes := int(d.Minutes()); minutes == 1 {
		return "About a minute"
	} else if minutes < 60 {
		return fmt.Sprintf("%d minutes", minutes)
	} else if hours := int(d.Hours()); hours == 1 {
		return "About an hour"
	} else if hours < 48 {
		return fmt.Sprintf("%d hours", hours)
	} else if hours < 24*7*2 {
		return fmt.Sprintf("%d days", hours/24)
	} else if hours < 24*30*3 {
		return fmt.Sprintf("%d weeks", hours/24/7)
	} else if hours < 24*365*2 {
		return fmt.Sprintf("%d months", hours/24/30)
	}
	return fmt.Sprintf("%d years", d.Hours()/24/365)
}

func Trunc(s string, maxlen int) string {
	if len(s) <= maxlen {
		return s
	}
	return s[:maxlen]
}

// Figure out the absolute path of our own binary
func SelfPath() string {
	path, err := exec.LookPath(os.Args[0])
	if err != nil {
		panic(err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		panic(err)
	}
	return path
}

type nopWriteCloser struct {
	io.Writer
}

func (w *nopWriteCloser) Close() error { return nil }

func NopWriteCloser(w io.Writer) io.WriteCloser {
	return &nopWriteCloser{w}
}

type bufReader struct {
	buf    *bytes.Buffer
	reader io.Reader
	err    error
	l      sync.Mutex
	wait   sync.Cond
}

func newBufReader(r io.Reader) *bufReader {
	reader := &bufReader{
		buf:    &bytes.Buffer{},
		reader: r,
	}
	reader.wait.L = &reader.l
	go reader.drain()
	return reader
}

func (r *bufReader) drain() {
	buf := make([]byte, 1024)
	for {
		n, err := r.reader.Read(buf)
		if err != nil {
			r.err = err
		} else {
			r.buf.Write(buf[0:n])
		}
		r.l.Lock()
		r.wait.Signal()
		r.l.Unlock()
		if err != nil {
			break
		}
	}
}

func (r *bufReader) Read(p []byte) (n int, err error) {
	for {
		n, err = r.buf.Read(p)
		if n > 0 {
			return n, err
		}
		if r.err != nil {
			return 0, r.err
		}
		r.l.Lock()
		r.wait.Wait()
		r.l.Unlock()
	}
	return
}

func (r *bufReader) Close() error {
	closer, ok := r.reader.(io.ReadCloser)
	if !ok {
		return nil
	}
	return closer.Close()
}

type writeBroadcaster struct {
	writers *list.List
}

func (w *writeBroadcaster) AddWriter(writer io.WriteCloser) {
	w.writers.PushBack(writer)
}

func (w *writeBroadcaster) RemoveWriter(writer io.WriteCloser) {
	for e := w.writers.Front(); e != nil; e = e.Next() {
		v := e.Value.(io.Writer)
		if v == writer {
			w.writers.Remove(e)
			return
		}
	}
}

func (w *writeBroadcaster) Write(p []byte) (n int, err error) {
	failed := []*list.Element{}
	for e := w.writers.Front(); e != nil; e = e.Next() {
		writer := e.Value.(io.Writer)
		if n, err := writer.Write(p); err != nil || n != len(p) {
			// On error, evict the writer
			failed = append(failed, e)
		}
	}
	// We cannot remove while iterating, so it has to be done in
	// a separate step
	for _, e := range failed {
		w.writers.Remove(e)
	}
	return len(p), nil
}

func (w *writeBroadcaster) Close() error {
	for e := w.writers.Front(); e != nil; e = e.Next() {
		writer := e.Value.(io.WriteCloser)
		writer.Close()
	}
	return nil
}

func newWriteBroadcaster() *writeBroadcaster {
	return &writeBroadcaster{list.New()}
}
