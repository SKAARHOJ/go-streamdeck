package streamdeck

import (
	"errors"
	"math/rand"
	"sync"
	"time"

	log "github.com/s00500/env_logger"
)

// Each device has one writer goroutine that sends everything to it, in order.
// This keeps writes to the device from overlapping, and callers never block on
// slow or stuck hardware. A write replaces a pending write of the same kind
// (same button, same area, brightness, ...), so a busy device shows the newest
// content instead of falling behind.
//
// Closing is safe while the device is being read or written: hidapi crashes if
// a device is closed during a read or write, so for USB devices the reader and
// the writer are stopped first, and the device is closed after that.

// ErrClosed is reported to the ButtonPress listeners when the device is closed by the application
var ErrClosed = errors.New("stream deck closed")

const (
	readPollInterval   = 100 // ms; how often a USB reader checks if the device is being closed
	closeFlushTimeout  = time.Second
	closeReturnTimeout = 2 * time.Second

	// A USB write that fails is tried again, after about 1, 2, 4, 8, 16, 20 and
	// 20 ms (each delay with up to 50% random added, so Stream Decks that failed
	// together don't retry together). With several Stream Decks on one USB
	// controller, a write fails when the controller has no room for it at that
	// moment, for instance when all of them are written at once (on Linux the
	// kernel logs "usbfs: usb_submit_urb returned -28"). A moment later it goes through.
	usbWriteAttempts      = 8
	usbWriteRetryDelay    = time.Millisecond
	usbWriteRetryMaxDelay = 20 * time.Millisecond

	requeueDelay        = 10 * time.Millisecond
	writeErrLogInterval = time.Minute // Write errors after the first are logged at most this often
)

// timeoutReader is implemented by transports that can read with a timeout (USB HID).
// Such transports must not be closed while a read is in progress.
type timeoutReader interface {
	ReadTimeout([]byte, int) (int, error)
}

// writeKey tells what a queued write updates. A write with the same key replaces a pending one.
type writeKey struct {
	kind       byte
	index      int
	x, y, w, h int
}

const (
	keyNone byte = iota // Never replaced; writes queued after it are not merged into ones before it
	keyButton
	keyArea
	keyBrightness
	keyEncoderColor
	keyEncoderRing
)

type writeJob struct {
	key      writeKey
	run      func(fd DeviceInterface) error
	requeued int // How often it failed and was queued again
}

// A USB write that failed, also after retries, is queued again, up to this many times, unless a newer write of
// the same kind is pending: so the device ends up showing the newest content even if writes fail now and then.
// (On the network, a failed write means the connection is gone.)
const maxRequeues = 10

type writeQueue struct {
	mu      sync.Mutex
	jobs    []*writeJob
	pending map[writeKey]*writeJob
	wake    chan struct{}
}

func newWriteQueue() *writeQueue {
	return &writeQueue{
		pending: map[writeKey]*writeJob{},
		wake:    make(chan struct{}, 1),
	}
}

