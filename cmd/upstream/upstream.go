package upstream

import (
	"database/sql"
	_ "github.com/mattn/go-sqlite3"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"
)

var upstreamCommand = &cobra.Command{
	Use:   "upstream",
	Short: "commands for managing upstreams",
}

func init() {
	upstreamCommand.AddCommand(&cobra.Command{
		Use:   "add NAME ADDRESS PASSWORD SCRIPT",
		Short: "add a new upstream",
		Args:  cobra.ExactArgs(4),
		RunE:  Add,
	})
}

func AddToCommand(cmd *cobra.Command) {
	cmd.AddCommand(upstreamCommand)
}

func Add(cmd *cobra.Command, args []string) error {
	key := args[0]
	address := args[1]
	password := args[2]
	script := args[3]

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	db, err := sql.Open("sqlite3", viper.GetString("db"))
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO upstreams (name, address, bcrypt, script) VALUES (?, ?, ?, ?)", key, address, hash, script)
	return err
}
