package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/henrygd/beszel/internal/common"
	"github.com/henrygd/beszel/internal/entities/system"
)

const (
	// diskPruneTimeout bounds one whole prune. Removing many images is slow.
	diskPruneTimeout = 10 * time.Minute
	// diskPruneDockerAPI is pinned like diskBreakdownDockerAPI.
	diskPruneDockerAPI = "http://localhost/v1.41/"
)

// diskPruneMinAge keeps anything newer than this, so a rollback image or a
// container that was just stopped survives. Only the live test changes it.
var diskPruneMinAge = "168h"

var (
	errPruneDisabled = errors.New("disk pruning is not enabled on this agent")
	errPruneNothing  = errors.New("nothing selected to prune")
	errPruneBusy     = errors.New("a disk scan or prune is already running")
)

// startPrune begins a background prune and returns at once. A scan runs when it
// finishes, so the next breakdown shows the new sizes. Only one scan or prune
// runs at a time.
func (m *diskBreakdownManager) startPrune(req common.DiskPruneRequest) (system.DiskBreakdown, error) {
	if !m.pruneEnabled {
		return system.DiskBreakdown{}, errPruneDisabled
	}
	if !req.Containers && !req.Images && !req.BuildCache {
		return system.DiskBreakdown{}, errPruneNothing
	}
	m.Lock()
	if m.running || m.pruning {
		m.Unlock()
		return system.DiskBreakdown{}, errPruneBusy
	}
	m.pruning = true
	m.Unlock()

	go m.runPrune(req)
	return m.request(false), nil
}

func (m *diskBreakdownManager) runPrune(req common.DiskPruneRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), diskPruneTimeout)
	result := m.docker.prune(ctx, req)
	cancel()
	result.FinishedAt = time.Now().Unix()

	m.Lock()
	m.prune = &result
	m.pruning = false
	m.running = true // claimed here so no other request slips in before the scan
	m.Unlock()
	m.refresh()
}

type dockerPruneResponse struct {
	SpaceReclaimed int64 `json:"SpaceReclaimed"`
}

// prune removes the selected kinds of unused data that are older than
// diskPruneMinAge. It never touches volumes. Steps run in dependency order
// (containers free images, which are then removed) and a failed step does not
// stop the others.
func (dm *dockerManager) prune(ctx context.Context, req common.DiskPruneRequest) system.DiskPruneResult {
	filters := func(f map[string][]string) string {
		b, _ := json.Marshal(f)
		return string(b)
	}
	age := []string{diskPruneMinAge}
	steps := []struct {
		enabled bool
		name    string
		path    string
		query   url.Values
	}{
		{req.Containers, "containers", "containers/prune", url.Values{
			"filters": {filters(map[string][]string{"until": age})},
		}},
		// dangling=false matches every unused image, which is what the card counts as reclaimable.
		{req.Images, "images", "images/prune", url.Values{
			"filters": {filters(map[string][]string{"dangling": {"false"}, "until": age})},
		}},
		{req.BuildCache, "build cache", "build/prune", url.Values{
			"all":     {"true"},
			"filters": {filters(map[string][]string{"until": age})},
		}},
	}

	var result system.DiskPruneResult
	for _, step := range steps {
		if !step.enabled {
			continue
		}
		freed, err := dm.postPrune(ctx, step.path, step.query)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", step.name, err))
			continue
		}
		result.Freed += freed
	}
	return result
}

// postPrune calls one Docker prune endpoint and returns the bytes it reports as
// reclaimed. Like getDiskUsage it uses the shared transport without the short
// per-request timeout and relies on ctx instead.
func (dm *dockerManager) postPrune(ctx context.Context, path string, query url.Values) (uint64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, diskPruneDockerAPI+path+"?"+query.Encode(), nil)
	if err != nil {
		return 0, err
	}
	client := &http.Client{Transport: dm.client.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return 0, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out dockerPruneResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return nonNegative(out.SpaceReclaimed), nil
}
