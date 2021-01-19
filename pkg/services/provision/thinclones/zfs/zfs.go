/*
2019 © Postgres.ai
*/

// Package zfs provides an interface to work with ZFS.
package zfs

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"gitlab.com/postgres-ai/database-lab/pkg/log"
	"gitlab.com/postgres-ai/database-lab/pkg/services/provision/resources"
	"gitlab.com/postgres-ai/database-lab/pkg/services/provision/runners"
	"gitlab.com/postgres-ai/database-lab/pkg/util"
)

const (
	headerOffset        = 1
	dataStateAtLabel    = "dblab:datastateat"
	isRoughStateAtLabel = "dblab:isroughdsa"
)

// ListEntry defines entry of ZFS list command.
type ListEntry struct {
	Name string

	// Read-only property that identifies the amount of disk space consumed
	// by a dataset and all its descendents.
	Used uint64

	// Controls the mount point used for this file system. When the mountpoint
	// property is changed for a file system, the file system and
	// any descendents that inherit the mount point are unmounted.
	// If the new value is legacy, then they remain unmounted. Otherwise,
	// they are automatically remounted in a new location if the property
	// was previously legacy or none, or if they were mounted before
	// the property was changed. In addition, any shared file systems are
	// unshared and shared in the new location.
	MountPoint string

	// Read-only property that identifies the compression ratio achieved for
	// a dataset, expressed as a multiplier. Compression can be enabled by the
	// zfs set compression=on dataset command.
	// The value is calculated from the logical size of all files and
	// the amount of referenced physical data. It includes explicit savings
	// through the use of the compression property.
	CompressRatio float64

	// Read-only property that identifies the amount of disk space available
	// to a file system and all its children, assuming no other activity in
	// the pool. Because disk space is shared within a pool, available space
	// can be limited by various factors including physical pool size, quotas,
	// reservations, and other datasets within the pool.
	Available uint64

	// Read-only property that identifies the dataset type as filesystem
	// (file system or clone), volume, or snapshot.
	Type string

	// Read-only property for cloned file systems or volumes that identifies
	// the snapshot from which the clone was created. The origin cannot be
	// destroyed (even with the –r or –f option) as long as a clone exists.
	// Non-cloned file systems have an origin of none.
	Origin string

	// Read-only property that identifies the date and time that a dataset
	// was created.
	Creation time.Time

	// The amount of data that is accessible by this dataset, which may
	// or may not be shared with other datasets in the pool. When a snapshot
	// or clone is created, it initially references the same amount of space
	//as the  file system or snapshot it was created from, since its contents
	// are identical.
	Referenced uint64

	// The amount of space that is "logically" accessible by this dataset.
	// See the referenced property. The logical space ignores the effect
	// of the compression and copies properties, giving a quantity closer
	// to the amount of data that applications see. However, it does include
	// space consumed by metadata.
	LogicalReferenced uint64

	// The amount of space that is "logically" consumed by this dataset
	// and all its descendents. See the used property. The logical space
	// ignores the effect of the compression and copies properties, giving
	// a quantity closer to the amount of data that applications see. However,
	// it does include space consumed by metadata.
	LogicalUsed uint64

	// DB Lab custom fields.

	// Data state timestamp.
	DataStateAt time.Time
}

type setFunc func(s string) error

type setTuple struct {
	field   string
	setFunc setFunc
}

// Manager describes a filesystem manager for ZFS.
type Manager struct {
	runner runners.Runner
	config Config
}

// Config defines configuration for ZFS filesystem manager.
type Config struct {
	Pool              *resources.Pool
	PreSnapshotSuffix string
	OSUsername        string
}

// NewFSManager creates a new Manager instance for ZFS.
func NewFSManager(runner runners.Runner, config Config) *Manager {
	m := Manager{
		runner: runner,
		config: config,
	}

	return &m
}

// Pool gets a filesystem pool.
func (m *Manager) Pool() *resources.Pool {
	return m.config.Pool
}

