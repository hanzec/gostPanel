package main

import (
	"context"
	"errors"
	"flag"
	"os"

	"yagp/internal/nodebridge"
)

func upgradeComponent(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("upgrade-component", flag.ExitOnError)
	configFile := flags.String("c", "node.yaml", "node bridge configuration file")
	component := flags.String("component", "", "component to upgrade (gost or daemon)")
	tag := flags.String("tag", "", "exact release tag")
	_ = flags.Parse(args)
	if *component == "" || *tag == "" || flags.NArg() != 0 {
		return errors.New("upgrade-component requires --component and --tag")
	}
	config, err := readConfig(*configFile)
	if err != nil {
		return err
	}
	return nodebridge.RunUpgrade(ctx, config, *configFile, *component, *tag)
}

func upgradeInstalled(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("--upgrade-installed", flag.ExitOnError)
	configFile := flags.String("c", "node.yaml", "installed node configuration file")
	nodeID := flags.Uint64("node-id", 0, "installed node ID")
	_ = flags.Parse(args)
	if *nodeID == 0 || flags.NArg() != 0 {
		return errors.New("--upgrade-installed requires --node-id")
	}
	config, err := readConfig(*configFile)
	if err != nil {
		return err
	}
	if config.NodeID != *nodeID {
		return errors.New("node ID does not match the installed configuration")
	}
	// The installer can remove the staged file if interrupted while systemd runs the worker.
	binary, err := os.Open("/proc/self/exe")
	if err != nil {
		return err
	}
	defer binary.Close()
	return nodebridge.UpgradeInstalledDaemon(ctx, config, *configFile, binary)
}
