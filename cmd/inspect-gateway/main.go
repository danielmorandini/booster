package main

import (
	"github.com/danielmorandini/booster/cmd/inspect-gateway/commands"
)

// Version and BuildTime are filled in during build by the Makefile
var (
	Version   = "N/A"
	BuildTime = "N/A"
)

func main() {
	commands.Version = Version
	commands.BuildTime = BuildTime
	commands.Execute()
}
