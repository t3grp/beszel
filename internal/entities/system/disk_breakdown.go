package system

// DiskItem is one entry in a disk usage breakdown.
type DiskItem struct {
	// Kind is "image", "container", "volume", "dir" or "file".
	Kind string `json:"kind" cbor:"0,keyasint"`
	Name string `json:"name" cbor:"1,keyasint"`
	Size uint64 `json:"size" cbor:"2,keyasint"`
	// InUse is false for images without containers, volumes without references and stopped containers.
	InUse bool `json:"inUse,omitempty" cbor:"3,keyasint,omitempty"`
}

// DiskCategory is the total for one class of Docker data.
type DiskCategory struct {
	Total       uint64 `json:"total" cbor:"0,keyasint"`
	Reclaimable uint64 `json:"reclaimable,omitempty" cbor:"1,keyasint,omitempty"`
	Count       uint32 `json:"count" cbor:"2,keyasint"`
	Active      uint32 `json:"active,omitempty" cbor:"3,keyasint,omitempty"`
}

// DockerDiskUsage is the Docker engine's own accounting of the space it uses.
type DockerDiskUsage struct {
	Images     DiskCategory `json:"images" cbor:"0,keyasint"`
	Containers DiskCategory `json:"containers" cbor:"1,keyasint"`
	Volumes    DiskCategory `json:"volumes" cbor:"2,keyasint"`
	BuildCache DiskCategory `json:"buildCache" cbor:"3,keyasint"`
	// Items holds the largest images, containers and volumes.
	Items []DiskItem `json:"items,omitempty" cbor:"4,keyasint,omitempty"`
}

// PathUsage is the size of each child directly under a configured path.
type PathUsage struct {
	Path     string     `json:"path" cbor:"0,keyasint"`
	Total    uint64     `json:"total" cbor:"1,keyasint"`
	Children []DiskItem `json:"children,omitempty" cbor:"2,keyasint,omitempty"`
	// Partial is true if some entries could not be read.
	Partial bool   `json:"partial,omitempty" cbor:"3,keyasint,omitempty"`
	Error   string `json:"error,omitempty" cbor:"4,keyasint,omitempty"`
}

// DiskPruneResult is the outcome of the last prune.
type DiskPruneResult struct {
	// FinishedAt is the Unix time in seconds the prune finished.
	FinishedAt int64 `json:"finishedAt" cbor:"0,keyasint"`
	// Freed is the space the engine reports it reclaimed, in bytes.
	Freed uint64 `json:"freed" cbor:"1,keyasint"`
	// Errors has one entry for each step that failed. Other steps still ran.
	Errors []string `json:"errors,omitempty" cbor:"2,keyasint,omitempty"`
}

// DiskBreakdown is the payload returned by the agent for the GetDiskBreakdown action.
type DiskBreakdown struct {
	// CheckedAt is the Unix time in seconds the last scan finished, 0 if none has finished.
	CheckedAt int64 `json:"checkedAt,omitempty" cbor:"0,keyasint,omitempty"`
	// Refreshing is true while a scan is running.
	Refreshing bool             `json:"refreshing,omitempty" cbor:"1,keyasint,omitempty"`
	Docker     *DockerDiskUsage `json:"docker,omitempty" cbor:"2,keyasint,omitempty"`
	DockerErr  string           `json:"dockerErr,omitempty" cbor:"3,keyasint,omitempty"`
	Paths      []PathUsage      `json:"paths,omitempty" cbor:"4,keyasint,omitempty"`
	// Pruning is true while a prune is running. A scan follows it.
	Pruning bool `json:"pruning,omitempty" cbor:"5,keyasint,omitempty"`
	// Prune is the result of the last prune since the agent started.
	Prune *DiskPruneResult `json:"prune,omitempty" cbor:"6,keyasint,omitempty"`
}
