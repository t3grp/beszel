package common

import (
	"github.com/fxamacker/cbor/v2"
	"github.com/henrygd/beszel/internal/entities/smart"
	"github.com/henrygd/beszel/internal/entities/system"
	"github.com/henrygd/beszel/internal/entities/systemd"
)

type WebSocketAction = uint8

const (
	// Request system data from agent
	GetData WebSocketAction = iota
	// Check the fingerprint of the agent
	CheckFingerprint
	// Request container logs from agent
	GetContainerLogs
	// Request container info from agent
	GetContainerInfo
	// Request SMART data from agent
	GetSmartData
	// Request detailed systemd service info from agent
	GetSystemdInfo
	// Request ZFS detail data from agent
	GetZfsData
	// Sync network monitor configuration to agent
	SyncNetworkMonitors
	// Request the list of pending package updates from agent
	GetPackageUpdates
	// Request recent logs for a systemd service from the agent.
	GetSystemdLogs
	// Add new actions here...
)

// Fork-only actions. These sit far above the iota block on purpose: upstream
// appends a new action almost every release, and a stock agent must never read
// one of these numbers as one of its own.
const (
	// Request the disk usage breakdown (Docker and configured paths) from the agent.
	GetDiskBreakdown WebSocketAction = 200
	// Start a background prune of unused Docker data. The agent refuses it unless DISK_PRUNE=true.
	PruneDiskSpace WebSocketAction = 201
)

// HubRequest defines the structure for requests sent from hub to agent.
type HubRequest[T any] struct {
	Action WebSocketAction `cbor:"0,keyasint"`
	Data   T               `cbor:"1,keyasint,omitempty,omitzero"`
	Id     *uint32         `cbor:"2,keyasint,omitempty"`
}

// AgentResponse defines the structure for responses sent from agent to hub.
type AgentResponse struct {
	Id          *uint32                    `cbor:"0,keyasint,omitempty"`
	SystemData  *system.CombinedData       `cbor:"1,keyasint,omitempty,omitzero"` // Legacy (<= 0.17)
	Fingerprint *FingerprintResponse       `cbor:"2,keyasint,omitempty,omitzero"` // Legacy (<= 0.17)
	Error       string                     `cbor:"3,keyasint,omitempty,omitzero"`
	String      *string                    `cbor:"4,keyasint,omitempty,omitzero"` // Legacy (<= 0.17)
	SmartData   map[string]smart.SmartData `cbor:"5,keyasint,omitempty,omitzero"` // Legacy (<= 0.17)
	ServiceInfo systemd.ServiceDetails     `cbor:"6,keyasint,omitempty,omitzero"` // Legacy (<= 0.17)
	// Data is the generic response payload for new endpoints (0.18+)
	Data          cbor.RawMessage `cbor:"7,keyasint,omitempty,omitzero"`
	SmartComplete bool            `cbor:"8,keyasint,omitempty,omitzero"`
}

type FingerprintRequest struct {
	Signature   []byte `cbor:"0,keyasint"`
	NeedSysInfo bool   `cbor:"1,keyasint"` // For universal token system creation
}

type FingerprintResponse struct {
	Fingerprint string `cbor:"0,keyasint"`
	// Optional system info for universal token system creation
	Hostname string `cbor:"1,keyasint,omitzero"`
	Port     string `cbor:"2,keyasint,omitzero"`
	Name     string `cbor:"3,keyasint,omitzero"`
}

type DataRequestOptions struct {
	CacheTimeMs    uint16 `cbor:"0,keyasint"`
	IncludeDetails bool   `cbor:"1,keyasint"`
}

type ZfsDataRequest struct {
	Force bool `cbor:"0,keyasint,omitempty"`
}

type ContainerLogsRequest struct {
	ContainerID string `cbor:"0,keyasint"`
}

type ContainerInfoRequest struct {
	ContainerID string `cbor:"0,keyasint"`
}

type SystemdInfoRequest struct {
	ServiceName string `cbor:"0,keyasint"`
}

type SystemdLogsRequest struct {
	ServiceName string `cbor:"0,keyasint"`
}

type DiskBreakdownRequest struct {
	// Force starts a new scan even if the cached result is still fresh.
	Force bool `cbor:"0,keyasint,omitempty"`
}

// DiskPruneRequest selects what to remove. Volumes are never pruned.
type DiskPruneRequest struct {
	// Containers removes containers that have been stopped for at least a week.
	Containers bool `cbor:"0,keyasint,omitempty"`
	// Images removes images no container uses that are at least a week old.
	Images bool `cbor:"1,keyasint,omitempty"`
	// BuildCache removes unused build cache entries that are at least a week old.
	BuildCache bool `cbor:"2,keyasint,omitempty"`
}
