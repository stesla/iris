package cmd

import (
	"context"
	"os"
	"strings"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	serve "github.com/stesla/iris/cmd/serve"
)

var (
	logger  = zerolog.New(os.Stdout)
	rootCmd = &cobra.Command{
		Use:   "iris",
		Short: "a MU proxy",
		Long:  "Iris is a MU proxy that provides centralized logging and persistent connections. This application both runs the server and manages the database from the command line.",
	}
)

func Execute() error {
	ctx := context.WithValue(context.Background(), "logger", &logger)
	return rootCmd.ExecuteContext(ctx)
}

func init() {
	cobra.OnInitialize(initConfig)

	viper.SetEnvPrefix("IRIS")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	viper.SetDefault("addr", ":4001")
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.dir", "./logs")

	serve.AddToCommand(rootCmd)
}

func initConfig() {
	if config := viper.GetString("config"); config != "" {
		viper.SetConfigFile(config)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath("$HOME/.config/iris")
		viper.AddConfigPath(".")
	}

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		logger.Fatal().Err(err).Msg("error loading config")
	}

	if l, err := zerolog.ParseLevel(viper.GetString("log.level")); err != nil {
		logger.Fatal().Str("level", viper.GetString("log.level")).Msg("invalid level")
	} else {
		logger = logger.Level(l)
	}
}
