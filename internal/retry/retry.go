// Package retry repeats a download that failed on the network, as a registry or a CDN that
// hiccups in CI, so the app does not turn into an error.
package retry

import (
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Delays are the waits before each new attempt; tests set them to zero.
var Delays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

// down is set once every attempt of a call failed: the network is out, and waiting again on
// each of the next hundreds of renders would only make the run slower.
var down atomic.Bool

// Reset forgets that the network was found down, for tests that expect a retry.
func Reset() { down.Store(false) }

// serverError is a gateway or an overloaded server; a 500 is a bug that repeats.
var serverError = regexp.MustCompile(`(?:status code |: )50[234]\b`)

// Transient reports whether err is a network failure that may pass, never a refusal such as
// 401, 403 or 404, which is the same on every attempt.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"dial tcp", "connection refused", "connection reset", "no such host", "i/o timeout",
		"network is unreachable", "TLS handshake timeout", "Client.Timeout", "unexpected EOF"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return serverError.MatchString(msg)
}

// Do runs f, and again after each of Delays while it fails with a transient error, unless an
// earlier call already found the network down.
func Do[T any](f func() (T, error)) (T, error) {
	v, err := f()
	if !Transient(err) || down.Load() {
		return v, err
	}
	for _, d := range Delays {
		time.Sleep(d)
		if v, err = f(); !Transient(err) {
			return v, err
		}
	}
	down.Store(true)
	return v, err
}
