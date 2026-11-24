// Command pair 是 Pairwise 的命令行入口.
package main

import (
	"os"

	"github.com/FooFishes/pairwise/internal/cli"
	"github.com/FooFishes/pairwise/internal/core"
)

func main() {
	os.Exit(cli.Execute(core.New))
}
