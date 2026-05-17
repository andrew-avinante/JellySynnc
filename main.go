package main

import (
	"runtime/debug"

	"github.com/andrew-avinante/JellySynnc/cmd"
)

var version = "dev"

func main() {
	v := version
	if v == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
	}
	cmd.Execute(v)
}
