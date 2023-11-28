package main

import (
	"fmt"
	"os"

	"difftool/pkg/cli"
)

func main() {
	err := cli.Run()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%+v", err)
		if err != nil {
			return
		}
		panic(err.Error())
	}
}