// CreateClone creates a new ZFS clone.
func (m *Manager) CreateClone(cloneName, snapshotID string) error {
	exists, err := m.cloneExists(cloneName)
	if err != nil {
		return errors.Wrap(err, "clone does not exist")
	}

	if exists {
		log.Msg(fmt.Sprintf("clone %q is already exists. Skip creation", cloneName))
		return nil
	}

	clonesMountDir := m.config.Pool.ClonesDir()

	cmd := "zfs clone " +
		"-o mountpoint=" + clonesMountDir + "/" + cloneName + " " +
		snapshotID + " " +
		m.config.Pool.Name + "/" + cloneName + " && " +
		"chown -R " + m.config.OSUsername + " " + clonesMountDir + "/" + cloneName

	out, err := m.runner.Run(cmd)
	if err != nil {
		return errors.Wrapf(err, "zfs clone error. Out: %v", out)
	}

	return nil
}

// DestroyClone destroys a ZFS clone.
func (m *Manager) DestroyClone(cloneName string) error {
	exists, err := m.cloneExists(cloneName)
	if err != nil {
		return errors.Wrap(err, "clone does not exist")
	}

	if !exists {
		log.Msg(fmt.Sprintf("clone %q is not exists. Skip deletion", cloneName))
		return nil
	}

	// Delete the clone and all snapshots and clones depending on it.
	// TODO(anatoly): right now, we are using this function only for
	// deleting thin clones created by users. If we are going to use
	// this function to delete clones used during the preparation
	// of baseline snapshots, we need to omit `-R`, to avoid
	// unexpected deletion of users' clones.
	cmd := fmt.Sprintf("zfs destroy -R %s/%s", m.config.Pool.Name, cloneName)

	if _, err = m.runner.Run(cmd); err != nil {
		return errors.Wrap(err, "failed to run command")
	}

	return nil
}

// cloneExists checks whether a ZFS clone exists.
func (m *Manager) cloneExists(name string) (bool, error) {
	listZfsClonesCmd := "zfs list"

	out, err := m.runner.Run(listZfsClonesCmd, false)
	if err != nil {
		return false, errors.Wrap(err, "failed to list clones")
	}

	return strings.Contains(out, name), nil
}

// ListClonesNames lists ZFS clones.
func (m *Manager) ListClonesNames() ([]string, error) {
	listZfsClonesCmd := "zfs list -o name -H"

	cmdOutput, err := m.runner.Run(listZfsClonesCmd, false)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list clones")
	}

	cloneNames := []string{}
	poolPrefix := m.config.Pool.Name + "/"
	clonePoolPrefix := m.config.Pool.Name + "/" + util.ClonePrefix
	lines := strings.Split(strings.TrimSpace(cmdOutput), "\n")

	for _, line := range lines {
		if strings.HasPrefix(line, clonePoolPrefix) {
			cloneNames = append(cloneNames, strings.TrimPrefix(line, poolPrefix))
		}
	}

	return util.Unique(cloneNames), nil
}

// CreateSnapshot creates a new snapshot.
func (m *Manager) CreateSnapshot(poolSuffix, dataStateAt string) (string, error) {
	poolName := m.config.Pool.Name

	if poolSuffix != "" {
		poolName += "/" + poolSuffix
	}

	originalDSA := dataStateAt

	if dataStateAt == "" {
		dataStateAt = time.Now().Format(util.DataStateAtFormat)
	}

	snapshotName := getSnapshotName(poolName, dataStateAt)
	cmd := fmt.Sprintf("zfs snapshot -r %s", snapshotName)

	if _, err := m.runner.Run(cmd, true); err != nil {
		return "", errors.Wrap(err, "failed to create snapshot")
	}

	cmd = fmt.Sprintf("zfs set %s=%q %s", dataStateAtLabel, strings.TrimSuffix(dataStateAt, m.config.PreSnapshotSuffix), snapshotName)

	if _, err := m.runner.Run(cmd, true); err != nil {
		return "", errors.Wrap(err, "failed to set the dataStateAt option for snapshot")
	}

	if originalDSA == "" {
		cmd = fmt.Sprintf("zfs set %s=%q %s", isRoughStateAtLabel, "1", snapshotName)

		if _, err := m.runner.Run(cmd, true); err != nil {
			return "", errors.Wrap(err, "failed to set the rough flag of dataStateAt option for snapshot")
		}
	}

	return snapshotName, nil
}

// getSnapshotName builds a snapshot name.
func getSnapshotName(pool, dataStateAt string) string {
	return fmt.Sprintf("%s@snapshot_%s", pool, dataStateAt)
}

