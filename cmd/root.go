package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stesla/iris/cmd/serve"
	"github.com/stesla/iris/cmd/upstream"
)

var (
	rootCmd = &cobra.Command{
		Use:   "iris",
		Short: "a MU proxy",
		Long:  "Iris is a MU proxy that provides centralized logging and persistent connections. This application both runs the server and manages the database from the command line.",
	}
)

func init() {
	cobra.OnInitialize(initConfig)

	viper.SetEnvPrefix("IRIS")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	viper.SetDefault("addr", ":4042")
	viper.SetDefault("db", "./iris.db")
	viper.SetDefault("grpc.client.addr", "localhost:40042")
	viper.SetDefault("grpc.server.addr", ":40042")
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.dir", "./logs")

	serve.AddToCommand(rootCmd)
	upstream.AddToCommand(rootCmd)
}

func initConfig() {
	if config := viper.GetString("config"); config != "" {
		viper.SetConfigFile(config)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath(".")
		viper.AddConfigPath("$HOME/.config/iris")
	}

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		cobra.CheckErr(err)
	}
}

func Execute() error {
	return rootCmd.Execute()
}
