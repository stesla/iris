package upstream

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stesla/iris/api"
)

var (
	pkgcmd = &cobra.Command{
		Use:   "upstream [OPTIONS] COMMAND",
		Short: "commands for managing upstreams",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// args have been validated by now, so any error is from the server
			cmd.SilenceUsage = true
		},
	}
	address     string
	login       string
	newName     string
	newPassword string
	script      string
)

func init() {
	pkgcmd.PersistentFlags().StringVarP(&address, "address", "a", "", "address for upstream session")
	pkgcmd.PersistentFlags().StringVarP(&login, "login", "l", "", "login for upstream session")
	pkgcmd.PersistentFlags().StringVarP(&newName, "name", "n", "", "name for upstream session")
	pkgcmd.PersistentFlags().StringVarP(&newPassword, "password", "p", "", "password for upstream session")
	pkgcmd.PersistentFlags().StringVarP(&script, "script", "s", "", "connect script")
	pkgcmd.AddCommand(&cobra.Command{
		Use:   "add NAME PASSWORD",
		Short: "add a new upstream",
		Args:  cobra.ExactArgs(2),
		RunE:  Add,
	})
	pkgcmd.AddCommand(&cobra.Command{
		Use:   "delete NAME PASSWORD",
		Short: "delete an upstream",
		Args:  cobra.ExactArgs(2),
		RunE:  Delete,
	})
	pkgcmd.AddCommand(&cobra.Command{
		Use:   "edit NAME PASSWORD",
		Short: "edit an existing upstream",
		Args:  cobra.ExactArgs(2),
		RunE:  Edit,
	})
	pkgcmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list all upstreams",
		Args:  cobra.NoArgs,
		RunE:  List,
	})
}

func AddToCommand(cmd *cobra.Command) {
	cmd.AddCommand(pkgcmd)
}

func Add(cmd *cobra.Command, args []string) error {
	req := &api.AddUpstreamRequest{
		Name:     args[0],
		Address:  address,
		Login:    login,
		Password: args[1],
	}
	if script != "" {
		req.Script = &script
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return newClient().AddUpstream(ctx, req)
}

func Delete(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return newClient().DeleteUpstream(ctx, args[0], args[1])
}

func Edit(cmd *cobra.Command, args []string) error {
	req := &api.EditUpstreamRequest{}
	if address != "" {
		req.Address = &address
	}
	if login != "" {
		req.Login = &login
	}
	if newName != "" {
		req.NewName = &newName
	}
	if newPassword != "" {
		req.NewPassword = &newPassword
	}
	if script != "" {
		req.Script = &script
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return newClient().EditUpstream(ctx, args[0], args[1], req)
}

func List(cmd *cobra.Command, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	upstreams, err := newClient().ListUpstreams(ctx)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tADDRESS\tLOGIN")
	for _, u := range upstreams {
		fmt.Fprintf(w, "%s\t%s\t%s\n", u.Name, u.Address, u.Login)
	}
	return w.Flush()
}

func newClient() *api.Client {
	return api.NewClient(viper.GetString("api.client.url"))
}
