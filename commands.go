package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/google/go-github/v68/github"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
	_ "modernc.org/sqlite"

	"github.com/ngrash/modhunt/index"
	"github.com/ngrash/modhunt/lookup"
)

func categoriesCommand(_ []string, lookup *lookup.Lookup) error {
	for _, s := range lookup.Sources {
		printCategory(s.Root)
	}
	return nil
}

func commonCommand(_ []string, lookup *lookup.Lookup) error {
	for name, links := range lookup.Packages {
		if len(links) > 1 {
			fmt.Printf("%s (%d)\n", name, len(links))
			for _, l := range links {
				fmt.Printf("  %s > %s - %s\n", l.Source.Name, l.Category.Name, l.Description)
			}
		}
	}
	return nil
}

func lookupModulesCommand() error {
	db, err := sql.Open("sqlite", "file:index.db?_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	return lookupAllPaths(db, 5000)
}

func lookupAllPaths(db *sql.DB, batchSize int) error {
	row := db.QueryRow("SELECT COUNT(*) FROM paths")
	var total int
	err := row.Scan(&total)
	if err != nil {
		return fmt.Errorf("count paths: %w", err)
	}

	fmt.Println("looking up", total, "paths")

	var count int
	lastID := int64(0)
	for {
		percentage := float64(count) / float64(total) * 100
		fmt.Printf("lookup %.2f%% (%d/%d)\n", percentage, count, total)
		count += batchSize

		var err error
		lastID, err = lookupBatch(db, batchSize, lastID)
		if err != nil {
			return fmt.Errorf("process batch: %w", err)
		}
		if lastID == 0 {
			break // done
		}
	}

	fmt.Printf("looked up %d/%d\n", total, total)
	return nil
}

func lookupBatch(db *sql.DB, batchSize int, lastID int64) (int64, error) {
	type PathRow struct {
		ID            int64
		Path          string
		LatestVersion string // calculated later
	}

	// Fetch the next batch.
	rows, err := db.Query(`SELECT id, path
            FROM paths
            WHERE id > ?
            ORDER BY id
            LIMIT ?`,
		lastID, batchSize,
	)
	if err != nil {
		return 0, fmt.Errorf("query failed: %w", err)
	}

	var batch []PathRow
	for rows.Next() {
		var r PathRow
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan failed: %w", err)
		}

		versionRows, err := db.Query(
			`SELECT version FROM versions WHERE path_id = ?`,
			r.ID,
		)
		if err != nil {
			return 0, fmt.Errorf("query versions: %w", err)
		}
		var versions []string
		for versionRows.Next() {
			var version string
			if err := versionRows.Scan(&version); err != nil {
				_ = versionRows.Close()
				return 0, fmt.Errorf("scan version: %w", err)
			}
			versions = append(versions, version)
		}
		_ = versionRows.Close()

		sort.Slice(versions, func(i, j int) bool {
			return goVersionLess(versions[i], versions[j])
		})
		if len(versions) > 0 {
			r.LatestVersion = versions[len(versions)-1]
		}
		// TODO: Versions are not correctly sorted.

		fmt.Println(r.Path, r.LatestVersion)

		batch = append(batch, r)
	}
	_ = rows.Close()

	// No more rows -> we are done
	if len(batch) == 0 {
		return 0, nil
	}

	// Advance lastID to the highest ID we’ve processed in this batch.
	lastID = batch[len(batch)-1].ID

	return lastID, nil

	for _, pathRow := range batch {
		version, module, err := lookupModule(pathRow.Path, pathRow.LatestVersion)
		if err != nil {
			return 0, fmt.Errorf("lookup module %q: %w", pathRow.Path, err)
		}
		fmt.Println(pathRow.Path, version, "=>", module)
	}

	return lastID, nil
}

func goVersionLess(a, b string) bool {
	// Classify each version: stable, prerelease, or pseudo
	aType := classifyVersion(a)
	bType := classifyVersion(b)

	// If type differs, stable < prerelease < pseudo in ascending order,
	// but we want stable > prerelease > pseudo for "latest",
	// so flip the comparison to put stable last in sort order:
	if aType != bType {
		return aType < bType
	}

	switch aType {
	case vtStable, vtPrerelease:
		// Use semver.Compare directly
		return semver.Compare(a, b) < 0

	case vtPseudo:
		// Compare base, then time, then commit
		less, err := pseudoLess(a, b)
		return err == nil && less
	}
	return false
}

