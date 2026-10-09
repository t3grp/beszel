package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const dockerDiskUsageSample = `{
  "LayersSize": 900,
  "Images": [
    {"Id": "sha256:aaaaaaaaaaaaaaaaaaaa", "RepoTags": ["app:1"], "Size": 500, "SharedSize": 100, "Containers": 1},
    {"Id": "sha256:bbbbbbbbbbbbbbbbbbbb", "RepoTags": [], "Size": 400, "SharedSize": 100, "Containers": 0},
    {"Id": "sha256:cccccccccccccccccccc", "RepoTags": ["old:1"], "Size": 300, "SharedSize": -1, "Containers": 0}
  ],
  "Containers": [
    {"Id": "1", "Names": ["/web"], "Image": "app:1", "SizeRw": 50, "State": "running"},
    {"Id": "2", "Names": [], "Image": "old:1", "SizeRw": 20, "State": "exited"}
  ],
  "Volumes": [
    {"Name": "data", "UsageData": {"RefCount": 1, "Size": 700}},
    {"Name": "orphan", "UsageData": {"RefCount": 0, "Size": 300}},
    {"Name": "unsized", "UsageData": {"RefCount": 1, "Size": -1}}
  ],
  "BuildCache": [
    {"InUse": true, "Size": 10},
    {"InUse": false, "Size": 40}
  ]
}`

func TestSummarizeDockerDiskUsage(t *testing.T) {
	var raw dockerDiskUsageResponse
	require.NoError(t, json.Unmarshal([]byte(dockerDiskUsageSample), &raw))
	usage := summarizeDockerDiskUsage(&raw)

	assert.Equal(t, uint64(900), usage.Images.Total, "uses LayersSize, which counts shared layers once")
	assert.Equal(t, uint32(3), usage.Images.Count)
	assert.Equal(t, uint32(1), usage.Images.Active)
	// unused images: (400-100) + (300-0, unknown shared size treated as 0)
	assert.Equal(t, uint64(600), usage.Images.Reclaimable)

	assert.Equal(t, system.DiskCategory{Total: 70, Reclaimable: 20, Count: 2, Active: 1}, usage.Containers)
	assert.Equal(t, system.DiskCategory{Total: 1000, Reclaimable: 300, Count: 3, Active: 2}, usage.Volumes)
	assert.Equal(t, system.DiskCategory{Total: 50, Reclaimable: 40, Count: 2, Active: 1}, usage.BuildCache)

	names := map[string]string{}
	for _, item := range usage.Items {
		names[item.Kind+":"+item.Name] = ""
	}
	assert.Contains(t, names, "image:app:1")
	assert.Contains(t, names, "image:<none> bbbbbbbbbbbb", "untagged images are shown by short id")
	assert.Contains(t, names, "container:web")
	assert.Contains(t, names, "container:old:1", "unnamed containers fall back to the image")
	assert.Contains(t, names, "volume:data")
	assert.NotContains(t, names, "volume:unsized", "zero-sized entries are dropped from the top list")
}

func TestSummarizeDockerDiskUsageEmpty(t *testing.T) {
	usage := summarizeDockerDiskUsage(&dockerDiskUsageResponse{})
	assert.Equal(t, uint32(0), usage.Images.Count)
	assert.Empty(t, usage.Items)
}

func TestTopItems(t *testing.T) {
	items := []system.DiskItem{
		{Name: "b", Size: 5},
		{Name: "a", Size: 5},
		{Name: "zero", Size: 0},
		{Name: "big", Size: 50},
		{Name: "c", Size: 1},
	}
	got := topItems(items, 3)
	require.Len(t, got, 3)
	assert.Equal(t, []string{"big", "a", "b"}, []string{got[0].Name, got[1].Name, got[2].Name})
	assert.Len(t, items, 5, "input is not modified")
}

func writeSizedFile(t *testing.T, path string, size int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, make([]byte, size), 0o644))
}

