package modhunter

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Result struct {
	Module    string
	Canonical string
	Strategy  string
	Latest    VersionInfo
}

type strategyFunc func(string) (string, bool)

type strategy struct {
	name string
	fn   strategyFunc
}

func Search(module string) (*Result, error) {
	strategies := []strategy{
		{"none", func(module string) (string, bool) { return module, true }},
		{"lowercase", func(module string) (string, bool) { return strings.ToLower(module), true }},
	}

	for _, strat := range strategies {
		mod := module
		if strat.name != "none" {
			// All strategies lowercase the module name except for the "none" strategy.
			mod = strings.ToLower(module)
		}
		canonical, ok := strat.fn(mod)
		if ok {
			vi, err := queryProxy(canonical)
			if err == nil {
				return &Result{
					Module:    module,
					Canonical: canonical,
					Strategy:  strat.name,
					Latest:    vi,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("no results for %q", module)
}

type VersionInfo struct {
	Version string    `json:"Version"`
	Time    time.Time `json:"Time"`
	Origin  struct {
		VCS  string `json:"VCS"`
		URL  string `json:"URL"`
		Ref  string `json:"Ref"`
		Hash string `json:"Hash"`
	} `json:"Origin"`
}

func queryProxy(module string) (vi VersionInfo, err error) {
	resp, err := http.Get(fmt.Sprintf("https://proxy.golang.org/%s/@latest", module))
	if err != nil {
		return vi, err
	}
	defer func() {
		closeErr := resp.Body.Close()
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return vi, fmt.Errorf("unexpected status: %s", resp.Status)
	}
	var info VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return vi, err
	}
	return info, nil
}
