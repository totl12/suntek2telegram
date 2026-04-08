package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"suntek2telegram/pkg/config"
	"suntek2telegram/pkg/database"
	"suntek2telegram/pkg/telegrambot"
	"suntek2telegram/pkg/trapmanager"
	"suntek2telegram/pkg/webserver"
)

var (
	version string

	flagConfigPath = flag.String("conf", "config.yml", "Path to config file")
	flagVersion    = flag.Bool("version", false, "prints version of the application")
)

func main() {
	flag.Parse()

	if *flagVersion {
		fmt.Println("Version:", version)
		return
	}

	c, err := config.New(*flagConfigPath)
	if err != nil {
		log.Fatalln("Failed to load config:", err)
	}

	if err := os.MkdirAll(c.PhotosDir, 0755); err != nil {
		log.Fatalln("Failed to create photos directory:", err)
	}
	if err := os.MkdirAll(filepath.Dir(c.DBPath), 0755); err != nil {
		log.Fatalln("Failed to create database directory:", err)
	}

	db, err := database.Open(c.DBPath)
	if err != nil {
		log.Fatalln("Failed to open database:", err)
	}
	defer db.Close()

	bot, err := telegrambot.NewBot(c.Telegram.APIKey)
	if err != nil {
		log.Fatalln("Failed to connect Telegram bot:", err)
	}

	mgr := trapmanager.New(db, bot, c.PhotosDir, c.FTP, c.SMTP)
	if err := mgr.Start(); err != nil {
		log.Fatalln("Failed to start trap manager:", err)
	}

	ws := webserver.New(db, mgr, c.PhotosDir, c.Web.BindHost, c.Web.BindPort, c.Web.Username, c.Web.Password)
	go ws.Start()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	<-interrupt

	log.Println("Shutting down...")
	mgr.Shutdown()
}
