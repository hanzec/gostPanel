package main

import (
	"context"
	"errors"
	"flag"

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