const (
	vtStable = iota
	vtPrerelease
	vtPseudo
	vtInvalid
)

func classifyVersion(v string) int {
	if !semver.IsValid(v) {
		return vtInvalid
	}
	if module.IsPseudoVersion(v) {
		return vtPseudo
	}
	// If prerelease is non-empty, it's vtPrerelease
	if prerelease := semver.Prerelease(v); prerelease != "" {
		return vtPrerelease
	}
	// Otherwise it's a stable release
	return vtStable
}

// pseudoLess compares two pseudo-versions by the rules:
//
//	base version ascending, then timestamp ascending, then revision ascending
//
// But since we want a < b for ascending, it keeps that logic.
func pseudoLess(a, b string) (bool, error) {
	baseA, err := module.PseudoVersionBase(a)
	if err != nil {
		return false, err
	}
	baseB, err := module.PseudoVersionBase(b)
	if err != nil {
		return false, err
	}
	if c := semver.Compare(baseA, baseB); c != 0 {
		return c < 0, nil
	}
	timeA, err := module.PseudoVersionTime(a)
	if err != nil {
		return false, err
	}
	timeB, err := module.PseudoVersionTime(b)
	if err != nil {
		return false, err
	}
	if timeA != timeB {
		return timeA.Before(timeB), nil
	}
	revA, err := module.PseudoVersionRev(a)
	if err != nil {
		return false, err
	}
	revB, err := module.PseudoVersionRev(b)
	if err != nil {
		return false, err
	}
	return strings.Compare(revA, revB) < 0, nil
}

func lookupModule(path, version string) (string, string, error) {
	path = strings.ToLower(path)

	resp, err := http.Get("https://proxy.golang.org/" + path + "/@v/" + version + ".mod")
	if err != nil {
		return "", "", fmt.Errorf("get failed: %w", err)
	}
	defer resp.Body.Close()

	var module string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "module ") {
			module = strings.TrimPrefix(line, "module ")
			break
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "module ") {
			module = strings.TrimPrefix(trimmed, "module ")
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", "", err
	}
	if module == "" {
		return "", "", fmt.Errorf("module not found: %s@%s", path, version)
	}

	return version, module, nil
}

func normalizeIndexCommand() error {
	db, err := sql.Open("sqlite", "file:index.db?_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE IF NOT EXISTS modules (id INTEGER PRIMARY KEY ASC, module TEXT NOT NULL UNIQUE);")
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	// Check if column module_id exists in paths table.
	row := db.QueryRow("SELECT COUNT(cid) FROM pragma_table_info('paths') WHERE name = 'module_id';")
	var count int
	err = row.Scan(&count)
	if err != nil {
		return fmt.Errorf("check column: %w", err)
	}
	if count == 0 {
		_, err := db.Exec("ALTER TABLE paths ADD COLUMN module_id INTEGER REFERENCES modules(id);")
		if err != nil {
			return fmt.Errorf("add column: %w", err)
		}
	}
	_, err = db.Exec("CREATE INDEX IF NOT EXISTS idx_paths_module_id ON paths(module_id);")
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}

	err = processAllRecords(db, 5000)
	if err != nil {
		return fmt.Errorf("process all records: %w", err)
	}
	fmt.Println("all normalized")
	return nil
}

func processAllRecords(db *sql.DB, batchSize int) error {
	row := db.QueryRow("SELECT COUNT(*) FROM paths")
	var total int
	err := row.Scan(&total)
	if err != nil {
		return fmt.Errorf("count paths: %w", err)
	}

	fmt.Println("cleaning up", total, "paths")

	var count int
	lastID := int64(0)
	for {
		percentage := float64(count) / float64(total) * 100
		fmt.Printf("normalizing %.2f%% (%d/%d)\n", percentage, count, total)
		count += batchSize

		var err error
		lastID, err = processBatch(db, batchSize, lastID)
		if err != nil {
			return fmt.Errorf("process batch: %w", err)
		}
		if lastID == 0 {
			break // done
		}
	}

	fmt.Printf("normalized %d/%d\n", total, total)

	// Remove unreferenced modules.
	fmt.Println("cleaning up modules")
	deleted, err := db.Exec("DELETE FROM modules WHERE id NOT IN (SELECT module_id FROM paths WHERE module_id IS NOT NULL);")
	if err != nil {
		return fmt.Errorf("delete unreferenced modules: %w", err)
	}
	affected, err := deleted.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	fmt.Printf("deleted %d unreferenced modules\n", affected)

	return nil
}

