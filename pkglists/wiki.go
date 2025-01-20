package pkglists

import (
	"io"
	"slices"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"github.com/ngrash/modhunt/lookup"
)

func ParseGoWikiProjects(r io.Reader) (*lookup.Source, error) {
	source := &lookup.Source{
		Name: "Go Wiki",
		URL:  "https://go.dev/wiki/Projects",
		Root: &lookup.Category{
			Name: "root",
		},
	}

	skipHeadings := []string{"title: Projects", "Indexes and search engines", "Dead projects", "Table of Contents"}

	cat := source.Root

	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	p := goldmark.DefaultParser()
	doc := p.Parse(text.NewReader(data))
	for child := doc.FirstChild(); child != nil; child = child.NextSibling() {
		heading, ok := child.(*ast.Heading)
		if !ok {
			continue
		}
		title := string(heading.Lines().Value(data))
		if slices.Contains(skipHeadings, title) {
			continue
		}
		level := heading.Level
		if level <= cat.Level {
			for cat = cat.Parent; cat.Level >= level; cat = cat.Parent {
			}
		}

		parent := cat
		cat = &lookup.Category{
			Parent: parent,
			Level:  level,
			Name:   title,
		}
		parent.Categories = append(parent.Categories, cat)

		for c := heading.NextSibling(); c != nil; c = c.NextSibling() {
			switch list := c.(type) {
			case *ast.Heading:
				goto nextHeading
			case *ast.List:
				for li := list.FirstChild(); li != nil; li = li.NextSibling() {
					item := li.(*ast.ListItem)
					for i := item.FirstChild(); i != nil; i = i.NextSibling() {
						tb, ok := i.(*ast.TextBlock)
						if !ok {
							continue
						}

						var url string
						for j := tb.FirstChild(); j != nil; j = j.NextSibling() {
							if link, ok := j.(*ast.Link); ok {
								url = string(link.Destination)
								break
							}
						}
						if url == "" {
							continue
						}

						tbLines := string(tb.Lines().Value(data))
						urlIdx := strings.Index(tbLines, url)
						desc := tbLines[urlIdx+len(url)+1:]
						desc = strings.TrimLeft(desc, " -")

						cat.Links = append(cat.Links, lookup.Link{
							URL:         url,
							Description: desc,
							Category:    cat,
							Source:      source,
						})
					}
				}
			}
		}

	nextHeading:
	}

	return source, nil
}
