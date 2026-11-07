package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"yagp/internal/buildinfo"
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
	if len(args) > 0 {
		switch args[0] {
		case "--version":
			_, err := fmt.Fprintln(os.Stdout, buildinfo.Version)
			return err
		case "enroll":
			return enroll(ctx, args[1:])
		case "prepare-gost":
			return prepareGost(ctx, args[1:])
		case "ensure-gost":
			return ensureGost(ctx, parseConfigFlag(args[0], args[1:]))
		case "nft-template":
			return printNftTemplate(ctx, parseConfigFlag(args[0], args[1:]))
		}
	}
	configFile := parseConfigFlag("yagp-node", args)
	config, err := readConfig(configFile)
	if err != nil {
		return err
	}
	config.NftConfigFile = filepath.Join(filepath.Dir(configFile), "nft.json")
	return nodebridge.Run(ctx, config)
}

func printNftTemplate(ctx context.Context, configFile string) error {
	config, err := readConfig(configFile)
	if err != nil {
		return err
	}
	template, err := nodebridge.GetNftTemplate(ctx, config)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, template)
	return err
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