func TestScanPath(t *testing.T) {
	root := t.TempDir()
	writeSizedFile(t, filepath.Join(root, "big", "a.bin"), 1<<20)
	writeSizedFile(t, filepath.Join(root, "big", "nested", "b.bin"), 1<<20)
	writeSizedFile(t, filepath.Join(root, "small", "c.bin"), 4096)
	writeSizedFile(t, filepath.Join(root, "skipme", "d.bin"), 8<<20)
	writeSizedFile(t, filepath.Join(root, "top.bin"), 4096)

	usage := scanPath(context.Background(), root, []string{filepath.Join(root, "skipme")})
	assert.Equal(t, root, usage.Path)
	assert.Empty(t, usage.Error)
	assert.False(t, usage.Partial)

	require.NotEmpty(t, usage.Children)
	assert.Equal(t, "big", usage.Children[0].Name, "largest child first")
	assert.Equal(t, "dir", usage.Children[0].Kind)
	assert.GreaterOrEqual(t, usage.Children[0].Size, uint64(2<<20))

	var skipped system.DiskItem
	for _, c := range usage.Children {
		if c.Name == "skipme" {
			skipped = c
		}
	}
	assert.Less(t, skipped.Size, uint64(1<<20), "excluded directory contents are not counted")

	var sum uint64
	for _, c := range usage.Children {
		sum += c.Size
	}
	assert.Equal(t, sum, usage.Total)
}

func TestScanPathMissing(t *testing.T) {
	usage := scanPath(context.Background(), filepath.Join(t.TempDir(), "nope"), nil)
	assert.NotEmpty(t, usage.Error)
	assert.Empty(t, usage.Children)
}

func TestScanPathCancelled(t *testing.T) {
	root := t.TempDir()
	writeSizedFile(t, filepath.Join(root, "a", "a.bin"), 4096)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	usage := scanPath(ctx, root, nil)
	assert.True(t, usage.Partial, "a cancelled scan is flagged partial")
}

func TestDiskBreakdownManagerRequest(t *testing.T) {
	root := t.TempDir()
	writeSizedFile(t, filepath.Join(root, "x", "x.bin"), 1<<20)
	t.Setenv("DISK_BREAKDOWN_PATHS", root)

	m := newDiskBreakdownManager(nil)
	require.NotNil(t, m, "configured paths enable the manager without Docker")

	first := m.request(false)
	assert.Zero(t, first.CheckedAt, "first call returns immediately with no data")
	assert.True(t, first.Refreshing)

	require.Eventually(t, func() bool { return m.request(false).CheckedAt != 0 }, 5*time.Second, 10*time.Millisecond)

	done := m.request(false)
	assert.False(t, done.Refreshing, "fresh result does not start another scan")
	require.Len(t, done.Paths, 1)
	assert.Equal(t, "x", done.Paths[0].Children[0].Name)
	assert.Nil(t, done.Docker)

	forced := m.request(true)
	assert.True(t, forced.Refreshing, "force starts a new scan")
	assert.NotZero(t, forced.CheckedAt, "the previous result stays available while refreshing")
}

func TestNewDiskBreakdownManagerDisabled(t *testing.T) {
	t.Setenv("DISK_BREAKDOWN_PATHS", "")
	assert.Nil(t, newDiskBreakdownManager(nil), "no Docker and no paths means nothing to report")
}

// TestGetDiskUsageLive checks the real engine response shape. It is skipped
// unless a Docker socket is reachable.
func TestGetDiskUsageLive(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("no Docker socket")
	}
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	dm := newDockerManager(&Agent{})
	require.NotNil(t, dm)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	usage, err := dm.getDiskUsage(ctx)
	require.NoError(t, err)
	require.NotNil(t, usage)
	t.Logf("images=%+v containers=%+v volumes=%+v cache=%+v items=%d",
		usage.Images, usage.Containers, usage.Volumes, usage.BuildCache, len(usage.Items))
	assert.Positive(t, usage.Images.Count, "the test host has at least the golang image")
	assert.Positive(t, usage.Images.Total)
}
