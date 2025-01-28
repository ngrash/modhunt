package indexcmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/urfave/cli/v3"

	_ "modernc.org/sqlite"

	"github.com/ngrash/modhunt/index"
)

var Cmd = &cli.Command{
	Name: "index",
	Commands: []*cli.Command{
		updateCmd,
	},
}

var updateCmd = &cli.Command{
	Name: "update",
	Action: func(ctx context.Context, cmd *cli.Command) error {
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
	},
}
