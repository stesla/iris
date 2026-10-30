package cmd

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stesla/iris/cmd/password"
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

	setupViper()

	password.AddToCommand(rootCmd)
	serve.AddToCommand(rootCmd)
	upstream.AddToCommand(rootCmd)
}

func setupViper() {
	viper.SetEnvPrefix("IRIS")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	viper.SetDefault("addr", ":4042")
	viper.SetDefault("db", "./iris.db")
	viper.SetDefault("api.client.url", "http://localhost:40042")
	viper.SetDefault("api.server.addr", "localhost:40042")
	viper.SetDefault("log.level", "info")
	viper.SetDefault("log.dir", "./logs")
	viper.SetDefault("secret.key_file", "./iris.key")
}

func initConfig() {
	cobra.CheckErr(loadConfig())
}

func loadConfig() error {
	if config := viper.GetString("config"); config != "" {
		viper.SetConfigFile(config)
	} else {
		viper.SetConfigName("config")
		viper.AddConfigPath(".")
		viper.AddConfigPath("$HOME/.config/iris")
	}

	return viper.ReadInConfig()
}

func Execute() error {
	return rootCmd.Execute()
}
