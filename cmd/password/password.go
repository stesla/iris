package password

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stesla/iris/internal/auth"
	"github.com/stesla/iris/internal/database"
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

	// open before prompting, so the password isn't typed for nothing
	ctx := context.Background()
	db, err := database.Open(ctx, viper.GetString("db"))
	if err != nil {
		return err
	}
	defer db.Close()

	password, err := prompt.ReadNewPassword("Iris password")
	if err != nil {
		return err
	}
	if err := auth.SetPassword(ctx, db, password); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Iris password set.")
	return nil
}
