/*
2019 © Postgres.ai
*/

// Package config provides access to the Database Lab configuration.
package config

import (
	"fmt"
	"io/ioutil"
	"os/user"
	"path"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"

	retConfig "gitlab.com/postgres-ai/database-lab/pkg/retrieval/config"
	"gitlab.com/postgres-ai/database-lab/pkg/services/cloning"
	"gitlab.com/postgres-ai/database-lab/pkg/services/platform"
	"gitlab.com/postgres-ai/database-lab/pkg/services/provision"
	"gitlab.com/postgres-ai/database-lab/pkg/srv"
	"gitlab.com/postgres-ai/database-lab/pkg/util"
)

// Config contains a common database-lab configuration.
type Config struct {
	Server    srv.Config       `yaml:"server"`
	Provision provision.Config `yaml:"provision"`
	Cloning   cloning.Config   `yaml:"cloning"`
	Platform  platform.Config  `yaml:"platform"`
	Global    Global           `yaml:"global"`
	Retrieval retConfig.Config `yaml:"retrieval"`
}

// Global contains global Database Lab configurations.
type Global struct {
	InstanceID     string
	Engine         string `yaml:"engine"`
	Debug          bool   `yaml:"debug"`
	MountDir       string `yaml:"mountDir"`
	DataSubDir     string `yaml:"dataSubDir"`
	ClonesMountDir string // TODO (akartasov): Use ClonesMountDir for the LocalModeOptions of a Provision service.
}

// DataDir provides full path to data directory.
func (g Global) DataDir() string {
	return path.Join(g.MountDir, g.DataSubDir)
}

// LoadConfig instances a new Config by configuration filename.
func LoadConfig(name string) (*Config, error) {
	configPath, err := util.GetConfigPath(name)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get config path")
	}

	b, err := ioutil.ReadFile(configPath)
	if err != nil {
		return nil, errors.Errorf("error loading %s config file", name)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("error parsing %s config", name)
	}

	if err := cfg.setUpProvisionParams(); err != nil {
		return nil, errors.Wrap(err, "failed to set up provision options")
	}

	return cfg, nil
}

func (cfg *Config) setUpProvisionParams() error {
	osUser, err := user.Current()
	if err != nil {
		return errors.Wrap(err, "failed to get current user")
	}

	cfg.Provision.OSUsername = osUser.Username
	cfg.Provision.MountDir = cfg.Global.MountDir
	cfg.Provision.DataSubDir = cfg.Global.DataSubDir

	return nil
}
