package main

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	// Exit on Ctrl-C
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	defer func() {
		signal.Stop(signalChan)
		close(signalChan)
	}()

	go func() {
		// First signal: exit gracefully
		select {
		case <-signalChan:
			log.Print("Exiting...")
			cancel()
		case <-ctx.Done():
			return
		}

		// Second signal: force exit
		_, ok := <-signalChan
		if ok {
			os.Exit(1)
		}
	}()

	var configPath string

	var databasePath string
	var db *sql.DB

	var port int
	var socket string

	cmd := cobra.Command{
		Use: "sheepcount",
		Run: func(cmd *cobra.Command, args []string) {
			config := DefaultConfig()

			_, err := toml.DecodeFile(configPath, &config)
			if err != nil {
				log.Printf("%+v", err)
				return
			}

			db, err = dbConnect(databasePath)
			if err != nil {
				log.Print(err)
				return
			}

			sheepcount, err := NewSheepCount(db, config)
			if err != nil {
				log.Printf("%+v", err)
				return
			}

			var l net.Listener
			if socket != "" {
				// Delete the socket first
				err = os.Remove(socket)
				if err != nil && !os.IsNotExist(err) {
					log.Printf("%+v", err)
					return
				}

				l, err = net.Listen("unix", socket)
				if err != nil {
					log.Printf("%+v", err)
					return
				}

				// Restrict access to socket
				err = os.Chmod(socket, 0700)
				if err != nil {
					log.Printf("%+v", err)
					return
				}

				sheepcount.AllowLocalhost = false
				sheepcount.ReverseProxy = true
			} else {
				l, err = net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
				sheepcount.AllowLocalhost = true
			}
			if err != nil {
				log.Printf("%+v", err)
				return
			}

			if err := sheepcount.Run(ctx, l); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("%+v", err)
			}

		},
		PostRun: func(cmd *cobra.Command, args []string) {
			if db != nil {
				if _, err := db.Exec("PRAGMA optimize"); err != nil {
					log.Print(err)
				}

				if err := db.Close(); err != nil {
					log.Print(err)
				}
			}
		},
	}

	cmd.PersistentFlags().StringVar(&configPath, "config", "sheepcount.toml", "Path to configuration file")
	cmd.PersistentFlags().StringVar(&databasePath, "database", "sheepcount.sqlite3", "Path to database")
	cmd.PersistentFlags().IntVar(&port, "port", 4444, "Port to listen on")
	cmd.PersistentFlags().StringVar(&socket, "socket", "", "Socket to listen on")

	cmd.Execute()
}
