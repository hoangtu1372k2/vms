package main

import (
	"github.com/hoangtu1372k2/vms/internal/app"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

func main() {

	// Initiate a simple logger
	log := logrus.New()

	// Setup Config
	cfg := viper.New()

	// Set Default Configs
	// Important: Viper configuration keys are case insensitive.
	cfg.SetDefault("enable_tls", false)
	cfg.SetDefault("listen_addr", "0.0.0.0:8080")
	cfg.SetDefault("base_url", "127.0.0.1:8080")
	cfg.SetDefault("service_path", "/vms/api/v0")

	cfg.SetDefault("jwk_set_uri", "")

	cfg.SetDefault("redis_host", "localhost")
	cfg.SetDefault("redis_post", "6379")

	cfg.SetDefault("global_limit", "5")
	cfg.SetDefault("rate_limit_fixed", "5")
	cfg.SetDefault("rate_limit_sliding", "5")
	cfg.SetDefault("rate_limit_tokens", "15")

	// Connect PosgreSQL
	cfg.SetDefault("enable_sql", true)
	cfg.SetDefault("sql_host", "")
	cfg.SetDefault("sql_port", 5432)
	cfg.SetDefault("sql_sslmode", "disable")
	cfg.SetDefault("sql_dbname", "vms")
	cfg.SetDefault("sql_user", "")
	cfg.SetDefault("sql_password", "")
	cfg.SetDefault("sql_schema", "")

	// Load Config
	cfg.AddConfigPath("./conf")
	cfg.SetEnvPrefix("app")
	cfg.AllowEmptyEnv(true)
	cfg.AutomaticEnv()
	err := cfg.ReadInConfig()
	if err != nil {
		switch err.(type) {
		case viper.ConfigFileNotFoundError:
			log.Warnf("No Config file found, loaded config from Environment - Default path ./conf")
		default:
			log.Fatalf("Error when Fetching Configuration - %s", err)
		}
	}

	// Load Config from Consul
	if cfg.GetBool("use_consul") {
		log.Infof("Setting up Consul Config source - %s/%s", cfg.GetString("consul_addr"), cfg.GetString("consul_keys_prefix"))
		err = cfg.AddRemoteProvider("consul", cfg.GetString("consul_addr"), cfg.GetString("consul_keys_prefix"))
		if err != nil {
			log.Fatalf("Error adding Consul as a remote Configuration Provider - %s", err)
		}

		cfg.SetConfigType("json")
		err = cfg.ReadRemoteConfig()
		if err != nil {
			log.Fatalf("Error when Fetching Configuration from Consul - %s", err)
		}

		if cfg.GetBool("from_consul") {
			log.Infof("Successfully loaded configuration from consul")
		}
	}

	// Run application
	log.Info("Start running vms service............................................")
	err = app.Run(cfg)
	if err != nil && err != app.ErrShutdown {
		log.Fatalf("Service stopped - %s", err)
	}
	log.Infof("Service shutdown - %s", err)
}