func (q *writeQueue) push(key writeKey, run func(DeviceInterface) error) {
	q.mu.Lock()
	if job, ok := q.pending[key]; ok && key.kind != keyNone {
		job.run = run // Keeps its place in the queue, so a key that changes all the time is still sent
		q.mu.Unlock()
		return
	}
	job := &writeJob{key: key, run: run}
	q.jobs = append(q.jobs, job)
	if key.kind == keyNone {
		q.pending = map[writeKey]*writeJob{}
	} else {
		q.pending[key] = job
	}
	q.mu.Unlock()

	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// What requeue did with a failed job
const (
	requeueDropped    = iota // Given up on: the write is lost
	requeueQueued            // Queued again, at the end
	requeueSuperseded        // Not needed: a newer write of the same kind is pending
)

// requeue queues a failed job again, unless a newer job with its key is pending.
func (q *writeQueue) requeue(job *writeJob) int {
	if job.key.kind == keyNone || job.requeued >= maxRequeues {
		return requeueDropped
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.pending[job.key]; ok {
		return requeueSuperseded
	}
	job.requeued++
	q.jobs = append(q.jobs, job)
	q.pending[job.key] = job
	return requeueQueued
}

func (q *writeQueue) pop() *writeJob {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) == 0 {
		return nil
	}
	job := q.jobs[0]
	q.jobs[0] = nil
	q.jobs = q.jobs[1:]
	if q.pending[job.key] == job {
		delete(q.pending, job.key)
	}
	return job
}

// deviceRuntime holds what a device with hardware behind it needs while open
type deviceRuntime struct {
	queue      *writeQueue
	closing    chan struct{}
	closeOnce  sync.Once
	writerDone chan struct{}
	readerDone chan struct{}

	// Used by the writer goroutine only
	writeErrs      int // Failed writes not logged yet
	writeErrLogged time.Time
}

// start starts the writer. Call it once d.fd is set, before anything is written.
func (d *Device) start() {
	d.rt = &deviceRuntime{
		queue:      newWriteQueue(),
		closing:    make(chan struct{}),
		writerDone: make(chan struct{}),
		readerDone: make(chan struct{}),
	}
	close(d.rt.readerDone) // Until a reader is started
	go d.writeLoop()
}

// startReader starts listening for input from the device.
func (d *Device) startReader() {
	d.rt.readerDone = make(chan struct{})
	go d.eventListener()
}

func (d *Device) writeLoop() {
	rt := d.rt
	defer close(rt.writerDone)
	fd := d.fd
	_, usb := fd.(timeoutReader)
	if usb {
		fd = retryingWriter{DeviceInterface: fd, closing: rt.closing}
	}
	for {
		if job := rt.queue.pop(); job != nil {
			if err := job.run(fd); err != nil {
				what := requeueDropped
				if usb {
					what = rt.queue.requeue(job)
				}
				switch what {
				case requeueQueued:
					// Give the device (or the USB controller) a moment before the queue reaches it again
					select {
					case <-time.After(requeueDelay):
					case <-rt.closing:
					}
				case requeueDropped:
					rt.writeFailed(d, err)
				}
			}
			continue
		}
		select {
		case <-rt.queue.wake:
		case <-rt.closing:
			for job := rt.queue.pop(); job != nil; job = rt.queue.pop() { // Send what is left, then stop
				job.run(d.fd)
			}
			return
		}
	}
}

// writeFailed logs a failed write: the first one right away, later ones counted, at most once per writeErrLogInterval.
func (rt *deviceRuntime) writeFailed(d *Device, err error) {
	rt.writeErrs++
	if time.Since(rt.writeErrLogged) < writeErrLogInterval {
		return
	}
	if rt.writeErrs == 1 {
		log.Warnf("Writing to Stream Deck %s failed: %v\n", d.GetSerial(), err)
	} else {
		log.Warnf("Writing to Stream Deck %s failed %d times since the last report: %v\n", d.GetSerial(), rt.writeErrs, err)
	}
	rt.writeErrs = 0
	rt.writeErrLogged = time.Now()
}

// retryingWriter tries failed writes to a USB device again (see usbWriteAttempts), until the device is closing.
type retryingWriter struct {
	DeviceInterface
	closing chan struct{}
}

func (r retryingWriter) Write(b []byte) (int, error) {
	return r.retry(func() (int, error) { return r.DeviceInterface.Write(b) })
}

func (r retryingWriter) SendFeatureReport(b []byte) (int, error) {
	return r.retry(func() (int, error) { return r.DeviceInterface.SendFeatureReport(b) })
}

func (r retryingWriter) retry(write func() (int, error)) (n int, err error) {
	delay := usbWriteRetryDelay
	for attempt := 1; ; attempt++ {
		if n, err = write(); err == nil || attempt == usbWriteAttempts {
			return n, err
		}
		select {
		case <-r.closing: // Unplugged or closed: don't hold up closing
			return n, err
		case <-time.After(delay + time.Duration(rand.Int63n(int64(delay)/2+1))):
		}
		if delay *= 2; delay > usbWriteRetryMaxDelay {
			delay = usbWriteRetryMaxDelay
		}
	}
}

func (rt *deviceRuntime) isClosing() bool {
	select {
	case <-rt.closing:
		return true
	default:
		return false
	}
}

// enqueue queues a write to the device. Writes are dropped once the device is closing.
func (d *Device) enqueue(key writeKey, run func(DeviceInterface) error) {
	if d.rt == nil || d.rt.isClosing() {
		return
	}
	d.rt.queue.push(key, run)
}

// readInput reads the next input report. USB reads poll, so they can stop when the device is closing.
func (d *Device) readInput(data []byte) (int, error) {
	if tr, ok := d.fd.(timeoutReader); ok {
		for {
			if d.rt.isClosing() {
				return 0, ErrClosed
			}
			n, err := tr.ReadTimeout(data, readPollInterval)
			if err != nil || n > 0 {
				return n, err
			}
		}
	}
	return d.fd.Read(data)
}

// Flush waits until everything written so far has been sent to the device.
// It returns false if that didn't happen within timeout, or if the device was closed.
func (d *Device) Flush(timeout time.Duration) bool {
	if d.rt == nil {
		return true // Virtual device
	}
	done := make(chan struct{})
	d.enqueue(writeKey{}, func(DeviceInterface) error {
		close(done)
		return nil
	})
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-d.rt.closing:
		return false
	case <-timer.C:
		return false
	}
}

// Close the device. Pending writes are sent first (for up to a second on network devices).
// It is safe to call at any time, from any goroutine, also more than once.
func (d *Device) Close() {
	if d.rt == nil { // Virtual device
		return
	}
	rt := d.rt
	rt.closeOnce.Do(func() {
		close(rt.closing)

		done := make(chan struct{})
		go func() {
			if _, ok := d.fd.(timeoutReader); ok {
				// USB: hidapi must not close the device while it's being read or written
				<-rt.writerDone
				<-rt.readerDone
			} else {
				// Network: closing is always safe, and it also stops a blocked reader or writer
				select {
				case <-rt.writerDone:
				case <-time.After(closeFlushTimeout):
				}
			}
			d.fd.Close()
			if d.parent != nil {
				d.parent.Close()
			}
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(closeReturnTimeout):
			// Most likely Close was called from an input callback, so the reader can't stop until it returns.
			// The device is closed as soon as the reader and writer stop.
			log.Debugf("Stream Deck %s is closed in the background\n", d.GetSerial())
		}
	})
}
