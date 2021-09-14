/*
2019 © Postgres.ai
*/

package models

import (
	"time"

	"gitlab.com/postgres-ai/database-lab/v2/pkg/services/provision/resources"
)

// InstanceStatus represents status of a Database Lab Engine instance.
type InstanceStatus struct {
	Status              *Status          `json:"status"`
	FileSystem          *FileSystem      `json:"fileSystem"`
	ExpectedCloningTime float64          `json:"expectedCloningTime"`
	NumClones           uint64           `json:"numClones"`
	Clones              []*Clone         `json:"clones"`
	Pools               []PoolEntry      `json:"pools"`
	Retrieving          Retrieving       `json:"retrieving"`
	Provisioner         ContainerOptions `json:"provisioner"`
	StartedAt           time.Time        `json:"startedAt"`
}

// PoolEntry represents a pool entry.
type PoolEntry struct {
	Name        string               `json:"name"`
	Mode        string               `json:"mode"`
	DataStateAt string               `json:"dataStateAt"`
	Status      resources.PoolStatus `json:"status"`
	CloneList   []string             `json:"cloneList"`
	FileSystem  FileSystem           `json:"fileSystem"`
}

// ContainerOptions describes options for running containers.
type ContainerOptions struct {
	DockerImage     string            `json:"dockerImage"`
	ContainerConfig map[string]string `json:"containerConfig"`
}

// Health represents a response for heath-check requests.
type Health struct {
	Version string `json:"engine_version"`
}

// CloneList represents a list of clones.
type CloneList struct {
	Clones []*Clone `json:"clones"`
}

// CloneListView represents a list of clone views.
type CloneListView struct {
	Clones []*CloneView `json:"clones"`
}

// InstanceStatusView represents view of a Database Lab Engine instance status.
type InstanceStatusView struct {
	*InstanceStatus
	FileSystem *FileSystemView `json:"fileSystem"`
	Pools      []PoolEntryView `json:"pools"`
	Clones     []*CloneView    `json:"clones"`
}

// PoolEntryView represents a pool entry view.
type PoolEntryView struct {
	*PoolEntry
	FileSystem FileSystemView `json:"fileSystem"`
}
