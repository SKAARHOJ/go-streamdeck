package streamdeck

import (
	"errors"
	"image/color"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHID is a USB-like transport: it reads with a timeout, and records if it's
// closed while a read or write is in progress (which crashes hidapi).
type fakeHID struct {
	busy            int32
	closed          int32
	closedWhileBusy int32
	events          chan []byte
	readErr         chan error

	mu     sync.Mutex
	writes [][]byte
}

func newFakeHID() *fakeHID {
	return &fakeHID{events: make(chan []byte, 10), readErr: make(chan error, 1)}
}

func (f *fakeHID) enter() { atomic.AddInt32(&f.busy, 1) }
func (f *fakeHID) leave() { atomic.AddInt32(&f.busy, -1) }

func (f *fakeHID) Close() error {
	if atomic.LoadInt32(&f.busy) > 0 {
		atomic.StoreInt32(&f.closedWhileBusy, 1)
	}
	atomic.StoreInt32(&f.closed, 1)
	return nil
}

func (f *fakeHID) SendFeatureReport(b []byte) (int, error) { return f.Write(b) }

func (f *fakeHID) Write(b []byte) (int, error) {
	f.enter()
	defer f.leave()
	time.Sleep(time.Millisecond)
	f.mu.Lock()
	f.writes = append(f.writes, append([]byte(nil), b...))
	f.mu.Unlock()
	return len(b), nil
}

func (f *fakeHID) Read(b []byte) (int, error) { panic("USB devices must be read with ReadTimeout") }

func (f *fakeHID) ReadTimeout(b []byte, ms int) (int, error) {
	f.enter()
	defer f.leave()
	select {
	case e := <-f.events:
		return copy(b, e), nil
	case err := <-f.readErr:
		return 0, err
	case <-time.After(time.Duration(ms) * time.Millisecond):
		return 0, nil
	}
}

func newTestDevice(fd DeviceInterface) *Device {
	d := &Device{fd: fd, deviceType: deviceType{name: "Test", numberOfButtons: 4, buttonReadOffset: 1}}
	d.start()
	d.startReader()
	return d
}

func drain(q *writeQueue) {
	for job := q.pop(); job != nil; job = q.pop() {
		job.run(nil)
	}
}

func TestQueueReplacesPendingWrites(t *testing.T) {
	q := newWriteQueue()
	var got []string
	add := func(key writeKey, name string) {
		q.push(key, func(DeviceInterface) error { got = append(got, name); return nil })
	}
	add(writeKey{kind: keyButton, index: 1}, "b1-old")
	add(writeKey{kind: keyButton, index: 2}, "b2")
	add(writeKey{kind: keyButton, index: 1}, "b1-new")
	add(writeKey{}, "reset")
	add(writeKey{kind: keyButton, index: 1}, "b1-after-reset")
	add(writeKey{kind: keyButton, index: 1}, "b1-last")
	drain(q)

	want := []string{"b1-new", "b2", "reset", "b1-last"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestCloseWaitsForReaderAndWriter(t *testing.T) {
	f := newFakeHID()
	d := newTestDevice(f)
	var closedErr atomic.Value
	d.ButtonPress(func(i int, _ *Device, err error, _ bool) {
		if err != nil {
			closedErr.Store(err)
		}
	})
	for i := 0; i < 50; i++ {
		d.SetBrightness(i)
		d.WriteColorToEncoder(i%2, color.Black)
	}
	d.Close()

	if atomic.LoadInt32(&f.closed) != 1 {
		t.Fatal("not closed")
	}
	if atomic.LoadInt32(&f.closedWhileBusy) == 1 {
		t.Fatal("closed while reading or writing")
	}
	f.mu.Lock()
	n := len(f.writes)
	f.mu.Unlock()
	if n < 3 || n > 10 { // Mostly replaced while waiting; at least brightness and the two encoders
		t.Fatalf("got %d writes, want 3-10 of 100", n)
	}
	time.Sleep(10 * time.Millisecond)
	if err, _ := closedErr.Load().(error); err != ErrClosed {
		t.Fatalf("listener got %v, want ErrClosed", err)
	}
	d.Close() // Twice is fine
}

func TestCloseFromErrorListener(t *testing.T) {
	f := newFakeHID()
	d := newTestDevice(f)
	closed := make(chan struct{})
	d.ButtonPress(func(i int, dev *Device, err error, _ bool) {
		if err != nil {
			dev.Close() // As xpanel-streamdeck does when a paired device disconnects
			close(closed)
		}
	})
	f.readErr <- errors.New("unplugged")

	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close from the error listener blocked")
	}
	if atomic.LoadInt32(&f.closedWhileBusy) == 1 {
		t.Fatal("closed while reading or writing")
	}
}

func TestCloseFromInputListener(t *testing.T) {
	f := newFakeHID()
	d := newTestDevice(f)
	returned := make(chan struct{})
	d.ButtonPress(func(i int, dev *Device, err error, pressed bool) {
		if err == nil && pressed {
			dev.Close() // Can't wait for the reader (it's this goroutine), so it finishes in the background
			close(returned)
		}
	})
	ev := make([]byte, 8)
	ev[0], ev[1] = 1, 1 // Button 0 pressed
	time.Sleep(150 * time.Millisecond) // Presses right after opening are ignored (debouncing)
	f.events <- ev

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Close from an input listener didn't return")
	}
	deadline := time.Now().Add(time.Second)
	for atomic.LoadInt32(&f.closed) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&f.closed) != 1 {
		t.Fatal("not closed")
	}
	if atomic.LoadInt32(&f.closedWhileBusy) == 1 {
		t.Fatal("closed while reading or writing")
	}
}

func TestFlush(t *testing.T) {
	f := newFakeHID()
	d := newTestDevice(f)
	defer d.Close()
	d.SetBrightness(10)
	if !d.Flush(time.Second) {
		t.Fatal("flush timed out")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 1 {
		t.Fatalf("got %d writes, want 1", len(f.writes))
	}
}
