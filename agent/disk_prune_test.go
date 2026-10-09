package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": {"application/json"}},
	}
}

// fakeDocker answers the prune and disk usage endpoints and records each call.
type fakeDocker struct {
	sync.Mutex
	calls  []*http.Request
	freed  map[string]int64 // by path, e.g. "/v1.41/images/prune"
	fail   map[string]bool
	gate   chan struct{} // if set, prune calls wait for it to close
	called chan struct{} // receives once for each prune call, if set
}

func (f *fakeDocker) manager() *dockerManager {
	return &dockerManager{client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		f.Lock()
		f.calls = append(f.calls, r)
		f.Unlock()
		if r.URL.Path == "/v1.41/system/df" {
			return jsonResponse(http.StatusOK, `{}`), nil
		}
		if f.called != nil {
			f.called <- struct{}{}
		}
		if f.gate != nil {
			<-f.gate
		}
		if f.fail[r.URL.Path] {
			return jsonResponse(http.StatusInternalServerError, `{"message":"boom"}`), nil
		}
		return jsonResponse(http.StatusOK, `{"SpaceReclaimed":`+jsonNumber(f.freed[r.URL.Path])+`}`), nil
	})}}
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (f *fakeDocker) pruneCalls() []*http.Request {
	f.Lock()
	defer f.Unlock()
	var out []*http.Request
	for _, c := range f.calls {
		if strings.HasSuffix(c.URL.Path, "/prune") {
			out = append(out, c)
		}
	}
	return out
}

func TestDockerPruneSelectedSteps(t *testing.T) {
	fake := &fakeDocker{freed: map[string]int64{
		"/v1.41/containers/prune": 100,
		"/v1.41/images/prune":     200,
		"/v1.41/build/prune":      300,
	}}
	result := fake.manager().prune(t.Context(), common.DiskPruneRequest{Containers: true, Images: true, BuildCache: true})

	assert.Equal(t, uint64(600), result.Freed)
	assert.Empty(t, result.Errors)

	calls := fake.pruneCalls()
	require.Len(t, calls, 3)
	var paths []string
	for _, c := range calls {
		assert.Equal(t, http.MethodPost, c.Method)
		paths = append(paths, c.URL.Path)
	}
	assert.Equal(t, []string{"/v1.41/containers/prune", "/v1.41/images/prune", "/v1.41/build/prune"}, paths,
		"containers go first so their images can be removed, and volumes are never touched")

	for _, c := range calls {
		filters := map[string][]string{}
		require.NoError(t, json.Unmarshal([]byte(c.URL.Query().Get("filters")), &filters))
		assert.Equal(t, []string{"168h"}, filters["until"], "%s keeps anything newer than a week", c.URL.Path)
	}
	var imageFilters map[string][]string
	require.NoError(t, json.Unmarshal([]byte(calls[1].URL.Query().Get("filters")), &imageFilters))
	assert.Equal(t, []string{"false"}, imageFilters["dangling"], "removes every unused image, not only untagged ones")
	assert.Equal(t, "true", calls[2].URL.Query().Get("all"), "removes all unused build cache, not only dangling")
}

func TestDockerPruneOnlyImages(t *testing.T) {
	fake := &fakeDocker{freed: map[string]int64{"/v1.41/images/prune": 42}}
	result := fake.manager().prune(t.Context(), common.DiskPruneRequest{Images: true})
	assert.Equal(t, uint64(42), result.Freed)
	require.Len(t, fake.pruneCalls(), 1)
	assert.Equal(t, "/v1.41/images/prune", fake.pruneCalls()[0].URL.Path)
}

func TestDockerPruneStepFailureDoesNotStopTheRest(t *testing.T) {
	fake := &fakeDocker{
		freed: map[string]int64{"/v1.41/containers/prune": 100, "/v1.41/build/prune": 5},
		fail:  map[string]bool{"/v1.41/images/prune": true},
	}
	result := fake.manager().prune(t.Context(), common.DiskPruneRequest{Containers: true, Images: true, BuildCache: true})

	assert.Equal(t, uint64(105), result.Freed, "steps that worked still count")
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0], "images")
	assert.Contains(t, result.Errors[0], "boom")
}

