package upstream

import (
	"context"
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/stesla/iris/api"
	"github.com/stesla/iris/internal/prompt"
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
	address  string
	login    string
	newName  string
	password string
	script   string
)

func init() {
	pkgcmd.PersistentFlags().StringVarP(&address, "address", "a", "", "address for upstream session")
	pkgcmd.PersistentFlags().StringVarP(&login, "login", "l", "", "login for upstream session")
	pkgcmd.PersistentFlags().StringVarP(&newName, "name", "n", "", "name for upstream session")
	pkgcmd.PersistentFlags().StringVarP(&password, "password", "p", "", "password for upstream session, or - to prompt for it")
	pkgcmd.PersistentFlags().StringVarP(&script, "script", "s", "", "connect script")
	pkgcmd.AddCommand(&cobra.Command{
		Use:   "add NAME -a ADDRESS -l LOGIN -p PASSWORD",
		Short: "add a new upstream",
		Long:  "Add a new upstream. The password (-p) is the password for the upstream account, which Iris sends in the connect script, and is required. With -p -, it is prompted for, or read from the first line of standard input if that is not a terminal, so it stays out of shell history.",
		Args:  cobra.ExactArgs(1),
		RunE:  Add,
	})
	pkgcmd.AddCommand(&cobra.Command{
		Use:   "delete NAME",
		Short: "delete an upstream",
		Args:  cobra.ExactArgs(1),
		RunE:  Delete,
	})
	pkgcmd.AddCommand(&cobra.Command{
		Use:   "edit NAME",
		Short: "edit an existing upstream",
		Long:  "Edit an existing upstream, changing only the settings given. With -p -, the new password is prompted for, or read from the first line of standard input if that is not a terminal, so it stays out of shell history.",
		Args:  cobra.ExactArgs(1),
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
	if password == "" {
		return errors.New("password is required: use -p PASSWORD, or -p - to be prompted for it")
	}
	req := &api.AddUpstreamRequest{
		Name:     args[0],
		Address:  address,
		Login:    login,
		Password: password,
	}
	if script != "" {
		req.Script = &script
	}
	// validate before prompting, so nothing is typed for a request that would
	// fail; "-" passes as a password until it is replaced
	if err := req.Validate(); err != nil {
		return err
	}

	client, err := newClient()
	if err != nil {
		return err
	}
	if req.Password, err = readPassword(req.Password); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return client.AddUpstream(ctx, req)
}

func Delete(cmd *cobra.Command, args []string) error {
	client, err := newClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return client.DeleteUpstream(ctx, args[0])
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
	if password != "" {
		req.Password = &password
	}
	if script != "" {
		req.Script = &script
	}
	// validate before prompting, so nothing is typed for a request that would
	// fail; "-" passes as a password until it is replaced
	if err := req.Validate(); err != nil {
		return err
	}

	client, err := newClient()
	if err != nil {
		return err
	}
	if req.Password != nil {
		p, err := readPassword(*req.Password)
		if err != nil {
			return err
		}
		req.Password = &p
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return client.EditUpstream(ctx, args[0], req)
}

func List(cmd *cobra.Command, args []string) error {
	client, err := newClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	upstreams, err := client.ListUpstreams(ctx)
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

// readPassword returns the upstream password given with -p, or prompts for
// it if that is "-".
func readPassword(flag string) (string, error) {
	if flag != "-" {
		return flag, nil
	}
	return prompt.ReadNewPassword("upstream password")
}

// newClient returns a client for the management API, authenticated with the
// Iris password from the api.client.password setting, or prompted for if
// that isn't set.
func newClient() (*api.Client, error) {
	password := viper.GetString("api.client.password")
	if password == "" {
		if !prompt.IsTerminal() {
			return nil, errors.New("iris password is required: set api.client.password or IRIS_API_CLIENT_PASSWORD")
		}
		var err error
		if password, err = prompt.Password("Iris password"); err != nil {
			return nil, err
		}
	}
	return api.NewClient(viper.GetString("api.client.url"), password), nil
}
