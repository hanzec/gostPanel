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
	configFile := flag.String("c", "node.yaml", "node bridge configuration file")
	flag.Parse()
	file, err := os.Open(*configFile)
	if err != nil {
		log.Fatal(err)
	}
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var config nodebridge.Config
	err = decoder.Decode(&config)
	file.Close()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err = nodebridge.Run(ctx, config); err != nil {
		log.Fatal(err)
	}
}
