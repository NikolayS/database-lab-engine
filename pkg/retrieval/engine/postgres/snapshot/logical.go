/*
2020 © Postgres.ai
*/

// Package snapshot provides components for preparing initial snapshots.
package snapshot

import (
	"context"
	"fmt"
	"path"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/pkg/errors"

	dblabCfg "gitlab.com/postgres-ai/database-lab/v2/pkg/config"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/log"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/retrieval/config"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/retrieval/dbmarker"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/retrieval/engine/postgres/tools"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/retrieval/engine/postgres/tools/cont"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/retrieval/engine/postgres/tools/health"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/retrieval/options"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/services/provision/databases/postgres/pgconfig"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/services/provision/pool"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/services/provision/resources"
)

// LogicalInitial describes a job for preparing a logical initial snapshot.
type LogicalInitial struct {
	name           string
	cloneManager   pool.FSManager
	fsPool         *resources.Pool
	dockerClient   *client.Client
	options        LogicalOptions
	globalCfg      *dblabCfg.Global
	dbMarker       *dbmarker.Marker
	queryProcessor *queryProcessor
}

// LogicalOptions describes options for a logical initialization job.
type LogicalOptions struct {
	DataPatching        DataPatching      `yaml:"dataPatching"`
	PreprocessingScript string            `yaml:"preprocessingScript"`
	Configs             map[string]string `yaml:"configs"`
	Schedule            Scheduler         `yaml:"schedule"`
}

// DataPatching allows executing queries to transform data before snapshot taking.
type DataPatching struct {
	DockerImage        string             `yaml:"dockerImage"`
	QueryPreprocessing QueryPreprocessing `yaml:"queryPreprocessing"`
}

const (
	// LogicalInitialType declares a job type for preparing a logical initial snapshot.
	LogicalInitialType = "logicalSnapshot"

	patchContainerPrefix = "dblab_patch_"
)

// NewLogicalInitialJob creates a new logical initial job.
func NewLogicalInitialJob(cfg config.JobConfig, global *dblabCfg.Global, cloneManager pool.FSManager) (*LogicalInitial, error) {
	li := &LogicalInitial{
		name:         cfg.Spec.Name,
		cloneManager: cloneManager,
		fsPool:       cfg.FSPool,
		dockerClient: cfg.Docker,
		globalCfg:    global,
		dbMarker:     cfg.Marker,
	}

	if err := li.Reload(cfg.Spec.Options); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal configuration options")
	}

	if li.options.DataPatching.QueryPreprocessing.QueryPath != "" {
		li.queryProcessor = newQueryProcessor(cfg.Docker, global.Database.Name(), global.Database.User(),
			li.options.DataPatching.QueryPreprocessing.QueryPath,
			li.options.DataPatching.QueryPreprocessing.MaxParallelWorkers)
	}

	return li, nil
}

// Name returns a name of the job.
func (s *LogicalInitial) Name() string {
	return s.name
}

// patchContainerName returns container name.
func (s *LogicalInitial) patchContainerName() string {
	return patchContainerPrefix + s.globalCfg.InstanceID
}

// Reload reloads job configuration.
func (s *LogicalInitial) Reload(cfg map[string]interface{}) (err error) {
	return options.Unmarshal(cfg, &s.options)
}

// Run starts the job.
func (s *LogicalInitial) Run(ctx context.Context) error {
	if s.options.PreprocessingScript != "" {
		if err := runPreprocessingScript(s.options.PreprocessingScript); err != nil {
			return err
		}
	}

	if err := s.touchConfigFiles(); err != nil {
		return errors.Wrap(err, "failed to create PostgreSQL configuration files")
	}

	dataDir := s.fsPool.DataDir()

	// Run basic PostgreSQL configuration.
	cfgManager, err := pgconfig.NewCorrector(dataDir)
	if err != nil {
		return errors.Wrap(err, "failed to create a config manager")
	}

	// Apply snapshot-specific configs.
	if err := cfgManager.ApplySnapshot(s.options.Configs); err != nil {
		return errors.Wrap(err, "failed to store PostgreSQL configs for the snapshot")
	}

	if s.queryProcessor != nil {
		if err := s.runPreprocessingQueries(ctx, dataDir); err != nil {
			return errors.Wrap(err, "failed to run preprocessing queries")
		}
	}

	dataStateAt := extractDataStateAt(s.dbMarker)

	if _, err := s.cloneManager.CreateSnapshot("", dataStateAt); err != nil {
		return errors.Wrap(err, "failed to create a snapshot")
	}

	return nil
}

