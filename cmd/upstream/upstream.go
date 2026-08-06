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

var upstreamCommand = &cobra.Command{
	Use:   "upstream",
	Short: "commands for managing upstreams",
}

func init() {
	upstreamCommand.AddCommand(&cobra.Command{
		Use:   "add NAME ADDRESS LOGIN PASSWORD [SCRIPT]",
		Short: "add a new upstream",
		Args:  cobra.ExactArgs(4),
		RunE:  Add,
	})
	upstreamCommand.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "list all upstreams",
		Args:  cobra.NoArgs,
		Run:   List,
	})
}

func AddToCommand(cmd *cobra.Command) {
	cmd.AddCommand(upstreamCommand)
}

func Add(cmd *cobra.Command, args []string) error {
	req := &api.AddUpstreamRequest{
		Upstream: &api.Upstream{
			Name:    &args[0],
			Address: &args[1],
			Login:   &args[2],
		},
		Password: &args[3],
	}
	if len(args) > 4 {
		req.Script = &args[4]
	}
	conn, err := grpcNew()
	cobra.CheckErr(err)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = conn.AddUpstream(ctx, req)
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
