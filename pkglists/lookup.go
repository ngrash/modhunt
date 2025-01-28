package pkglists

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
)

type Link struct {
	URL         string
	Description string
	Category    *Category
	Source      *Source
}

type Category struct {
	Level      int
	Name       string
	Categories []*Category
	Parent     *Category
	Links      []Link
}

type Source struct {
	Name string
	URL  string

	Root *Category
}

type Lookup struct {
	Sources  []*Source
	Packages map[string][]Link
}

func NewLookup() Lookup {
	return Lookup{
		Packages: make(map[string][]Link),
	}
}

func Key(pkgURL string) (string, error) {
	u, err := url.Parse(pkgURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	u.Scheme = ""
	key := u.String()[2:] // remove leading "//"
	return key, nil
}

func (l *Lookup) AddSource(s *Source) error {
	l.Sources = append(l.Sources, s)

	return l.addCategory(s.Root, true)
}

func (l *Lookup) addCategory(c *Category, root bool) error {
	if err := checkCategory(c, root); err != nil {
		return fmt.Errorf("check category %+v: %w", c, err)
	}

	for _, link := range c.Links {
		if err := checkLink(link); err != nil {
			return fmt.Errorf("check link %+v: %w", link, err)
		}
		key, err := Key(link.URL)
		if err != nil {
			return fmt.Errorf("lookup key: %w", err)
		}
		l.Packages[key] = append(l.Packages[key], link)
	}
	for _, c := range c.Categories {
		if err := l.addCategory(c, false); err != nil {
			return err
		}
	}
	return nil
}

func checkCategory(c *Category, root bool) error {
	if c.Name == "" {
		return fmt.Errorf("category has no name")
	}
	if c.Level < 0 {
		return fmt.Errorf("category %s has negative level", c.Name)
	}
	if !root {
		if c.Parent == nil {
			return fmt.Errorf("non-root category %s has no parent", c.Name)
		}
		var knownByParent bool
		if c.Parent != nil {
			for _, sib := range c.Parent.Categories {
				if sib == c {
					knownByParent = true
					break
				}
			}
		}
		if !knownByParent {
			return fmt.Errorf("category %s not found in parent", c.Name)
		}
	}
	return nil
}

func checkLink(l Link) error {
	if l.URL == "" {
		return fmt.Errorf("link has no URL")
	}
	if l.Description == "" {
		return fmt.Errorf("link %s has no description", l.URL)
	}
	if l.Category == nil {
		return fmt.Errorf("link %s has no category", l.URL)
	}
	if l.Source == nil {
		return fmt.Errorf("link %s has no source", l.URL)
	}
	return nil
}

func NewTestdataLookup() (*Lookup, error) {
	l := NewLookup()

	wikiData, err := os.ReadFile("testdata/go-wiki-Projects.md")
	if err != nil {
		return nil, fmt.Errorf("read wiki: %w", err)
	}
	wikiSource, err := ParseGoWikiProjects(bytes.NewReader(wikiData))
	if err != nil {
		return nil, fmt.Errorf("parse wiki: %w", err)
	}
	if err := l.AddSource(wikiSource); err != nil {
		return nil, fmt.Errorf("add wiki source: %w", err)
	}

	awesomeData, err := os.ReadFile("testdata/awesome-go-README.md")
	if err != nil {
		return nil, fmt.Errorf("read awesome: %w", err)
	}
	awesomeSource, err := ParseAwesomeGoReadme(bytes.NewReader(awesomeData))
	if err != nil {
		return nil, fmt.Errorf("parse awesome: %w", err)
	}
	if err := l.AddSource(awesomeSource); err != nil {
		return nil, fmt.Errorf("add awesome source: %w", err)
	}

	return &l, nil
}
