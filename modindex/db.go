package modindex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ngrash/modhunt/modindex/internal/index"
)

func SynchronizeDatabase(ctx context.Context) (err error) {
	db, err := setup()
	if err != nil {
		return fmt.Errorf("setup database: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	last, err := lastVersionInfo(db)
	if err != nil {
		return err
	}

	client, err := index.New("https://index.golang.org/index", http.DefaultClient)
	if err != nil {
		return fmt.Errorf("new index client: %w", err)
	}

	start := time.Now()
	covered := time.Duration(0)

	for {
		if err := printProgress(last, start, covered); err != nil {
			return fmt.Errorf("print progress: %w", err)
		}

		// Fetch a batch of version updates from the index server that
		// happened after the timestamp of the last version we have in the database.
		// The timestamp is inclusive, so the response will container the last version
		// we have in the database. If this is the first batch, the last timestamp is
		// zero and the response will start with the first version it has.
		versions, err := client.GetVersions(ctx, last.Timestamp, 2000)
		if err != nil {
			return fmt.Errorf("get versions: %w", err)
		}

		// If this is not the first batch, 'last' contains the last version
		// we have in the database. The first version in the response should
		// be the same as the last version in the previous batch.
		// Validate this assumption and remove the first item from the list
		// of versions to insert.
		var versionsToInsert []*index.VersionInfo
		if last.Timestamp.IsZero() {
			versionsToInsert = versions
		} else {
			if len(versions) > 0 &&
				versions[0].Timestamp == last.Timestamp &&
				versions[0].Path == last.Path &&
				versions[0].Version == last.Version {
				// The first item in the list is the same as the last item in the previous list.
				// That's what we expect. Remove it.
				versionsToInsert = versions[1:]
			} else {
				_, _ = fmt.Fprintf(os.Stderr, "BUG: index: expected list to start with %s but got %s\n", last.DebugString(), versions[0].DebugString())
				versionsToInsert = versions
			}
		}

		if len(versionsToInsert) == 0 {
			fmt.Println("Index is up-to-date")
			break
		}

		if err := insertVersions(ctx, db, versionsToInsert); err != nil {
			return fmt.Errorf("insert batch: %w", err)
		}

		// Calculate how much time we covered with this batch.
		// If this was the first batch, 'last' is zero and the
		// time covered is the time between the first and last
		// version timestamp in the batch.
		// If this was not the first batch, the time covered is
		// the time between the last version of the previous batch
		// and the last version of this batch.
		if last.Timestamp.IsZero() {
			covered = versionsToInsert[len(versionsToInsert)-1].Timestamp.Sub(versionsToInsert[0].Timestamp)
		} else {
			covered += versionsToInsert[len(versionsToInsert)-1].Timestamp.Sub(last.Timestamp)
		}

		// Continue with the next batch
		// which starts with the last item
		// of the batch we just processed.
		last = *versionsToInsert[len(versionsToInsert)-1]
		continue
	}

	return nil
}

func printProgress(last index.VersionInfo, start time.Time, covered time.Duration) error {
	if !last.Timestamp.IsZero() {
		fmt.Print("\033[H\033[2J") // Clear screen

		target := time.Now().UTC()
		duration := target.Sub(start)
		coveredHours := int64(covered.Hours())
		openHours := int64(target.Sub(last.Timestamp).Hours())

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
		_, _ = fmt.Fprintf(w, "Duration\t%s\n", duration.Round(time.Second))
		_, _ = fmt.Fprintf(w, "Target\t%s\n", target.Format(time.RFC3339))
		_, _ = fmt.Fprintf(w, "Current\t%s\n", last.Timestamp.Format(time.RFC3339))
		_, _ = fmt.Fprintf(w, "Hours done\t%d\n", coveredHours)
		_, _ = fmt.Fprintf(w, "Hours open\t%d\n", openHours)

		if coveredHours > 0 {
			expectedRemainingRuntime := time.Duration(openHours * int64(duration) / coveredHours)
			coveredHoursPerMinute := float64(coveredHours) / duration.Minutes()

			_, _ = fmt.Fprintf(w, "Remaining\t%s\n", expectedRemainingRuntime.Round(time.Second))
			_, _ = fmt.Fprintf(w, "ETL\t%s\n", target.Add(expectedRemainingRuntime).Local().Format(time.RFC3339))
			_, _ = fmt.Fprintf(w, "Speed\t%.2f hours/minute\n", coveredHoursPerMinute)
		}

		if err := w.Flush(); err != nil {
			return fmt.Errorf("flush: %w", err)
		}
	}
	return nil
}

func insertVersions(ctx context.Context, db *sql.DB, versions []*index.VersionInfo) error {
	// The transactions primary purpose is to speed up the inserts
	// as it allows the database to batch them together on commit.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	for _, v := range versions {
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

		_, err := tx.Exec("INSERT INTO versions (path_id, version, timestamp) VALUES (?, ?, ?)", pathID, v.Version, v.Timestamp.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("insert version: %w", err)
		}
	}
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

func lastVersionInfo(db *sql.DB) (index.VersionInfo, error) {
	var last index.VersionInfo
	row := db.QueryRow("SELECT p.path, v.version, v.timestamp FROM versions AS v JOIN paths AS p ON p.id = v.path_id ORDER BY v.timestamp DESC LIMIT 1;")
	var timestamp string
	err := row.Scan(&last.Path, &last.Version, &timestamp)
	if !errors.Is(err, sql.ErrNoRows) {
		if err != nil {
			return last, fmt.Errorf("scan max row: %w", err)
		}
		last.Timestamp, err = time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			return last, fmt.Errorf("parse timestamp: %w", err)
		}
	}
	return last, nil
}

func setup() (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:index.db?_pragma=foreign_keys(1)&_time_format=sqlite")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	_, err = db.Exec("CREATE TABLE IF NOT EXISTS paths (id INTEGER PRIMARY KEY ASC, path TEXT NOT NULL UNIQUE);")
	if err != nil {
		return nil, fmt.Errorf("create paths table: %w", err)
	}

	_, err = db.Exec("CREATE TABLE IF NOT EXISTS versions (path_id INTEGER REFERENCES paths(id), version TEXT, timestamp TEXT, PRIMARY KEY(path_id, version)) WITHOUT ROWID; CREATE INDEX IF NOT EXISTS idx_versions_timestamp ON versions(timestamp);")
	if err != nil {
		return nil, fmt.Errorf("create versions table: %w", err)
	}

	return db, nil
}