func processBatch(db *sql.DB, batchSize int, lastID int64) (int64, error) {
	type PathRow struct {
		ID   int64
		Path string
	}

	// Fetch the next batch.
	rows, err := db.Query(`
            SELECT id, path
            FROM paths
            WHERE id > ?
            ORDER BY id
            LIMIT ?`,
		lastID, batchSize,
	)
	if err != nil {
		return 0, fmt.Errorf("query failed: %w", err)
	}

	var batch []PathRow
	for rows.Next() {
		var r PathRow
		if err := rows.Scan(&r.ID, &r.Path); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan failed: %w", err)
		}
		batch = append(batch, r)
	}
	_ = rows.Close()

	// No more rows -> we are done
	if len(batch) == 0 {
		return 0, nil
	}

	// Process and update each row.
	tx, err := db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx failed: %w", err)
	}

	stmt, err := tx.Prepare(`
            UPDATE paths
            SET module_id = ?
            WHERE id = ?
        `)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("prepare update failed: %w", err)
	}
	defer stmt.Close()

	for _, pathRow := range batch {
		var moduleID int64
		moduleName := normalizeModuleName(pathRow.Path)
		modRow := tx.QueryRow("SELECT id FROM modules WHERE module = ?", moduleName)
		err = modRow.Scan(&moduleID)
		if errors.Is(err, sql.ErrNoRows) {
			// Insert a new module.
			res, err := tx.Exec("INSERT INTO modules (module) VALUES (?)", moduleName)
			if err != nil {
				_ = tx.Rollback()
				return 0, fmt.Errorf("insert module failed: %w", err)
			}
			moduleID, err = res.LastInsertId()
			if err != nil {
				_ = tx.Rollback()
				return 0, fmt.Errorf("last insert id failed: %w", err)
			}
		}

		if _, err := stmt.Exec(moduleID, pathRow.ID); err != nil {
			_ = tx.Rollback()
			return 0, fmt.Errorf("exec update failed: %w", err)
		}
	}

	// Commit the batch updates.
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit failed: %w", err)
	}

	// Advance lastID to the highest ID we’ve processed in this batch.
	lastID = batch[len(batch)-1].ID
	return lastID, nil
}

func normalizeModuleName(original string) string {
	// Inconsistent capitalization is the most common issue.
	name := strings.ToLower(original)

	// Then there are some common prefixes that can be removed.
	if strings.HasPrefix(name, "www.github.com/") {
		return strings.TrimPrefix(name, "www.")
	}

	if strings.HasPrefix(original, "gopkg.in/") {
		// TODO: Why does https://pkg.go.dev/github.com/go-yaml/yaml/v3 redirect to https://pkg.go.dev/gopkg.in/yaml.v2?
		// From https://labix.org/gopkg.in:
		//
		//   The gopkg.in service provides versioned URLs that offer the proper metadata for redirecting the go tool onto well defined GitHub repositories.
		//
		//   gopkg.in/pkg.v3      → github.com/go-pkg/pkg (branch/tag v3, v3.N, or v3.N.M)
		//   gopkg.in/user/pkg.v3 → github.com/user/pkg   (branch/tag v3, v3.N, or v3.N.M)
	}

	return name
}

