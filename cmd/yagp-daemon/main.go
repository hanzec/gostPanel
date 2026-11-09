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
		case "--runtime-layout-version":
			_, err := fmt.Fprintln(os.Stdout, nodebridge.RuntimeLayoutVersion)
			return err
		case "enroll":
			return enroll(ctx, args[1:])
		case "prepare-gost":
			return prepareGost(ctx, args[1:])
		case "ensure-gost":
			return ensureGost(ctx, parseConfigFlag(args[0], args[1:]))
		case "ensure-gost-binary":
			configFile := parseConfigFlag(args[0], args[1:])
			return ensureGostBinary(ctx, filepath.Join(filepath.Dir(configFile), "gost"))
		case "update-runtime":
			configFile := parseConfigFlag(args[0], args[1:])
			config, err := readConfig(configFile)
			if err != nil {
				return err
			}
			return nodebridge.UpdateSystemdRuntime(ctx, config, configFile)
		case "nft-template":
			return printNftTemplate(parseConfigFlag(args[0], args[1:]))
		case "upgrade-component":
			return upgradeComponent(ctx, args[1:])
		case "--upgrade-installed":
			return upgradeInstalled(ctx, args[1:])
		}
	}
	configFile := parseConfigFlag("yagp-daemon", args)
	config, err := readConfig(configFile)
	if err != nil {
		return err
	}
	config.NftConfigFile = filepath.Join(filepath.Dir(configFile), "nft.json")
	config.ConfigFile = configFile
	return nodebridge.Run(ctx, config)
}

func printNftTemplate(configFile string) error {
	config, err := readConfig(configFile)
	if err != nil {
		return err
	}
	template, err := nodebridge.ReadNodeNftTemplate(configFile, config.NodeID)
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
	var stored struct {
		nodebridge.Config `yaml:",inline"`
		// Older installations still have these keys. Keep their files usable while discarding the listener settings.
		ControlListen string `yaml:"control_listen"`
		TLSCertFile   string `yaml:"tls_cert_file"`
		TLSKeyFile    string `yaml:"tls_key_file"`
	}
	file, err := os.Open(configFile)
	if err != nil {
		return stored.Config, err
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	err = decoder.Decode(&stored)
	stored.RuntimeLayout = nodebridge.ReadRuntimeLayout(configFile)
	return stored.Config, err
}
