package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/danielmorandini/booster/booster"
	"github.com/spf13/cobra"
)

var connectCmd = &cobra.Command{
	Use:   "connect [host:port]",
	Short: "connect two nodes together",
	Long:  `connect asks (by default) the local node to perform the necessary steps required to connect an external node to itself. Returns the added node identifier if successfull. You can use the 'inspect' command to monitor node activity.`,
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		dest := strings.Join(args, " ")
		b := booster.NewBoosterDefault()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
		defer cancel()

		id, err := b.Connect(ctx, "tcp", boosterAddr, dest)
		if err != nil {
			fmt.Println(err)
			return
		}

		fmt.Printf("connected to (%v): %v\n", dest, id)
	},
}
