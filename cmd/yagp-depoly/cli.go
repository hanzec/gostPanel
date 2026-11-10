package main

import (
	"errors"
	"fmt"
	"log"
	"os"

	"yagp/internal/buildinfo"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: yagp-depoly version | serve")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "version":
		if len(args) == 1 {
			fmt.Println(buildinfo.Version)
			return nil
		}
	}
	return fmt.Errorf("invalid yagp-depoly command %q", args[0])
}
