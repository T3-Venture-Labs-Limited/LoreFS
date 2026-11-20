package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/T3-Venture-Labs-Limited/LoreFS/internal/server"
)

const serverHelp = `lorefs server init --address <IP or name> [folder]

Writes a folder with everything needed to run the drive's index (Valkey) on
a computer that has Docker: a compose file, a configuration that accepts
encrypted connections only, a password, and a certificate for the address.

  --address   the IP address or name other computers will use (required)
  --port      the port to publish (default 6379)
  --memory-mb how much memory the index may use before it refuses new files (default 400)
  --image     the index server image (default ` + server.DefaultImage + `)
  --name      the drive's name, used to label the certificate authority
`

func cmdServer(args []string) error {
	if len(args) == 0 || args[0] != "init" {
		return errUsage("use: lorefs server init --address <IP or name> [folder]\n\n" + serverHelp)
	}
	fs := flags("server init")
	address := fs.String("address", "", "the IP address or name other computers will use to reach the index")
	port := fs.Int("port", 6379, "the port to publish")
	mem := fs.Int("memory-mb", 400, "memory limit in MB")
	image := fs.String("image", server.DefaultImage, "the index server image")
	name := fs.String("name", "lorefs", "the drive's name")
	fs.Usage = func() { fmt.Fprint(os.Stderr, serverHelp) }
	plain, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	dir := "lorefs-index"
	switch len(plain) {
	case 0:
	case 1:
		dir = plain[0]
	default:
		return errUsage("give at most one folder")
	}
	if err := server.Init(server.InitOptions{Address: *address, Dir: dir, Port: *port, MemoryMB: *mem,
		Image: *image, Name: *name, UID: os.Getuid(), GID: os.Getgid()}); err != nil {
		return err
	}
	abs, _ := filepath.Abs(dir)
	fmt.Printf("The index bundle is in %s\n\n", abs)
	fmt.Println("1. Copy the folder to the computer that will run the index, if it is not this one.")
	fmt.Println("2. There, start it:           cd " + dir + " && docker compose up -d")
	fmt.Printf("3. Let port %d through the firewall to the computers that will use the drive, and nobody else.\n", *port)
	fmt.Println("4. Create the drive on it:    lorefs create --server-dir " + dir + " --bucket <address of your bucket> --access-key <key>")
	fmt.Println()
	fmt.Println("Keep the secrets/ folder safe and private. The README in the folder explains the rest.")
	return nil
}
