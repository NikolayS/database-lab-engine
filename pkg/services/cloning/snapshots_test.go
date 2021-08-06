/*
2021 © Postgres.ai
*/

package cloning

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitlab.com/postgres-ai/database-lab/v2/pkg/models"
)

func (s *BaseCloningSuite) TestLatestSnapshot() {
	s.cloning.resetSnapshots(make(map[string]*models.Snapshot))

	snapshot1 := &models.Snapshot{
		ID:          "TestSnapshotID1",
		CreatedAt:   "2020-02-20 01:23:45",
		DataStateAt: "2020-02-19 00:00:00",
	}

	snapshot2 := &models.Snapshot{
		ID:          "TestSnapshotID2",
		CreatedAt:   "2020-02-20 05:43:21",
		DataStateAt: "2020-02-20 00:00:00",
	}

	assert.Equal(s.T(), 0, len(s.cloning.snapshotBox.items))
	latestSnapshot, err := s.cloning.getLatestSnapshot()
	assert.Nil(s.T(), latestSnapshot)
	assert.EqualError(s.T(), err, "no snapshot found")

	s.cloning.addSnapshot(snapshot1)
	s.cloning.addSnapshot(snapshot2)

	latestSnapshot, err = s.cloning.getLatestSnapshot()
	require.NoError(s.T(), err)
	assert.Equal(s.T(), latestSnapshot, snapshot1)
}

func (s *BaseCloningSuite) TestSnapshotByID() {
	s.cloning.resetSnapshots(make(map[string]*models.Snapshot))

	snapshot1 := &models.Snapshot{
		ID:          "TestSnapshotID1",
		CreatedAt:   "2020-02-20 01:23:45",
		DataStateAt: "2020-02-19 00:00:00",
	}

	snapshot2 := &models.Snapshot{
		ID:          "TestSnapshotID2",
		CreatedAt:   "2020-02-20 05:43:21",
		DataStateAt: "2020-02-20 00:00:00",
	}

	assert.Equal(s.T(), 0, len(s.cloning.snapshotBox.items))
	latestSnapshot, err := s.cloning.getLatestSnapshot()
	assert.Nil(s.T(), latestSnapshot)
	assert.EqualError(s.T(), err, "no snapshot found")

	s.cloning.addSnapshot(snapshot1)
	assert.Equal(s.T(), 1, len(s.cloning.snapshotBox.items))
	assert.Equal(s.T(), "TestSnapshotID1", s.cloning.snapshotBox.items[snapshot1.ID].ID)

	s.cloning.addSnapshot(snapshot2)
	assert.Equal(s.T(), 2, len(s.cloning.snapshotBox.items))
	assert.Equal(s.T(), "TestSnapshotID2", s.cloning.snapshotBox.items[snapshot2.ID].ID)

	latestSnapshot, err = s.cloning.getSnapshotByID("TestSnapshotID2")
	require.NoError(s.T(), err)
	assert.Equal(s.T(), latestSnapshot, snapshot2)

	s.cloning.resetSnapshots(make(map[string]*models.Snapshot))
	assert.Equal(s.T(), 0, len(s.cloning.snapshotBox.items))
	latestSnapshot, err = s.cloning.getLatestSnapshot()
	assert.Nil(s.T(), latestSnapshot)
	assert.EqualError(s.T(), err, "no snapshot found")
}

func TestCloneCounter(t *testing.T) {
	c := &baseCloning{}
	c.snapshotBox.items = make(map[string]*models.Snapshot)
	snapshot := &models.Snapshot{
		ID:        "testSnapshotID",
		NumClones: 0,
	}
	c.snapshotBox.items[snapshot.ID] = snapshot

	snapshot, err := c.getSnapshotByID("testSnapshotID")
	require.Nil(t, err)
	assert.Equal(t, 0, snapshot.NumClones)

	c.incrementCloneNumber("testSnapshotID")
	snapshot, err = c.getSnapshotByID("testSnapshotID")
	require.Nil(t, err)
	assert.Equal(t, 1, snapshot.NumClones)

	c.decrementCloneNumber("testSnapshotID")
	snapshot, err = c.getSnapshotByID("testSnapshotID")
	require.Nil(t, err)
	assert.Equal(t, 0, snapshot.NumClones)
}