func indexCommand() error {
	db, err := sql.Open("sqlite", "file:index.db?_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	_, err = db.Exec("CREATE TABLE IF NOT EXISTS paths (id INTEGER PRIMARY KEY ASC, path TEXT NOT NULL UNIQUE);")
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	_, err = db.Exec("CREATE TABLE IF NOT EXISTS versions (path_id INTEGER REFERENCES paths(id), version TEXT, timestamp TEXT, PRIMARY KEY(path_id, version)) WITHOUT ROWID; CREATE INDEX IF NOT EXISTS idx_versions_timestamp ON versions(timestamp);")
	if err != nil {
		return fmt.Errorf("create table: %w", err)
	}

	var last index.VersionInfo
	row := db.QueryRow("SELECT p.path, v.version, v.timestamp FROM versions AS v JOIN paths AS p ON p.id = v.path_id ORDER BY v.timestamp DESC LIMIT 1;")
	var timestamp string
	err = row.Scan(&last.Path, &last.Version, &timestamp)
	if !errors.Is(err, sql.ErrNoRows) {
		if err != nil {
			return fmt.Errorf("scan max row: %w", err)
		}
		last.Timestamp, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return fmt.Errorf("parse timestamp: %w", err)
		}
	}

	client, err := index.New("https://index.golang.org/index", http.DefaultClient)
	if err != nil {
		return fmt.Errorf("new index client: %w", err)
	}
	ctx := context.Background()

	start := time.Now()
	covered := time.Duration(0)

	// Start late to test end condition.
	//t, _ := time.Parse(time.RFC3339Nano, "2025-01-19T04:09:12.702162Z")
	//last = index.VersionInfo{
	//	Path:      "buf.build/gen/go/mickamy/sampay/connectrpc/go",
	//	Version:   "v1.11.0-20250118034021-69ec5b555ef6.1",
	//	Timestamp: t,
	//}

	for {
		if !last.Timestamp.IsZero() {
			fmt.Print("\033[H\033[2J") // Clear screen

			target := time.Now().UTC()
			duration := target.Sub(start)
			coveredHours := int64(covered.Hours())
			openHours := int64(target.Sub(last.Timestamp).Hours())

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
			fmt.Fprintf(w, "Duration\t%s\n", duration.Round(time.Second))
			fmt.Fprintf(w, "Target\t%s\n", target.Format(time.RFC3339))
			fmt.Fprintf(w, "Current\t%s\n", last.Timestamp.Format(time.RFC3339))
			fmt.Fprintf(w, "Hours done\t%d\n", coveredHours)
			fmt.Fprintf(w, "Hours open\t%d\n", openHours)

			if coveredHours > 0 {
				expectedRemainingRuntime := time.Duration(openHours * int64(duration) / coveredHours)
				coveredHoursPerMinute := float64(coveredHours) / duration.Minutes()

				fmt.Fprintf(w, "Remaining\t%s\n", expectedRemainingRuntime.Round(time.Second))
				fmt.Fprintf(w, "ETL\t%s\n", target.Add(expectedRemainingRuntime).Local().Format(time.RFC3339))
				fmt.Fprintf(w, "Speed\t%.2f hours/minute\n", coveredHoursPerMinute)
			}

			if err := w.Flush(); err != nil {
				return fmt.Errorf("flush: %w", err)
			}
		}

		versions, err := client.GetVersions(ctx, last.Timestamp, 2000)
		if err != nil {
			return fmt.Errorf("get versions: %w", err)
		}
		//fmt.Println("Since:", last.DebugString())
		//fmt.Println("First:", versions[0].DebugString())
		//fmt.Println("Last:", versions[len(versions)-1].DebugString())

		if len(versions) == 0 ||
			(len(versions) == 1 &&
				versions[0].Path == last.Path &&
				versions[0].Version == last.Version &&
				versions[0].Timestamp == last.Timestamp) {
			fmt.Println("Index is up-to-date")
			return nil
		}

		// The transactions primary purpose is to speed up the inserts
		// as it allows the database to batch them together on commit.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		for i, v := range versions {
			if i == 0 && !last.Timestamp.IsZero() {
				if v.Path == last.Path && v.Version == last.Version && v.Timestamp == last.Timestamp {
					// The last item in the previous list should be the first item in the current list.
					// We can skip this item.
					continue
				} else {
					_, _ = fmt.Fprintf(os.Stderr, "BUG: index: expected list to start with %s but got %s\n", last.DebugString(), v.DebugString())
				}
			}

			if i > 0 {
				covered += v.Timestamp.Sub(versions[i-1].Timestamp)
			}

			row := tx.QueryRow("SELECT id FROM paths WHERE path = ?", v.Path)
			var pathID int64
			err = row.Scan(&pathID)
			if errors.Is(err, sql.ErrNoRows) {
				// Insert a new path.
				res, err := tx.Exec("INSERT INTO paths (path) VALUES (?)", v.Path)
				if err != nil {
					return fmt.Errorf("insert path: %w", err)
				}
				pathID, err = res.LastInsertId()
				if err != nil {
					return fmt.Errorf("last insert id: %w", err)
				}
			} else if err != nil {
				return fmt.Errorf("select path: %w", err)
			}

			// TODO: Maybe just let it fail? This way we would know if the index contains duplicates.

			// With INSERT OR REPLACE we make sure that the timestamp is always the latest.
			// This is defensive, but we cannot be sure that the index cannot contain duplicate versions.
			_, err := tx.Exec("INSERT OR REPLACE INTO versions (path_id, version, timestamp) VALUES (?, ?, ?)", pathID, v.Version, v.Timestamp.Format(time.RFC3339Nano))
			if err != nil {
				return fmt.Errorf("insert version: %w", err)
			}
		}
		err = tx.Commit()
		if err != nil {
			return fmt.Errorf("commit transaction: %w", err)
		}

		// Continue with the next batch
		// which starts with the last item
		// of the batch we just processed.
		last = *versions[len(versions)-1]
	}
}

