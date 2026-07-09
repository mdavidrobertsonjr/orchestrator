package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"orchestrator/backend/internal/postings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const defaultKeepQuery = "new grad early career university software engineering"

func main() {
	confirm := flag.Bool("confirm", false, "delete low-signal postings instead of printing a dry run")
	keepQuery := flag.String("keep-query", defaultKeepQuery, "concept query postings must match to be kept")
	sampleLimit := flag.Int("sample", 25, "number of prune candidates to print")
	flag.Parse()

	databaseURL := strings.TrimSpace(os.Getenv("ORCH_DATABASE_URL"))
	if databaseURL == "" {
		log.Fatal("ORCH_DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := postings.NewPostgresStore(ctx, databaseURL)
	if err != nil {
		log.Fatalf("open postings store: %v", err)
	}
	defer store.Close()

	all, err := store.List()
	if err != nil {
		log.Fatalf("list postings: %v", err)
	}

	var prune []*postings.Posting
	for _, posting := range all {
		if !postings.MatchesQuery(*keepQuery, posting.Company, posting.Title, posting.Location, posting.URL) {
			prune = append(prune, posting)
		}
	}

	fmt.Printf("Scanned %d postings\n", len(all))
	fmt.Printf("Keep query: %q\n", *keepQuery)
	if *confirm {
		fmt.Printf("Deleting %d low-signal postings\n", len(prune))
	} else {
		fmt.Printf("Would delete %d low-signal postings\n", len(prune))
		fmt.Println("Dry run only. Re-run with --confirm to delete.")
	}

	printSample(prune, *sampleLimit)

	if !*confirm || len(prune) == 0 {
		return
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatalf("begin delete transaction: %v", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, posting := range prune {
		if _, err := tx.ExecContext(ctx, `DELETE FROM postings WHERE id = $1`, posting.ID); err != nil {
			log.Fatalf("delete posting %s: %v", posting.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit delete transaction: %v", err)
	}
	fmt.Printf("Deleted %d postings\n", len(prune))
}

func printSample(items []*postings.Posting, limit int) {
	if len(items) == 0 || limit <= 0 {
		return
	}
	if limit > len(items) {
		limit = len(items)
	}
	fmt.Printf("\nSample prune candidates (%d of %d):\n", limit, len(items))
	for _, posting := range items[:limit] {
		fmt.Printf("- %s | %s | %s | score=%d | %s\n",
			posting.Company,
			posting.Title,
			posting.Location,
			posting.MatchScore,
			posting.ID[:min(12, len(posting.ID))],
		)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
