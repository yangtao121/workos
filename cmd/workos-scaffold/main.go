package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yangtao121/workos/internal/platform/scaffold"
)

func main() {
	module := flag.NewFlagSet("module", flag.ExitOnError)
	root := module.String("root", ".", "repository root")
	process := module.String("process", "", "stable process name")
	name := module.String("name", "", "lowercase module name")
	if len(os.Args) < 2 || os.Args[1] != "module" {
		fmt.Fprintln(os.Stderr, "usage: workos-scaffold module --process workos-core --name calendar [--root .]")
		os.Exit(2)
	}
	if err := module.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}
	target, err := scaffold.CreateModule(*root, *process, *name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(target)
}
