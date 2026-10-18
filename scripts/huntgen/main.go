// Command huntgen writes the generated test inputs of the 1.1 bug hunt. Everything is a function of a
// seed, so the same command gives the same files. Output goes to .hunt/ (gitignored) except `seeds`.
//
//	go run ./scripts/huntgen restore [-out .hunt/restore] [-n 40]   mutated SQLite files for `kipple restore`
//	go run ./scripts/huntgen docs    [-out .hunt/docs]    [-n 40]   generated OPML, Atom and RSS documents
//	go run ./scripts/huntgen seeds   [-out internal/hunt/testdata/restore]   the small committed restore seeds
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/WPTK/kipple/internal/hunt"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: huntgen restore|docs|seeds [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "restore":
		err = runRestore(os.Args[2:])
	case "docs":
		err = runDocs(os.Args[2:])
	case "seeds":
		err = runSeeds(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "huntgen:", err)
		os.Exit(1)
	}
}

func runRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	out := fs.String("out", filepath.Join(".hunt", "restore"), "output directory (one subdirectory per case)")
	n := fs.Int("n", 40, "seeded random cases to add to the fixed ones")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	cases, err := hunt.RestoreCases(ctx)
	if err != nil {
		return err
	}
	for i := 0; i < *n; i++ {
		c, err := hunt.RestoreFromSeed(ctx, uint64(i)+1, hunt.RestoreKnobs{Version: uint8(i * 5), PageSize: uint8(i), Mutation: uint8(i), Flips: uint8(i * 3)})
		if err != nil {
			return err
		}
		cases = append(cases, c)
	}
	for _, c := range cases {
		dir := filepath.Join(*out, c.Name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		if _, err := c.WriteTo(dir); err != nil {
			return err
		}
	}
	fmt.Printf("%d restore cases in %s\n", len(cases), *out)
	return nil
}

func runDocs(args []string) error {
	fs := flag.NewFlagSet("docs", flag.ExitOnError)
	out := fs.String("out", filepath.Join(".hunt", "docs"), "output directory")
	n := fs.Int("n", 40, "documents of each kind")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	gens := []struct {
		name, ext string
		f         func(uint64, hunt.DocKnobs) []byte
	}{{"opml", "opml", hunt.OPMLFromSeed}, {"atom", "atom.xml", hunt.AtomFromSeed}, {"rss", "rss.xml", hunt.RSSFromSeed}}
	for _, g := range gens {
		for i := 0; i < *n; i++ {
			k := hunt.DocKnobs{Size: uint8(8 + i*5), Depth: uint8(i), Weird: uint8(i * 37), Charset: uint8(i / 3)}
			if err := os.WriteFile(filepath.Join(*out, fmt.Sprintf("%s-%03d.%s", g.name, i, g.ext)), g.f(uint64(i)+1, k), 0o600); err != nil {
				return err
			}
		}
	}
	fmt.Printf("%d documents in %s\n", 3**n, *out)
	return nil
}

func runSeeds(args []string) error {
	fs := flag.NewFlagSet("seeds", flag.ExitOnError)
	out := fs.String("out", filepath.Join("internal", "hunt", "testdata", "restore"), "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	seeds, err := hunt.TinySeeds(context.Background())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	for name, b := range seeds {
		if err := os.WriteFile(filepath.Join(*out, name+".seed"), b, 0o600); err != nil {
			return err
		}
	}
	fmt.Printf("%d seeds in %s\n", len(seeds), *out)
	return nil
}
