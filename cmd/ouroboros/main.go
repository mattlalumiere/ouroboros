// Command ouroboros is the packer. It turns files and folders into one
// self extracting, passphrase protected executable.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"

	"ouroboros/internal/app"
	"ouroboros/internal/archive"
	"ouroboros/internal/stubs"
	"ouroboros/internal/web"
)

func main() {
	var (
		cli       = flag.Bool("cli", false, "run without the browser interface")
		out       = flag.String("o", "", "output file (command line mode)")
		target    = flag.String("target", "", "platform to build for, like windows/amd64 (default: this machine)")
		level     = flag.String("level", "default", "compression: fast, default, better or best")
		keyfile   = flag.String("keyfile", "", "optional keyfile that is needed along with the passphrase")
		del       = flag.Bool("delete", false, "delete the originals after the result has been verified")
		noBrowser = flag.Bool("no-browser", false, "do not open a browser window automatically")
		list      = flag.Bool("targets", false, "list the platforms this build can produce and exit")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags] [file or folder ...]\n\nWithout paths the browser interface opens.\nIn command line mode the passphrase comes from OUROBOROS_PASSPHRASE or a prompt.\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if *list {
		for _, t := range stubs.Available() {
			fmt.Println(t)
		}
		return
	}
	if *cli || flag.NArg() > 0 {
		if err := runCLI(flag.Args(), *out, *target, *level, *keyfile, *del); err != nil {
			app.Fatal(err)
		}
		return
	}
	if err := runWeb(*noBrowser); err != nil {
		app.Fatal(err)
	}
}

func runCLI(paths []string, out, target, level, keyfile string, del bool) error {
	if len(paths) == 0 {
		return fmt.Errorf("give at least one file or folder to pack")
	}
	if out == "" {
		return fmt.Errorf("give the output file with -o")
	}
	req := app.PackRequest{Paths: paths, Keyfile: keyfile, Level: level, DeleteOriginals: del}
	if target != "" {
		os, arch, ok := strings.Cut(target, "/")
		if !ok {
			return fmt.Errorf("target must look like linux/amd64")
		}
		req.OS, req.Arch = os, arch
	}
	dir, name := splitPath(out)
	req.OutDir, req.OutName = dir, name

	pass, err := app.Passphrase(true)
	if err != nil {
		return err
	}
	req.Passphrase = pass

	msg, warns, err := app.Pack(req, func(p archive.Progress) {
		if p.Total > 0 {
			fmt.Fprintf(os.Stderr, "\r%3d%%", p.Bytes*100/p.Total)
		}
	})
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return err
	}
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	fmt.Println(msg)
	return nil
}

func splitPath(p string) (dir, name string) {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return ".", p
	}
	if i == 0 {
		return p[:1], p[1:]
	}
	return p[:i], p[i+1:]
}

func runWeb(noBrowser bool) error {
	srv, err := web.New("packer.html")
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	srv.API("/api/info", func(*http.Request) (any, error) {
		return map[string]any{
			"targets": stubs.Available(),
			"host":    stubs.Host(),
			"home":    home,
			"outDir":  home,
			"sep":     string(os.PathSeparator),
			"names":   map[string]string{"windows": "Windows", "linux": "Linux", "darwin": "macOS"},
		}, nil
	})
	srv.API("/api/pack", func(r *http.Request) (any, error) {
		var req app.PackRequest
		if err := web.Decode(r, &req); err != nil {
			return nil, err
		}
		if len(req.Paths) == 0 {
			return nil, fmt.Errorf("nothing selected to pack")
		}
		err := srv.StartJob(func(update func(archive.Progress)) (string, []string, error) {
			return app.Pack(req, update)
		})
		return map[string]bool{"started": err == nil}, err
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return srv.Run(ctx, noBrowser)
}