func (s *LogicalInitial) touchConfigFiles() error {
	dataDir := s.fsPool.DataDir()

	if err := tools.TouchFile(path.Join(dataDir, "postgresql.conf")); err != nil {
		return err
	}

	return tools.TouchFile(path.Join(dataDir, "pg_hba.conf"))
}

func (s *LogicalInitial) runPreprocessingQueries(ctx context.Context, dataDir string) error {
	pgVersion, err := tools.DetectPGVersion(dataDir)
	if err != nil {
		return errors.Wrap(err, "failed to detect the Postgres version")
	}

	patchImage := s.options.DataPatching.DockerImage
	if patchImage == "" {
		patchImage = fmt.Sprintf("postgresai/extended-postgres:%g", pgVersion)
	}

	if err := tools.PullImage(ctx, s.dockerClient, patchImage); err != nil {
		return errors.Wrap(err, "failed to scan image pulling response")
	}

	pwd, err := tools.GeneratePassword()
	if err != nil {
		return errors.Wrap(err, "failed to generate PostgreSQL password")
	}

	hostConfig, err := s.buildHostConfig(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to build container host config")
	}

	// Run patch container.
	patchCont, err := s.dockerClient.ContainerCreate(ctx,
		s.buildContainerConfig(dataDir, patchImage, pwd),
		hostConfig,
		&network.NetworkingConfig{},
		s.patchContainerName(),
	)
	if err != nil {
		return errors.Wrap(err, "failed to create container")
	}

	defer tools.RemoveContainer(ctx, s.dockerClient, patchCont.ID, cont.StopPhysicalTimeout)

	defer func() {
		if err != nil {
			tools.PrintContainerLogs(ctx, s.dockerClient, s.patchContainerName())
			tools.PrintLastPostgresLogs(ctx, s.dockerClient, s.patchContainerName(), dataDir)
		}
	}()

	log.Msg(fmt.Sprintf("Running container: %s. ID: %v", s.patchContainerName(), patchCont.ID))

	if err := s.dockerClient.ContainerStart(ctx, patchCont.ID, types.ContainerStartOptions{}); err != nil {
		return errors.Wrap(err, "failed to start container")
	}

	log.Msg("Starting PostgreSQL and waiting for readiness")
	log.Msg(fmt.Sprintf("View logs using the command: %s %s", tools.ViewLogsCmd, s.patchContainerName()))

	if err := tools.CheckContainerReadiness(ctx, s.dockerClient, patchCont.ID); err != nil {
		return errors.Wrap(err, "failed to readiness check")
	}

	if err := s.queryProcessor.applyPreprocessingQueries(ctx, patchCont.ID); err != nil {
		return errors.Wrap(err, "failed to run preprocessing queries")
	}

	return nil
}

func (s *LogicalInitial) buildHostConfig(ctx context.Context) (*container.HostConfig, error) {
	hostConfig := &container.HostConfig{}

	if err := tools.AddVolumesToHostConfig(ctx, s.dockerClient, hostConfig, s.fsPool.DataDir()); err != nil {
		return nil, err
	}

	return hostConfig, nil
}

func (s *LogicalInitial) buildContainerConfig(clonePath, patchImage, password string) *container.Config {
	hcInterval := health.DefaultRestoreInterval
	hcRetries := health.DefaultRestoreRetries

	return &container.Config{
		Labels: map[string]string{
			cont.DBLabControlLabel:    cont.DBLabPatchLabel,
			cont.DBLabInstanceIDLabel: s.globalCfg.InstanceID,
		},
		Env: []string{
			"PGDATA=" + clonePath,
			"POSTGRES_PASSWORD=" + password,
		},
		Image: patchImage,
		Healthcheck: health.GetConfig(
			s.globalCfg.Database.User(),
			s.globalCfg.Database.Name(),
			health.OptionInterval(hcInterval),
			health.OptionRetries(hcRetries),
		),
	}
}
