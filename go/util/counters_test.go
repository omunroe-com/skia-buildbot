package util

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"testing"
	"time"

	"go.skia.org/infra/go/testutils"

	assert "github.com/stretchr/testify/require"
)

// mockTime is a struct used for faking the passage of time. It keeps a current
// time which can be advanced using the Sleep or Tick methods. When either of
// the above are used, any funcs provided to AfterFunc are run if their duration
// has expired.
type mockTime struct {
	t0         time.Time
	t          time.Time
	afterFuncs map[time.Time][]func()
}

// Return a mockTime instance.
func newMockTime() *mockTime {
	now := time.Now()
	return &mockTime{
		t0:         now,
		t:          now,
		afterFuncs: map[time.Time][]func(){},
	}
}

// Equivalent to time.Now(), but returns the mocked current time.
func (t *mockTime) Now() time.Time {
	return t.t
}

// Equivalent to time.AfterFunc, runs the func when the mocked current time
// passes the given duration.
func (t *mockTime) AfterFunc(d time.Duration, fn func()) *time.Timer {
	runAt := t.t.Add(d)
	t.afterFuncs[runAt] = append(t.afterFuncs[runAt], fn)
	return nil // We don't use the returned Timer.
}

// Skip ahead the given duration and run any AfterFuncs, synchronously as
// opposed to in a goroutine, so that the caller doesn't have to wait.
func (t *mockTime) Sleep(d time.Duration) {
	t.t = t.t.Add(d)
	for after, funcs := range t.afterFuncs {
		if t.t.After(after) {
			for _, fn := range funcs {
				fn()
			}
			delete(t.afterFuncs, after)
		}
	}
}

// Sleep for the given duration, then run the given func, repeatedly until
// the func returns false.
func (t *mockTime) Tick(d time.Duration, fn func() bool) {
	for {
		t.Sleep(d)
		if !fn() {
			return
		}
	}
}

// Return the mock elapsed time since the mockTime was created. Useful for
// debugging timelines.
func (t *mockTime) Elapsed() time.Duration {
	return t.t.Sub(t.t0)
}

func TestPersistentAutoDecrementCounter(t *testing.T) {
	testutils.MediumTest(t)

	mt := newMockTime()
	timeAfterFunc = mt.AfterFunc
	timeNowFunc = mt.Now
	counters := []*PersistentAutoDecrementCounter{}
	defer func() {
		timeAfterFunc = time.AfterFunc
		timeNowFunc = time.Now
	}()

	w, err := ioutil.TempDir("", "")
	assert.NoError(t, err)
	defer testutils.RemoveAll(t, w)

	f := path.Join(w, "counter")
	d := 200 * time.Millisecond

	newCounter := func() *PersistentAutoDecrementCounter {
		backingFile := fmt.Sprintf("%s_%d", f, len(counters))
		if len(counters) > 0 {
			// Copy the backing file from the first counter, rather
			// than reusing it, to prevent contention.
			firstCounterFile := fmt.Sprintf("%s_%d", f, 0)
			contents, err := ioutil.ReadFile(firstCounterFile)
			assert.NoError(t, err)
			assert.NoError(t, ioutil.WriteFile(backingFile, contents, os.ModePerm))
		}
		rv, err := NewPersistentAutoDecrementCounter(backingFile, d)
		assert.NoError(t, err)
		counters = append(counters, rv)
		return rv
	}

	c := newCounter()
	assert.Equal(t, int64(0), c.Get())
	assert.NoError(t, c.Inc())
	assert.Equal(t, int64(1), c.Get())

	mt.Sleep(time.Duration(0.5 * float64(d)))

	assert.NoError(t, c.Inc())
	assert.Equal(t, int64(2), c.Get())

	c2 := newCounter()
	assert.Equal(t, int64(2), c2.Get())

	mt.Sleep(d)
	assert.Equal(t, int64(1), c.Get())
	assert.Equal(t, int64(1), c2.Get())
	mt.Sleep(d)

	assert.Equal(t, int64(0), c.Get())
	assert.Equal(t, int64(0), c2.Get())

	c3 := newCounter()
	assert.Equal(t, int64(0), c3.Get())

	i := 0
	mt.Tick(time.Duration(float64(d)/float64(4)), func() bool {
		assert.Equal(t, int64(i), c.Get())
		assert.NoError(t, c.Inc())
		if i == 2 {
			return false
		}
		i++
		return true
	})
	mt.Sleep(time.Duration(float64(d) / float64(8)))
	expect := int64(3)

	mt.Tick(time.Duration(float64(d)/float64(4)), func() bool {
		assert.Equal(t, expect, c.Get())
		c4 := newCounter()
		assert.Equal(t, expect, c4.Get())
		if expect == 0 {
			return false
		}
		expect--
		return true
	})

	// Test the Reset() functionality.
	assert.NoError(t, c.Inc())
	assert.Equal(t, int64(1), c.Get())
	c2 = newCounter()
	assert.Equal(t, int64(1), c2.Get())
	assert.NoError(t, c.Reset())
	assert.Equal(t, int64(0), c.Get())
	c2 = newCounter()
	assert.Equal(t, int64(0), c2.Get())

	// Ensure that we don't go negative or crash.
	mt.Sleep(time.Duration(1.5 * float64(d)))
	assert.Equal(t, int64(0), c.Get())
}
