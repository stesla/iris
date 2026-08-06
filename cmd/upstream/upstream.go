package upstream

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/stesla/iris/api"
)

var (
	pkgcmd = &cobra.Command{
		Use:   "upstream [OPTIONS] COMMAND",
		Short: "commands for managing upstreams",
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
		Use:   "add",
		Short: "add a new upstream",
		Args:  cobra.ExactArgs(2),
		RunE:  Add,
	})
	pkgcmd.AddCommand(&cobra.Command{
		Use:   "edit NAME PASSWORD",
		Short: "edit an existing upstream",
		Args:  cobra.RangeArgs(2, 3),
		RunE:  Edit,
	})
	pkgcmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list all upstreams",
		Args:  cobra.NoArgs,
		Run:   List,
	})
}

func AddToCommand(cmd *cobra.Command) {
	cmd.AddCommand(pkgcmd)
}

func Add(cmd *cobra.Command, args []string) error {
	req := &api.AddUpstreamRequest{
		Upstream: &api.Upstream{
			Name:    &args[0],
			Address: &address,
			Login:   &login,
		},
		Password: &args[1],
	}
	if script != "" {
		req.Script = &script
	}
	conn, err := grpcNew()
	cobra.CheckErr(err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = conn.AddUpstream(ctx, req)
	return err
}

func Edit(cmd *cobra.Command, args []string) error {
	req := &api.EditUpstreamRequest{
		Name:     &args[0],
		Password: &args[1],
	}
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

	conn, err := grpcNew()
	cobra.CheckErr(err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = conn.EditUpstream(ctx, req)
	return err
}

func List(cmd *cobra.Command, args []string) {
	conn, err := grpcNew()
	cobra.CheckErr(err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := conn.ListUpstreams(ctx, &emptypb.Empty{})
	cobra.CheckErr(err)
	for _, name := range resp.Upstreams {
		fmt.Println(name)
	}
}

func grpcNew() (api.UpstreamsClient, error) {
	conn, err := grpc.NewClient(
		viper.GetString("grpc.client.addr"),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	return api.NewUpstreamsClient(conn), err
}
