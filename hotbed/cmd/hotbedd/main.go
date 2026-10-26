package main

import (
	"fmt"
	"os"

	"hotbed/internal/cli"
)

func main() {
	if err := cli.Dispatch(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "\033[31;1mhotbedd: FAIL\033[0m %v\n", err)
		os.Exit(1)
	}
}
