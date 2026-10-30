package password

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stesla/iris/internal/auth"
	"github.com/stesla/iris/internal/prompt"
)

func AddToCommand(cmd *cobra.Command) {
	cmd.AddCommand(&cobra.Command{
		Use:   "password",
		Short: "set the Iris password",
		Long:  "Set the password used to log in to Iris, through both the Telnet proxy and the management API. This writes directly to the database, so it works without the server running. When standard input is not a terminal, the password is read from its first line.",
		Args:  cobra.NoArgs,
		RunE:  SetPassword,
	})
}

func SetPassword(cmd *cobra.Command, args []string) error {
	// args have been validated by now, so any error isn't a usage error
	cmd.SilenceUsage = true

	path := viper.GetString("db")
	// sql.Open would silently create an empty database
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("opening database: %w", err)
	}

	password, err := prompt.ReadNewPassword("Iris password")
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := auth.SetPassword(context.Background(), db, password); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return fmt.Errorf("%w (have the migrations been run?)", err)
		}
		return err
	}
	fmt.Fprintln(os.Stderr, "Iris password set.")
	return nil
}
