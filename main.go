package main

import (
	"bytes"
	"flag"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/ngrash/modhunt/lookup"
	"github.com/ngrash/modhunt/pkglists"
)

func main() {
	if err := run(os.Args); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("global flags", flag.ContinueOnError)
	fs.Args()
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	lookup, err := initLookup()
	if err != nil {
		return fmt.Errorf("init lookup: %w", err)
	}

	switch fs.Arg(0) {
	case "categories":
		return categoriesCommand(fs.Args()[1:], lookup)
	case "common":
		return commonCommand(fs.Args()[1:], lookup)
	case "alternatives":
		return alternativesCommand(fs.Args()[1:], lookup)
	case "github":
		return githubCommand(fs.Args()[1:], lookup)
	case "go-proxy":
		return goProxyCommand(fs.Args()[1:], lookup)
	case "strange":
		return strangeCommand(lookup)
	case "multi-url":
		return multiURLCommand(lookup)
	case "download-info":
		return downloadInfoCommand(lookup)
	case "index":
		return indexCommand()
	case "normalize-index":
		return normalizeIndexCommand()
	case "lookup-mods":
		return lookupModulesCommand()
	case "search":
		query := strings.Join(fs.Args()[1:], " ")
		for name, links := range lookup.Packages {
			if strings.Contains(name, query) {
				fmt.Println(name)
				continue
			}
			for _, link := range links {
				if strings.Contains(link.Description, query) {
					fmt.Println(name, link.Description)
					continue
				}
			}
		}
	case "domains":
		domains := make(map[string]int)
		for _, links := range lookup.Packages {
			for _, link := range links {
				u, err := url.Parse(link.URL)
				if err != nil {
					return fmt.Errorf("parse URL: %w", err)
				}
				domains[u.Host]++
			}
		}
		keys := slices.SortedFunc(maps.Keys(domains), func(i, j string) int {
			return domains[i] - domains[j]
		})
		for _, key := range keys {
			percentage := float64(domains[key]) / float64(len(lookup.Packages)) * 100
			fmt.Printf("%s: %d (%.2f%%)\n", key, domains[key], percentage)
		}
	case "suggest":
		// Find an approved package that is similar to the given package.
		// We can use GitHub topics to find similar packages.
	}

	return nil
}

func initLookup() (*lookup.Lookup, error) {
	lookup := lookup.NewLookup()

	wikiData, err := os.ReadFile("testdata/go-wiki-Projects.md")
	if err != nil {
		return nil, fmt.Errorf("read wiki: %w", err)
	}
	wikiSource, err := pkglists.ParseGoWikiProjects(bytes.NewReader(wikiData))
	if err != nil {
		return nil, fmt.Errorf("parse wiki: %w", err)
	}
	if err := lookup.AddSource(wikiSource); err != nil {
		return nil, fmt.Errorf("add wiki source: %w", err)
	}

	awesomeData, err := os.ReadFile("testdata/awesome-go-README.md")
	if err != nil {
		return nil, fmt.Errorf("read awesome: %w", err)
	}
	awesomeSource, err := pkglists.ParseAwesomeGoReadme(bytes.NewReader(awesomeData))
	if err != nil {
		return nil, fmt.Errorf("parse awesome: %w", err)
	}
	if err := lookup.AddSource(awesomeSource); err != nil {
		return nil, fmt.Errorf("add awesome source: %w", err)
	}

	return &lookup, nil
}

func printCategory(cat *lookup.Category) {
	var ident string
	if cat.Level > 0 {
		ident = strings.Repeat("  ", cat.Level-1) + "└─"
	}

	fmt.Printf("%s %s (%d)\n", ident, cat.Name, len(cat.Links))

	for _, l := range cat.Links {
		id := strings.Repeat("  ", cat.Level) + "└─"
		fmt.Printf("%s %s - %s\n", id, l.URL, l.Description)
	}
	for _, c := range cat.Categories {
		printCategory(c)
	}
}
