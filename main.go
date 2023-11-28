package main

import (
	"github.com/bitoku/difftool/pkg/cli"
)

func main() {
	err := cli.Run()
	if err != nil {
		panic(err.Error())
	}
}