func alternativesCommand(args []string, lookup *lookup.Lookup) error {
	if len(args) != 1 {
		return fmt.Errorf("expected 1 argument, got %d", len(args))
	}
	name := args[0]
	links, ok := lookup.Packages[name]
	if !ok {
		return fmt.Errorf("package %s not found", name)
	}
	fmt.Println(name, "found")
	for _, l := range links {
		fmt.Println(l.Source.Name, ">", l.Category.Name)
		for _, other := range l.Category.Links {
			if other != l {
				fmt.Printf("  %s\n    %s\n", other.URL, other.Description)
			} else {
				fmt.Printf("=>%s\n    %s\n", l.URL, l.Description)
			}
		}
	}
	return nil
}

func goProxyCommand(args []string, lookup *lookup.Lookup) error {
	if len(args) != 1 {
		return fmt.Errorf("expected 1 argument, got %d", len(args))
	}
	_, ok := lookup.Packages[args[0]]
	if !ok {
		return fmt.Errorf("package %s not found", args[0])
	}
	resp, err := http.Get(fmt.Sprintf("https://proxy.golang.org/%s/@latest", args[0]))
	if err != nil {
		return fmt.Errorf("get latest version info: %w", err)
	}
	defer resp.Body.Close()

	var info VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return fmt.Errorf("decode version info: %w", err)
	}

	fmt.Println("Version:", info.Version)
	fmt.Println("Time:", info.Time)
	fmt.Println("URL:", info.Origin.URL)

	return nil
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

func strangeCommand(lookup *lookup.Lookup) error {
	for name, links := range lookup.Packages {
		// TODO: We should probably clean this up somewhere.
		n := strings.TrimRight(name, "/")
		if strings.Count(n, "/") != 2 {
			if !strings.HasPrefix(n, "gitssslab.com") {
				var sources []string
				for _, link := range links {
					sources = append(sources, link.Source.Name)
				}
				fmt.Println(n, sources)
			}
		}
	}

	return nil
}

