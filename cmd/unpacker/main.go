// Command unpacker is the stub that gets an archive attached to it by the
// packer. Run without an attached archive it just explains what it is.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"

	"ouroboros/internal/app"
	"ouroboros/internal/archive"
	"ouroboros/internal/payload"
	"ouroboros/internal/web"
)

func main() {
	var (
		cli       = flag.Bool("cli", false, "run without the browser interface")
		list      = flag.Bool("list", false, "only list what the archive contains")
		dest      = flag.String("dest", "", "folder to extract to (command line mode)")
		conflict  = flag.String("conflict", "rename", "when a file exists: rename, skip or overwrite")
		keyfile   = flag.String("keyfile", "", "keyfile, if the archive was made with one")
		noModes   = flag.Bool("no-modes", false, "do not restore dates and permissions")
		noBrowser = flag.Bool("no-browser", false, "do not open a browser window automatically")
	)
	flag.Parse()

	if _, err := app.NeedsKeyfile(); err != nil {
		if err == payload.ErrNone || err == archive.ErrNotArchive {
			app.Fatal(fmt.Errorf("this program has no archive attached, create one with the ouroboros packer"))
		}
		app.Fatal(err)
	}

	var err error
	if *cli || *list || *dest != "" {
		err = runCLI(*list, *dest, *conflict, *keyfile, !*noModes)
	} else {
		err = runWeb(*noBrowser)
	}
	if err != nil {
		app.Fatal(err)
	}
}

func runCLI(list bool, dest, conflict, keyfile string, keepModes bool) error {
	app.HandleInterrupt()
	pass, err := app.Passphrase(false)
	if err != nil {
		return err
	}
	if list {
		items, err := app.List(pass, keyfile)
		if err != nil {
			return err
		}
		for _, i := range items {
			suffix := ""
			if i.IsDir {
				suffix = "/"
			}
			fmt.Printf("%12d  %s%s\n", i.Size, i.Path, suffix)
		}
		return nil
	}
	if dest == "" {
		dest = app.DefaultDest()
	}
	msg, warns, err := app.Extract(app.ExtractRequest{
		Passphrase: pass, Keyfile: keyfile, Dest: dest, Conflict: conflict, KeepModes: keepModes,
	}, func(p archive.Progress) {
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

func runWeb(noBrowser bool) error {
	os.Unsetenv("OUROBOROS_PASSPHRASE")
	srv, err := web.New("unpacker.html")
	if err != nil {
		return err
	}
	need, _ := app.NeedsKeyfile()
	home, _ := os.UserHomeDir()
	srv.API("/api/info", func(*http.Request) (any, error) {
		return map[string]any{"needsKeyfile": need, "dest": app.DefaultDest(), "home": home}, nil
	})
	srv.API("/api/list", func(r *http.Request) (any, error) {
		var req app.ExtractRequest
		if err := web.Decode(r, &req); err != nil {
			return nil, err
		}
		return app.List(req.Passphrase, req.Keyfile)
	})
	srv.API("/api/extract", func(r *http.Request) (any, error) {
		var req app.ExtractRequest
		if err := web.Decode(r, &req); err != nil {
			return nil, err
		}
		err := srv.StartJob(func(update func(archive.Progress)) (string, []string, error) {
			return app.Extract(req, update)
		})
		return map[string]bool{"started": err == nil}, err
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return srv.Run(ctx, noBrowser)
}
