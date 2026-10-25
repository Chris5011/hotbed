package main

import (
	"fmt"
	"os"

	"sbxd/internal/cli"
)

func main() {
	if err := cli.Dispatch(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "\033[31;1msbxd: FAIL\033[0m %v\n", err)
		os.Exit(1)
	}
}
