package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"yagp/internal/nodebridge"

	"gopkg.in/yaml.v3"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "enroll" {
		return enroll(ctx, args[1:])
	}
	config, err := readConfig(parseConfigFlag("yagp-node", args))
	if err != nil {
		return err
	}
	return nodebridge.Run(ctx, config)
}

func parseConfigFlag(name string, args []string) string {
	flags := flag.NewFlagSet(name, flag.ExitOnError)
	configFile := flags.String("c", "node.yaml", "node bridge configuration file")
	_ = flags.Parse(args)
	return *configFile
}

func readConfig(configFile string) (nodebridge.Config, error) {
	var config nodebridge.Config
	file, err := os.Open(configFile)
	if err != nil {
		return config, err
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	err = decoder.Decode(&config)
	return config, err
}
