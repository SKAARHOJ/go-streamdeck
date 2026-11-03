package streamdeck

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// flakyHID fails the first `fail` writes (or all of them if fail < 0), like a
// USB controller that has no free transfer slot for a moment.
type flakyHID struct {
	*fakeHID
	fail     int32
	attempts int32
}

func (f *flakyHID) Write(b []byte) (int, error) {
	atomic.AddInt32(&f.attempts, 1)
	if n := atomic.LoadInt32(&f.fail); n != 0 {
		if n > 0 {
			atomic.AddInt32(&f.fail, -1)
		}
		return -1, errors.New("hidapi: unknown failure")
	}
	return f.fakeHID.Write(b)
}

func (f *flakyHID) SendFeatureReport(b []byte) (int, error) { return f.Write(b) }

func TestUSBWriteIsRetried(t *testing.T) {
	f := &flakyHID{fakeHID: newFakeHID(), fail: 3}
	d := newTestDevice(f)
	defer d.Close()

	d.enqueue(writeKey{kind: keyBrightness}, func(fd DeviceInterface) error {
		_, err := fd.SendFeatureReport([]byte{1, 2, 3})
		return err
	})
	if !d.Flush(time.Second) {
		t.Fatal("flush timed out")
	}
	if got := atomic.LoadInt32(&f.attempts); got != 4 {
		t.Errorf("attempts = %d, want 4 (3 failures, then success)", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 1 {
		t.Errorf("writes that went through = %d, want 1", len(f.writes))
	}
	if d.rt.writeErrs != 0 {
		t.Errorf("write errors = %d, want 0: a write that went through on retry isn't an error", d.rt.writeErrs)
	}
}

func TestUSBWriteGivesUp(t *testing.T) {
	f := &flakyHID{fakeHID: newFakeHID(), fail: -1}
	d := newTestDevice(f)
	defer d.Close()

	// keyNone writes are never queued again, so this tests the retries alone
	start := time.Now()
	d.enqueue(writeKey{}, func(fd DeviceInterface) error {
		_, err := fd.Write([]byte{1})
		return err
	})
	d.enqueue(writeKey{}, func(fd DeviceInterface) error {
		_, err := fd.Write([]byte{2})
		return err
	})
	if !d.Flush(time.Second) {
		t.Fatal("flush timed out")
	}
	if got := atomic.LoadInt32(&f.attempts); got != 2*usbWriteAttempts {
		t.Errorf("attempts = %d, want %d", got, 2*usbWriteAttempts)
	}
	if el := time.Since(start); el > 500*time.Millisecond {
		t.Errorf("giving up took %v", el)
	}
	// The first error is logged right away, the second is counted for the next report
	if d.rt.writeErrs != 1 || d.rt.writeErrLogged.IsZero() {
		t.Errorf("writeErrs = %d, logged at %v; want 1 counted after one logged", d.rt.writeErrs, d.rt.writeErrLogged)
	}
}

func TestCloseDuringRetriesIsQuick(t *testing.T) {
	f := &flakyHID{fakeHID: newFakeHID(), fail: -1}
	d := newTestDevice(f)
	for i := 0; i < 20; i++ {
		d.enqueue(writeKey{kind: keyButton, index: i}, func(fd DeviceInterface) error {
			_, err := fd.Write([]byte{1})
			return err
		})
	}
	time.Sleep(5 * time.Millisecond)
	start := time.Now()
	d.Close()
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Errorf("Close took %v while writes were failing", el)
	}
}

func TestNetworkWriteIsNotRetried(t *testing.T) {
	// Transports without ReadTimeout (network) fail fast: a failed write means the connection is gone
	f := &flakyNet{fail: -1}
	d := &Device{fd: f, deviceType: deviceType{name: "Test", numberOfButtons: 4}}
	d.start()
	defer d.Close()
	d.enqueue(writeKey{kind: keyBrightness}, func(fd DeviceInterface) error {
		_, err := fd.Write([]byte{1})
		return err
	})
	d.Flush(time.Second)
	if got := atomic.LoadInt32(&f.attempts); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

type flakyNet struct {
	fail     int32
	attempts int32
}

func (f *flakyNet) Close() error                            { return nil }
func (f *flakyNet) Read(b []byte) (int, error)              { select {} }
func (f *flakyNet) SendFeatureReport(b []byte) (int, error) { return f.Write(b) }
func (f *flakyNet) Write(b []byte) (int, error) {
	atomic.AddInt32(&f.attempts, 1)
	return -1, errors.New("broken pipe")
}

func TestFailedWriteIsRequeued(t *testing.T) {
	// Fails more often than a single write is retried, so it only goes through when it's queued again
	f := &flakyHID{fakeHID: newFakeHID(), fail: usbWriteAttempts + 2}
	d := newTestDevice(f)
	defer d.Close()
	d.enqueue(writeKey{kind: keyButton, index: 1}, func(fd DeviceInterface) error {
		_, err := fd.Write([]byte{7})
		return err
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		n := len(f.writes)
		f.mu.Unlock()
		if n == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the write never went through")
}

func TestRequeueDropsSuperseded(t *testing.T) {
	q := newWriteQueue()
	key := writeKey{kind: keyButton, index: 1}
	q.push(key, func(DeviceInterface) error { return nil })
	failed := q.pop()
	q.push(key, func(DeviceInterface) error { return nil }) // Newer content for the same key
	if q.requeue(failed) != requeueSuperseded {
		t.Error("a failed write was queued again although a newer one for the same key is pending")
	}
	other := &writeJob{key: writeKey{kind: keyButton, index: 2}}
	if q.requeue(other) != requeueQueued {
		t.Error("a failed write was not queued again")
	}
	other.requeued = maxRequeues
	q.pop()
	q.pop()
	if q.requeue(other) != requeueDropped {
		t.Error("a write was queued again more than maxRequeues times")
	}
}

func TestNoBrightnessWithoutPacket(t *testing.T) {
	// The Pedal has no lights and refuses a brightness request, so none is sent
	f := newFakeHID()
	d := &Device{fd: f, deviceType: deviceType{name: "Pedal", numberOfButtons: 3}}
	d.start()
	defer d.Close()
	d.SetBrightness(50)
	d.Flush(time.Second)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.writes) != 0 {
		t.Errorf("got %d writes, want none", len(f.writes))
	}
}