// TestPruneLive runs a real prune to check that the engine accepts the filters.
// It DELETES unused images and old stopped containers on the engine behind
// /var/run/docker.sock, so it only runs when DISK_PRUNE_LIVE_TEST=1 is set. Point
// it at a throwaway engine, such as docker:dind, never at a machine you use.
func TestPruneLive(t *testing.T) {
	if os.Getenv("DISK_PRUNE_LIVE_TEST") != "1" {
		t.Skip("set DISK_PRUNE_LIVE_TEST=1 and use a throwaway Docker engine")
	}
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	dm := newDockerManager(&Agent{})
	require.NotNil(t, dm)

	// A short age (for example 1s) lets freshly created test data be removed.
	minAge := os.Getenv("DISK_PRUNE_LIVE_MIN_AGE")
	if minAge != "" {
		previous := diskPruneMinAge
		diskPruneMinAge = minAge
		defer func() { diskPruneMinAge = previous }()
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	result := dm.prune(ctx, common.DiskPruneRequest{Containers: true, Images: true, BuildCache: true})
	t.Logf("min age=%s freed=%d errors=%v", diskPruneMinAge, result.Freed, result.Errors)
	assert.Empty(t, result.Errors)
	if minAge != "" {
		assert.Positive(t, result.Freed, "unused images older than the min age are removed")
	}
}

func TestStartPruneRefusals(t *testing.T) {
	fake := &fakeDocker{}

	t.Setenv("DISK_PRUNE", "")
	m := newDiskBreakdownManager(fake.manager())
	require.NotNil(t, m)
	assert.False(t, m.pruneEnabled, "off unless DISK_PRUNE=true")
	_, err := m.startPrune(common.DiskPruneRequest{Images: true})
	assert.ErrorIs(t, err, errPruneDisabled)

	t.Setenv("DISK_PRUNE", "true")
	m = newDiskBreakdownManager(fake.manager())
	require.True(t, m.pruneEnabled)
	_, err = m.startPrune(common.DiskPruneRequest{})
	assert.ErrorIs(t, err, errPruneNothing)

	t.Setenv("DISK_BREAKDOWN_PATHS", t.TempDir())
	m = newDiskBreakdownManager(nil)
	require.NotNil(t, m)
	assert.False(t, m.pruneEnabled, "no Docker engine means nothing to prune")

	assert.Empty(t, fake.pruneCalls(), "a refused request never reaches Docker")
}

func TestStartPruneRunsInBackgroundThenScans(t *testing.T) {
	t.Setenv("DISK_PRUNE", "true")
	fake := &fakeDocker{
		freed:  map[string]int64{"/v1.41/images/prune": 1234},
		gate:   make(chan struct{}),
		called: make(chan struct{}, 4),
	}
	m := newDiskBreakdownManager(fake.manager())
	require.NotNil(t, m)

	state, err := m.startPrune(common.DiskPruneRequest{Images: true})
	require.NoError(t, err)
	assert.True(t, state.Pruning, "returns at once while the prune is still running")
	assert.Nil(t, state.Prune)
	<-fake.called // the prune is now blocked inside Docker

	_, err = m.startPrune(common.DiskPruneRequest{Images: true})
	assert.ErrorIs(t, err, errPruneBusy, "one prune at a time")

	during := m.request(true)
	assert.True(t, during.Pruning)
	assert.False(t, during.Refreshing, "no scan runs alongside a prune, even when forced")

	close(fake.gate)
	require.Eventually(t, func() bool {
		s := m.request(false)
		return !s.Pruning && !s.Refreshing && s.CheckedAt != 0
	}, 5*time.Second, 10*time.Millisecond, "a scan follows the prune")

	done := m.request(false)
	require.NotNil(t, done.Prune)
	assert.Equal(t, uint64(1234), done.Prune.Freed)
	assert.NotZero(t, done.Prune.FinishedAt)
	assert.Empty(t, done.Prune.Errors)

	// The result stays available and a new prune is allowed again.
	_, err = m.startPrune(common.DiskPruneRequest{Images: true})
	assert.NoError(t, err)
	require.Eventually(t, func() bool { s := m.request(false); return !s.Pruning && !s.Refreshing }, 5*time.Second, 10*time.Millisecond)
}