func downloadLatestVersionInfo(module string) (vi VersionInfo, err error) {
	switch {
	case strings.HasPrefix(module, "pkg.go.dev/"):
		module, _ = strings.CutPrefix(module, "pkg.go.dev/")
	case strings.HasPrefix(module, "github.com/"):
		before, after, found := strings.Cut(module, "/tree/master")
		if found {
			module = before + after
			break
		}
		before, after, found = strings.Cut(module, "/tree/main")
		if found {
			module = before + after
			break
		}
	}

	canonical := strings.ToLower(module) // go proxy requires lowercase
	resp, err := http.Get(fmt.Sprintf("https://proxy.golang.org/%s/@latest", canonical))
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

func save(root *os.Root, result dlResult) (err error) {
	// Create the directory structure.
	parts := strings.Split(result.module, "/")
	for i := 1; i <= len(parts); i++ {
		dir := strings.Join(parts[:i], "/")
		fi, err := root.Stat(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("stat dir: %w", err)
			}
		}
		if err == nil && fi.IsDir() {
			continue
		}
		err = root.Mkdir(dir, 0755)
		if err != nil {
			return fmt.Errorf("make dir: %w", err)
		}
	}

	f, err := root.Create(result.module + "/latest.json")
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer func() {
		closeErr := f.Close()
		if closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	return json.NewEncoder(f).Encode(result.latest)
}

type dlResult struct {
	module string
	latest VersionInfo
	err    error
}

func downloadWorker(wg *sync.WaitGroup, modules <-chan string, results chan<- dlResult) {
	defer wg.Done()
	for mod := range modules {
		info, err := downloadLatestVersionInfo(mod)
		results <- dlResult{module: mod, latest: info, err: err}
	}
}

func downloadInfoCommand(lookup *lookup.Lookup) error {
	err := os.MkdirAll("./cache", 0755)
	if err != nil {
		return fmt.Errorf("make cache dir: %w", err)
	}
	root, err := os.OpenRoot("cache")
	if err != nil {
		return fmt.Errorf("open root: %w", err)
	}

	var toDownload []string
	for module := range lookup.Packages {
		if _, err := root.Stat(module + "/latest.json"); os.IsNotExist(err) {
			toDownload = append(toDownload, module)
		} else if err != nil {
			return fmt.Errorf("stat: %w", err)
		}
	}

	modules := make(chan string, len(toDownload))
	results := make(chan dlResult, len(toDownload))
	var wg sync.WaitGroup
	numWorkers := 50
	wg.Add(numWorkers)
	for range numWorkers {
		go downloadWorker(&wg, modules, results)
	}

	total := len(toDownload)
	remaining := total
	saveDone := make(chan struct{})
	go func() {
		for result := range results {
			remaining--
			if result.err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "%d/%d | Error downloading %q: %v\n", total-remaining, total, result.module, result.err)
				continue
			}
			err := save(root, result)
			if err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "%d/%d | Error saving %q: %v\n", total-remaining, total, result.module, err)
				continue
			}
			_, _ = fmt.Fprintf(os.Stderr, "%d/%d | Downloaded %q\n", total-remaining, total, result.module)
		}
		close(saveDone)
	}()

	for _, name := range toDownload {
		modules <- name
	}
	close(modules)

	wg.Wait()
	close(results)

	<-saveDone

	return nil
}

func multiURLCommand(lookup *lookup.Lookup) error {
	for name, links := range lookup.Packages {
		seen := make(map[string]bool)
		for _, link := range links {
			seen[link.URL] = true
		}
		if len(seen) > 1 {
			fmt.Printf("Multiple URLs for package %s\n", name)
			for _, link := range links {
				fmt.Println("-", link.URL)
			}
		}
	}
	return nil
}

func githubCommand(args []string, lookup *lookup.Lookup) error {
	if len(args) != 1 {
		return fmt.Errorf("expected 1 argument, got %d", len(args))
	}
	links, ok := lookup.Packages[args[0]]
	if !ok {
		return fmt.Errorf("package %s not found", args[0])
	}
	link := links[0]

	u, err := url.Parse(link.URL)
	if err != nil {
		return fmt.Errorf("parse URL: %w", err)
	}
	if u.Host != "github.com" {
		return fmt.Errorf("expected github.com URL, got %s", u.Host)
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 3 {
		return fmt.Errorf("expected /<owner>/<repo> URL, got %s", u.Path)
	}

	client := github.NewClient(nil)
	repo, _, err := client.Repositories.Get(context.Background(), parts[1], parts[2])
	if err != nil {
		return fmt.Errorf("get repository: %w", err)
	}
	fmt.Println("Repo:", repo.GetFullName())
	fmt.Println("Updated at:", repo.GetUpdatedAt())
	fmt.Println("Watchers:", repo.GetWatchers())
	fmt.Println("Stargazers:", repo.GetStargazersCount())
	fmt.Println("Forks:", repo.GetForksCount())
	fmt.Println("Open Issues:", repo.GetOpenIssuesCount())
	fmt.Println("Description:", repo.GetDescription())
	fmt.Println("Topics:", repo.Topics)

	return nil
}
