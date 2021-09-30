/*
2019 © Postgres.ai
*/

// TODO(anatoly):
// - Validate configs in all components.
// - Tests.

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docker/docker/client"
	"github.com/pkg/errors"

	"gitlab.com/postgres-ai/database-lab/v2/pkg/config"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/config/global"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/estimator"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/log"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/observer"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/retrieval"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/retrieval/engine/postgres/tools/cont"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/services/cloning"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/services/platform"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/services/provision"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/services/provision/pool"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/services/provision/resources"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/services/provision/runners"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/srv"
	"gitlab.com/postgres-ai/database-lab/v2/pkg/util/networks"
)

const (
	shutdownTimeout = 30 * time.Second
)

func main() {
	cfg, err := config.LoadConfiguration()
	if err != nil {
		log.Fatal(errors.WithMessage(err, "failed to parse config"))
	}

	log.Msg("Database Lab Instance ID:", cfg.Global.InstanceID)

	runner := runners.NewLocalRunner(cfg.Provision.UseSudo)

	pm := pool.NewPoolManager(&cfg.PoolManager, runner)
	if err := pm.ReloadPools(); err != nil {
		log.Fatal(err.Error())
	}

	dockerCLI, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		log.Fatal("Failed to create a Docker client:", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hostname := os.Getenv("HOSTNAME")
	if hostname == "" {
		log.Err("hostname is empty")
	}

	internalNetwork, err := networks.Setup(ctx, dockerCLI, cfg.Global.InstanceID, hostname)
	if err != nil {
		log.Errf(err.Error())
		return
	}

	defer networks.Stop(dockerCLI, internalNetwork.ID)

	// Create a platform service to make requests to Platform.
	platformSvc, err := platform.New(ctx, cfg.Platform)
	if err != nil {
		log.Errf(errors.WithMessage(err, "failed to create a new platform service").Error())
		return
	}

	dbCfg := &resources.DB{
		Username: cfg.Global.Database.User(),
		DBName:   cfg.Global.Database.Name(),
	}

	// Create a cloning service to provision new clones.
	provisionSvc, err := provision.New(ctx, &cfg.Provision, dbCfg, dockerCLI, pm, internalNetwork.ID)
	if err != nil {
		log.Errf(errors.WithMessage(err, `error in the "provision" section of the config`).Error())
	}

	observingChan := make(chan string, 1)

	cloningSvc := cloning.NewBase(&cfg.Cloning, provisionSvc, observingChan)

	emergencyShutdown := func() {
		cancel()

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()

		shutdownDatabaseLabEngine(shutdownCtx, dockerCLI, cfg.Global, pm.Active().Pool(), cloningSvc)
	}

	// Create a new retrieval service to prepare a data directory and start snapshotting.
	retrievalSvc := retrieval.New(cfg, dockerCLI, pm, runner)

	if err := retrievalSvc.Run(ctx); err != nil {
		log.Err("Failed to run the data retrieval service:", err)
		emergencyShutdown()

		return
	}

	defer retrievalSvc.Stop()

	if err = cloningSvc.Run(ctx); err != nil {
		log.Err(err)
		emergencyShutdown()

		return
	}

	obs := observer.NewObserver(dockerCLI, &cfg.Observer, platformSvc.Client, pm)
	est := estimator.NewEstimator(&cfg.Estimator)

	go removeObservingClones(observingChan, obs)

	server := srv.NewServer(&cfg.Server, &cfg.Global, obs, cloningSvc, platformSvc, dockerCLI, est, pm)
	shutdownCh := setShutdownListener()

	go setReloadListener(ctx, provisionSvc, retrievalSvc, pm, cloningSvc, platformSvc, est, server)

	server.InitHandlers()

	go func() {
		// Start the Database Lab.
		if err = server.Run(); err != nil {
			log.Msg(err)
		}
	}()

	<-shutdownCh
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Msg(err)
	}

	shutdownDatabaseLabEngine(shutdownCtx, dockerCLI, cfg.Global, pm.Active().Pool(), cloningSvc)
}

func reloadConfig(ctx context.Context, provisionSvc *provision.Provisioner, retrievalSvc *retrieval.Retrieval,
	pm *pool.Manager, cloningSvc *cloning.Base, platformSvc *platform.Service, est *estimator.Estimator, server *srv.Server) error {
	cfg, err := config.LoadConfiguration()
	if err != nil {
		return err
	}

	if err := provision.IsValidConfig(cfg.Provision); err != nil {
		return err
	}

	if err := retrieval.IsValidConfig(cfg); err != nil {
		return err
	}

	newPlatformSvc, err := platform.New(ctx, cfg.Platform)
	if err != nil {
		return err
	}

	if err := pm.Reload(cfg.PoolManager); err != nil {
		return err
	}

	dbCfg := resources.DB{
		Username: cfg.Global.Database.User(),
		DBName:   cfg.Global.Database.Name(),
	}

	provisionSvc.Reload(cfg.Provision, dbCfg)
	retrievalSvc.Reload(ctx, cfg)
	cloningSvc.Reload(cfg.Cloning)
	platformSvc.Reload(newPlatformSvc)
	est.Reload(cfg.Estimator)
	server.Reload(cfg.Server)

	return nil
}

func setReloadListener(ctx context.Context, provisionSvc *provision.Provisioner, retrievalSvc *retrieval.Retrieval,
	pm *pool.Manager, cloningSvc *cloning.Base, platformSvc *platform.Service, est *estimator.Estimator, server *srv.Server) {
	reloadCh := make(chan os.Signal, 1)
	signal.Notify(reloadCh, syscall.SIGHUP)

	for range reloadCh {
		log.Msg("Reloading configuration")

		if err := reloadConfig(ctx, provisionSvc, retrievalSvc, pm, cloningSvc, platformSvc, est, server); err != nil {
			log.Err("Failed to reload configuration", err)
		}

		log.Msg("Configuration has been reloaded")
	}
}

func setShutdownListener() chan os.Signal {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	return c
}

func shutdownDatabaseLabEngine(ctx context.Context, dockerCLI *client.Client, global global.Config, fsp *resources.Pool,
	cloningSvc *cloning.Base) {
	log.Msg("Stopping control containers")

	if err := cont.StopControlContainers(ctx, dockerCLI, global.InstanceID, fsp.DataDir()); err != nil {
		log.Err("Failed to stop control containers", err)
	}

	log.Msg("Control containers have been stopped")

	log.Msg("Saving state of running clones")

	cloningSvc.SaveClonesState()
}

func removeObservingClones(obsCh chan string, obs *observer.Observer) {
	for cloneID := range obsCh {
		obs.RemoveObservingClone(cloneID)
	}
}
