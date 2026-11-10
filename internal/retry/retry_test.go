package retry

import (
	"errors"
	"testing"
	"time"
)

func TestTransient(t *testing.T) {
	for _, tt := range []struct {
		err  string
		want bool
	}{
		{`Get "https://raw.githubusercontent.com/x/pod.json": dial tcp 185.199.111.133:443: connect: connection refused`, true},
		{`failed to perform "Fetch" on source: Get "https://cdn01.quay.io/x": read tcp: connection reset by peer`, true},
		{`GET https://charts.example.com/index.yaml: 503 Service Unavailable`, true},
		{`GET "https://ghcr.io/v2/x/manifests/1.0.0": response status code 504: Gateway Time-out`, true},
		{`GET "https://ghcr.io/token?scope=x": response status code 403: denied: requested access to the resource is denied`, false},
		{`GET https://charts.example.com/x-1.0.0.tgz: 404 Not Found`, false},
		{`GET https://charts.example.com/index.yaml: 500 Internal Server Error`, false},
		{`chart "x" version "9.9.9" not found`, false},
		{`execution error at (app/templates/a.yaml:1:2): name is required`, false},
	} {
		if got := Transient(errors.New(tt.err)); got != tt.want {
			t.Errorf("Transient(%q) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestDoRetriesOnlyTransientFailures(t *testing.T) {
	saved := Delays
	Delays = []time.Duration{0, 0}
	defer func() { Delays = saved }()

	calls := 0
	v, err := Do(func() (int, error) {
		calls++
		if calls < 3 {
			return 0, errors.New("dial tcp: i/o timeout")
		}
		return 7, nil
	})
	if v != 7 || err != nil || calls != 3 {
		t.Errorf("a passing failure is retried: got %d, %v after %d calls", v, err, calls)
	}

	calls = 0
	_, err = Do(func() (int, error) {
		calls++
		return 0, errors.New("response status code 404: not found")
	})
	if err == nil || calls != 1 {
		t.Errorf("a refusal is not retried: %d calls", calls)
	}
}

func TestDoStopsWaitingOnceTheNetworkIsDown(t *testing.T) {
	saved := Delays
	Delays = []time.Duration{0, 0}
	defer func() { Delays = saved; Reset() }()

	calls := 0
	failing := func() (int, error) {
		calls++
		return 0, errors.New("dial tcp: connect: network is unreachable")
	}
	if _, err := Do(failing); err == nil || calls != 3 {
		t.Fatalf("the first failure is tried %d times, want 3", calls)
	}
	calls = 0
	if _, err := Do(failing); err == nil || calls != 1 {
		t.Errorf("after a call found the network down, the next ones try once: %d calls", calls)
	}
}