// RollbackSnapshot rollbacks ZFS snapshot.
func RollbackSnapshot(r runners.Runner, pool string, snapshot string) error {
	cmd := fmt.Sprintf("zfs rollback -f -r %s", snapshot)

	if _, err := r.Run(cmd, true); err != nil {
		return errors.Wrap(err, "failed to rollback a snapshot")
	}

	return nil
}

// DestroySnapshot destroys the snapshot.
func (m *Manager) DestroySnapshot(snapshotName string) error {
	cmd := fmt.Sprintf("zfs destroy -R %s", snapshotName)

	if _, err := m.runner.Run(cmd); err != nil {
		return errors.Wrap(err, "failed to run command")
	}

	return nil
}

// CleanupSnapshots destroys old snapshots considering retention limit.
func (m *Manager) CleanupSnapshots(retentionLimit int) ([]string, error) {
	cleanupCmd := fmt.Sprintf(
		"zfs list -t snapshot -H -o name -s %s -s creation -r %s | grep -v clone | head -n -%d "+
			"| xargs -n1 --no-run-if-empty zfs destroy -R ",
		dataStateAtLabel, m.config.Pool.Name, retentionLimit)

	out, err := m.runner.Run(cleanupCmd)
	if err != nil {
		return nil, errors.Wrap(err, "failed to clean up snapshots")
	}

	lines := strings.Split(out, "\n")

	return lines, nil
}

// GetSessionState returns a state of a session.
func (m *Manager) GetSessionState(name string) (*resources.SessionState, error) {
	entries, err := m.listFilesystems(m.config.Pool.Name)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list filesystems")
	}

	var sEntry *ListEntry

	entryName := m.config.Pool.Name + "/" + name

	for _, entry := range entries {
		if entry.Name == entryName {
			sEntry = entry
			break
		}
	}

	if sEntry == nil {
		return nil, errors.New("cannot get session state: specified ZFS pool does not exist")
	}

	state := &resources.SessionState{
		CloneDiffSize: sEntry.Used,
	}

	return state, nil
}

// GetDiskState returns a disk state.
func (m *Manager) GetDiskState() (*resources.Disk, error) {
	parts := strings.SplitN(m.config.Pool.Name, "/", 2)
	if len(parts) == 0 {
		return nil, errors.New("failed to get a filesystem pool name")
	}

	parentPool := parts[0]

	entries, err := m.listFilesystems(parentPool)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list filesystems")
	}

	var parentPoolEntry, poolEntry *ListEntry

	for _, entry := range entries {
		if entry.Name == parentPool {
			parentPoolEntry = entry
		}

		if entry.Name == m.config.Pool.Name {
			poolEntry = entry
		}

		if parentPoolEntry != nil && poolEntry != nil {
			break
		}
	}

	if parentPoolEntry == nil || poolEntry == nil {
		return nil, errors.New("cannot get disk state: pool entries not found")
	}

	disk := &resources.Disk{
		Size:     parentPoolEntry.Available + parentPoolEntry.Used,
		Free:     parentPoolEntry.Available,
		Used:     parentPoolEntry.Used,
		DataSize: poolEntry.LogicalReferenced,
	}

	return disk, nil
}

// GetSnapshots returns a snapshot list.
func (m *Manager) GetSnapshots() ([]resources.Snapshot, error) {
	entries, err := m.listSnapshots(m.config.Pool.Name)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list snapshots")
	}

	snapshots := make([]resources.Snapshot, 0, len(entries))

	for _, entry := range entries {
		// Filter pre-snapshots, they will not be allowed to be used for cloning.
		if strings.HasSuffix(entry.Name, m.config.PreSnapshotSuffix) {
			continue
		}

		snapshot := resources.Snapshot{
			ID:          entry.Name,
			CreatedAt:   entry.Creation,
			DataStateAt: entry.DataStateAt,
		}

		snapshots = append(snapshots, snapshot)
	}

	return snapshots, nil
}

// ListFilesystems lists ZFS file systems (clones, pools).
func (m *Manager) listFilesystems(pool string) ([]*ListEntry, error) {
	return m.listDetails(pool, "filesystem")
}

// ListSnapshots lists ZFS snapshots.
func (m *Manager) listSnapshots(pool string) ([]*ListEntry, error) {
	return m.listDetails(pool, "snapshot")
}

