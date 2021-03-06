package main

import (
	"os"

	"github.com/nspcc-dev/neo-go/cli/server"
	"github.com/nspcc-dev/neo-go/cli/smartcontract"
	"github.com/nspcc-dev/neo-go/cli/util"
	"github.com/nspcc-dev/neo-go/cli/vm"
	"github.com/nspcc-dev/neo-go/cli/wallet"
	"github.com/nspcc-dev/neo-go/pkg/config"
	"github.com/urfave/cli"
)

func main() {
	ctl := cli.NewApp()
	ctl.Name = "neo-go"
	ctl.Version = config.Version
	ctl.Usage = "Official Go client for Neo"

	ctl.Commands = append(ctl.Commands, server.NewCommands()...)
	ctl.Commands = append(ctl.Commands, smartcontract.NewCommands()...)
	ctl.Commands = append(ctl.Commands, wallet.NewCommands()...)
	ctl.Commands = append(ctl.Commands, vm.NewCommands()...)
	ctl.Commands = append(ctl.Commands, util.NewCommands()...)

	if err := ctl.Run(os.Args); err != nil {
		panic(err)
	}
}
