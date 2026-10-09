package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/henrygd/beszel/agent/utils"
	"github.com/henrygd/beszel/internal/entities/system"
)

const (
	// diskBreakdownTTL is how long a finished scan is reused before a request starts a new one.
	diskBreakdownTTL = 15 * time.Minute
	// diskBreakdownTimeout bounds one whole scan.
	diskBreakdownTimeout = 10 * time.Minute
	// diskBreakdownDockerAPI pins the /system/df response shape. Newer engines
	// return a different layout on the unversioned path.
	diskBreakdownDockerAPI = "http://localhost/v1.41/system/df"
	diskBreakdownTopItems  = 8
	diskBreakdownTopDirs   = 15
)

// defaultDiskBreakdownExcludes are skipped while walking configured paths.
// /var/lib/docker is reported by the Docker part and walking overlay2 is slow.
var defaultDiskBreakdownExcludes = []string{"/proc", "/sys", "/dev", "/var/lib/docker"}

// diskBreakdownManager reports what is using disk space. It scans in the
// background only when the hub asks, so it adds no load to normal collection.
type diskBreakdownManager struct {
	sync.Mutex
	docker   *dockerManager
	paths    []string
	excludes []string
	result   system.DiskBreakdown
	running  bool
}

// newDiskBreakdownManager returns nil if there is neither a Docker engine nor a
// configured path to report on. DISK_BREAKDOWN_PATHS is a comma-separated list of
// directories (mounted read-only into the agent container when containerised).
func newDiskBreakdownManager(docker *dockerManager) *diskBreakdownManager {
	m := &diskBreakdownManager{docker: docker, excludes: slices.Clone(defaultDiskBreakdownExcludes)}
	m.paths = splitEnvList("DISK_BREAKDOWN_PATHS")
	m.excludes = append(m.excludes, splitEnvList("DISK_BREAKDOWN_EXCLUDE")...)
	if docker == nil && len(m.paths) == 0 {
		return nil
	}
	slog.Debug("Disk breakdown", "docker", docker != nil, "paths", m.paths)
	return m
}

func splitEnvList(key string) []string {
	env, _ := utils.GetEnv(key)
	var list []string
	for part := range strings.SplitSeq(env, ",") {
		if part = strings.TrimSpace(part); part != "" {
			list = append(list, filepath.Clean(part))
		}
	}
	return list
}

// request returns the cached breakdown and starts a background scan if there is
// no result yet, the result is stale or force is set.
func (m *diskBreakdownManager) request(force bool) system.DiskBreakdown {
	m.Lock()
	defer m.Unlock()
	stale := m.result.CheckedAt == 0 || time.Since(time.Unix(m.result.CheckedAt, 0)) >= diskBreakdownTTL
	if !m.running && (force || stale) {
		m.running = true
		go m.refresh()
	}
	out := m.result
	out.Refreshing = m.running
	return out
}

func (m *diskBreakdownManager) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), diskBreakdownTimeout)
	defer cancel()

	var result system.DiskBreakdown
	if m.docker != nil {
		usage, err := m.docker.getDiskUsage(ctx)
		if err != nil {
			slog.Debug("Disk breakdown: Docker", "err", err)
			result.DockerErr = err.Error()
		}
		result.Docker = usage
	}
	for _, path := range m.paths {
		result.Paths = append(result.Paths, scanPath(ctx, path, m.excludes))
	}
	result.CheckedAt = time.Now().Unix()

	m.Lock()
	m.result = result
	m.running = false
	m.Unlock()
}

// dockerDiskUsageResponse is the legacy (API <= 1.51) /system/df layout.
type dockerDiskUsageResponse struct {
	LayersSize int64 `json:"LayersSize"`
	Images     []struct {
		ID         string   `json:"Id"`
		RepoTags   []string `json:"RepoTags"`
		Size       int64    `json:"Size"`
		SharedSize int64    `json:"SharedSize"`
		Containers int64    `json:"Containers"`
	} `json:"Images"`
	Containers []struct {
		ID     string   `json:"Id"`
		Names  []string `json:"Names"`
		Image  string   `json:"Image"`
		SizeRw int64    `json:"SizeRw"`
		State  string   `json:"State"`
	} `json:"Containers"`
	Volumes []struct {
		Name      string `json:"Name"`
		UsageData *struct {
			RefCount int64 `json:"RefCount"`
			Size     int64 `json:"Size"`
		} `json:"UsageData"`
	} `json:"Volumes"`
	BuildCache []struct {
		InUse bool  `json:"InUse"`
		Size  int64 `json:"Size"`
	} `json:"BuildCache"`
}