// listDetails lists all ZFS types.
func (m *Manager) listDetails(pool, dsType string) ([]*ListEntry, error) {
	// TODO(anatoly): Return map.
	// TODO(anatoly): Generalize.
	numberFields := 12
	listCmd := "zfs list -po name,used,mountpoint,compressratio,available,type," +
		"origin,creation,referenced,logicalreferenced,logicalused," + dataStateAtLabel + " " +
		"-S " + dataStateAtLabel + " -S creation " + // Order DESC.
		"-t " + dsType + " " +
		"-r " + pool

	out, err := m.runner.Run(listCmd, true)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list details")
	}

	lines := strings.Split(out, "\n")

	// First line is header.
	if len(lines) <= headerOffset {
		return nil, errors.Errorf(`ZFS error: no available %s for pool %q`, dsType, pool)
	}

	entries := make([]*ListEntry, len(lines)-headerOffset)

	for i := headerOffset; i < len(lines); i++ {
		fields := strings.Fields(lines[i])

		// Empty value of standard ZFS params is "-", but for custom
		// params it will be just an empty string. Which mean that fields
		// array contain less elements. It's still bad to not have our
		// custom variables, but we don't want fail completely in this case.
		if len(fields) == numberFields-1 {
			log.Dbg(fmt.Sprintf("Probably %q is not set. Manually check ZFS snapshots.", dataStateAtLabel))

			fields = append(fields, "-")
		}

		// In other cases something really wrong with output format.
		if len(fields) != numberFields {
			return nil, errors.Errorf("ZFS error: some fields are empty. First of all, check " + dataStateAtLabel)
		}

		zfsListEntry := &ListEntry{
			Name:       fields[0],
			MountPoint: fields[2],
			Type:       fields[5],
			Origin:     fields[6],
		}

		setRules := []setTuple{
			{field: fields[1], setFunc: zfsListEntry.setUsed},
			{field: fields[3], setFunc: zfsListEntry.setCompressRatio},
			{field: fields[4], setFunc: zfsListEntry.setAvailable},
			{field: fields[7], setFunc: zfsListEntry.setCreation},
			{field: fields[8], setFunc: zfsListEntry.setReferenced},
			{field: fields[9], setFunc: zfsListEntry.setLogicalReferenced},
			{field: fields[10], setFunc: zfsListEntry.setLogicalUsed},
			{field: fields[11], setFunc: zfsListEntry.setDataStateAt},
		}

		for _, rule := range setRules {
			if len(rule.field) == 0 || rule.field == "-" {
				continue
			}

			if err := rule.setFunc(rule.field); err != nil {
				return nil, errors.Errorf("ZFS error: cannot parse output.\nCommand: %s.\nOutput: %s\nErr: %v",
					listCmd, out, err)
			}
		}

		entries[i-1] = zfsListEntry
	}

	return entries, nil
}

func (z *ListEntry) setUsed(field string) error {
	used, err := util.ParseBytes(field)
	if err != nil {
		return err
	}

	z.Used = used

	return nil
}

func (z *ListEntry) setCompressRatio(field string) error {
	ratioStr := strings.ReplaceAll(field, "x", "")

	compressRatio, err := strconv.ParseFloat(ratioStr, 64)
	if err != nil {
		return err
	}

	z.CompressRatio = compressRatio

	return nil
}

func (z *ListEntry) setAvailable(field string) error {
	available, err := util.ParseBytes(field)
	if err != nil {
		return err
	}

	z.Available = available

	return nil
}

func (z *ListEntry) setCreation(field string) error {
	creation, err := util.ParseUnixTime(field)
	if err != nil {
		return err
	}

	z.Creation = creation

	return nil
}

func (z *ListEntry) setReferenced(field string) error {
	referenced, err := util.ParseBytes(field)
	if err != nil {
		return err
	}

	z.Referenced = referenced

	return nil
}

func (z *ListEntry) setLogicalReferenced(field string) error {
	logicalReferenced, err := util.ParseBytes(field)
	if err != nil {
		return err
	}

	z.LogicalReferenced = logicalReferenced

	return nil
}

func (z *ListEntry) setLogicalUsed(field string) error {
	logicalUsed, err := util.ParseBytes(field)
	if err != nil {
		return err
	}

	z.LogicalUsed = logicalUsed

	return nil
}

func (z *ListEntry) setDataStateAt(field string) error {
	stateAt, err := util.ParseCustomTime(field)
	if err != nil {
		return err
	}

	z.DataStateAt = stateAt

	return nil
}
