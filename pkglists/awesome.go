package pkglists

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/ngrash/modhunt/lookup"
)

func ParseAwesomeGoReadme(r io.Reader) (*lookup.Source, error) {
	type state string

	const (
		stAwaitHeader         state = "awaitHeader"
		stAwaitTableOfContent state = "awaitTableOfContent"
		stSkipTableOfContent  state = "skipTableOfContent"
		stReadCategoryTitle   state = "readCategoryTitle"
		stReadCategoryBody    state = "readCategoryBody"
		stReadLinkList        state = "readLinkList"
	)

	s := bufio.NewScanner(r)

	source := &lookup.Source{
		Name: "Awesome Go",
		URL:  "https://awesome-go.com/",
		Root: &lookup.Category{Level: 0, Name: "root"},
	}

	cat := source.Root
	st := stAwaitHeader

	baseLogger := slog.Default()

	var prevWasEmpty bool
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if len(line) == 0 {
			prevWasEmpty = true
			continue // skip empty lines
		}
		if strings.HasPrefix(line, "_") {
			continue // skip italic lines
		}

		for {
			log := baseLogger.With("line", line).With("state", st)

			switch st {
			case stAwaitHeader:
				if line != "# Awesome Go" {
					log.Warn("Ignoring unexpected header.")
				}
				st = stAwaitTableOfContent
			case stAwaitTableOfContent:
				if line == "## Contents" {
					st = stSkipTableOfContent
				}
			case stSkipTableOfContent:
				if strings.HasPrefix(line, "##") {
					st = stReadCategoryTitle
					continue // reprocess line
				}
			case stReadCategoryTitle:
				if line == "# Resources" {
					goto done
				}
				if !strings.HasPrefix(line, "#") {
					return nil, fmt.Errorf("expected category title, got: %s", line)
				}

				title := strings.TrimSpace(strings.TrimLeft(line, "#"))
				level := strings.Count(line, "#")
				if level <= cat.Level {
					for cat = cat.Parent; cat.Level >= level; cat = cat.Parent {
					}
				}

				parent := cat
				cat = &lookup.Category{Level: level, Name: title, Parent: parent}
				cat.Parent.Categories = append(cat.Parent.Categories, cat)
				st = stReadCategoryBody
			case stReadCategoryBody:
				if strings.HasPrefix(line, "-") {
					st = stReadLinkList
					continue // reprocess line
				}
				if strings.HasPrefix(line, "#") {
					st = stReadCategoryTitle
					continue // reprocess line
				}
				break // ignore all other lines in category body.
			case stReadLinkList:
				if strings.HasPrefix(line, "##") {
					st = stReadCategoryTitle
					continue // reprocess line
				}
				if line == "**[⬆ back to top](#contents)**" {
					st = stReadCategoryTitle
					break // next line
				}

				if strings.HasPrefix(line, "-") {
					// Split into "- [name" and "](url) - description"
					parts := strings.SplitN(line, "](", 2)
					if len(parts) != 2 {
						return nil, fmt.Errorf("link without '](': %s", line)
					}
					parts = strings.SplitN(parts[1], ")", 2)
					if len(parts) != 2 {
						return nil, fmt.Errorf("link without ')': %s", line)
					}
					url := parts[0]
					desc := strings.TrimLeft(parts[1], " -")
					cat.Links = append(cat.Links, lookup.Link{
						URL:         url,
						Description: desc,
						Category:    cat,
						Source:      source,
					})
					break // next line
				}

				// Append to last link description if not separated by empty line.
				if len(cat.Links) > 0 && !prevWasEmpty {
					last := &cat.Links[len(cat.Links)-1]
					last.Description += line
					break // next line
				}

				log.Warn("Ignoring unexpected line.")
			default:
				return nil, fmt.Errorf("BUG: unexpected state: %d", st)
			}

			prevWasEmpty = false
			break // next line
		}
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

done:

	return source, nil
}