// getDiskUsage asks the engine how much space images, containers, volumes and
// build cache use. The engine can take a while to size volumes, so this uses a
// client without the short per-request timeout and relies on ctx instead.
func (dm *dockerManager) getDiskUsage(ctx context.Context) (*system.DockerDiskUsage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, diskBreakdownDockerAPI, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: dm.client.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("docker disk usage request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var raw dockerDiskUsageResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return summarizeDockerDiskUsage(&raw), nil
}

func summarizeDockerDiskUsage(raw *dockerDiskUsageResponse) *system.DockerDiskUsage {
	out := &system.DockerDiskUsage{}
	var images, containers, volumes []system.DiskItem

	for _, img := range raw.Images {
		size := nonNegative(img.Size)
		out.Images.Count++
		inUse := img.Containers > 0
		if inUse {
			out.Images.Active++
		} else {
			out.Images.Reclaimable += size - min(size, nonNegative(img.SharedSize))
		}
		name := "<none>"
		if len(img.RepoTags) > 0 {
			name = img.RepoTags[0]
		} else if id := strings.TrimPrefix(img.ID, "sha256:"); len(id) >= 12 {
			name = "<none> " + id[:12]
		}
		images = append(images, system.DiskItem{Kind: "image", Name: name, Size: size, InUse: inUse})
	}
	// LayersSize counts shared layers once, unlike the sum of per-image sizes.
	out.Images.Total = nonNegative(raw.LayersSize)
	if out.Images.Total == 0 {
		for _, img := range images {
			out.Images.Total += img.Size
		}
	}

	for _, ctr := range raw.Containers {
		size := nonNegative(ctr.SizeRw)
		out.Containers.Count++
		out.Containers.Total += size
		running := ctr.State == "running"
		if running {
			out.Containers.Active++
		} else {
			out.Containers.Reclaimable += size
		}
		name := ctr.Image
		if len(ctr.Names) > 0 {
			name = strings.TrimPrefix(ctr.Names[0], "/")
		}
		containers = append(containers, system.DiskItem{Kind: "container", Name: name, Size: size, InUse: running})
	}

	for _, vol := range raw.Volumes {
		var size uint64
		inUse := true
		if vol.UsageData != nil {
			size = nonNegative(vol.UsageData.Size)
			inUse = vol.UsageData.RefCount > 0
		}
		out.Volumes.Count++
		out.Volumes.Total += size
		if inUse {
			out.Volumes.Active++
		} else {
			out.Volumes.Reclaimable += size
		}
		volumes = append(volumes, system.DiskItem{Kind: "volume", Name: vol.Name, Size: size, InUse: inUse})
	}

	for _, entry := range raw.BuildCache {
		size := nonNegative(entry.Size)
		out.BuildCache.Count++
		out.BuildCache.Total += size
		if entry.InUse {
			out.BuildCache.Active++
		} else {
			out.BuildCache.Reclaimable += size
		}
	}

	for _, items := range [][]system.DiskItem{images, containers, volumes} {
		out.Items = append(out.Items, topItems(items, diskBreakdownTopItems)...)
	}
	return out
}

func nonNegative(n int64) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}

// topItems returns up to n items with a non-zero size, largest first.
func topItems(items []system.DiskItem, n int) []system.DiskItem {
	items = slices.DeleteFunc(slices.Clone(items), func(i system.DiskItem) bool { return i.Size == 0 })
	slices.SortFunc(items, func(a, b system.DiskItem) int {
		switch {
		case a.Size > b.Size:
			return -1
		case a.Size < b.Size:
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(items) > n {
		items = items[:n]
	}
	return items
}

// scanPath sizes every direct child of root. Symlinks are not followed and
// unreadable entries are skipped, which sets Partial.
func scanPath(ctx context.Context, root string, excludes []string) system.PathUsage {
	usage := system.PathUsage{Path: root}
	entries, err := os.ReadDir(root)
	if err != nil {
		usage.Error = err.Error()
		return usage
	}
	var children []system.DiskItem
	for _, entry := range entries {
		if ctx.Err() != nil {
			usage.Partial = true
			break
		}
		child := filepath.Join(root, entry.Name())
		size, partial := diskUsageOf(ctx, child, entry, excludes)
		usage.Total += size
		usage.Partial = usage.Partial || partial
		kind := "file"
		if entry.IsDir() {
			kind = "dir"
		}
		children = append(children, system.DiskItem{Kind: kind, Name: entry.Name(), Size: size})
	}
	usage.Children = topItems(children, diskBreakdownTopDirs)
	return usage
}

// diskUsageOf returns the disk space used by path and everything beneath it.
func diskUsageOf(ctx context.Context, path string, entry fs.DirEntry, excludes []string) (size uint64, partial bool) {
	if !entry.IsDir() {
		info, err := entry.Info()
		if err != nil {
			return 0, true
		}
		return fileDiskUsage(info), false
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			partial = true
			return ctx.Err()
		}
		if err != nil {
			partial = true
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() && slices.Contains(excludes, p) {
			return fs.SkipDir
		}
		info, err := d.Info()
		if err != nil {
			partial = true
			return nil
		}
		size += fileDiskUsage(info)
		return nil
	})
	return size, partial
}
