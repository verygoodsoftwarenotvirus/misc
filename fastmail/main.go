package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

func derivePrefix(item *OPItemDetail) string {
	// Try to extract domain from primary URL
	for _, u := range item.URLs {
		if parsed, err := url.Parse(u.Href); err == nil && parsed.Host != "" {
			host := strings.TrimPrefix(parsed.Host, "www.")
			parts := strings.Split(host, ".")
			if len(parts) >= 2 {
				return sanitizePrefix(parts[len(parts)-2])
			}
			return sanitizePrefix(parts[0])
		}
	}
	// Fall back to item title
	return sanitizePrefix(item.Title)
}

func deriveDomain(item *OPItemDetail) string {
	for _, u := range item.URLs {
		if parsed, err := url.Parse(u.Href); err == nil && parsed.Host != "" {
			return strings.TrimPrefix(parsed.Host, "www.")
		}
	}
	return ""
}

func sanitizePrefix(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumeric.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if len(s) > 30 {
		s = s[:30]
	}
	if s == "" {
		return "masked"
	}
	return s
}

type stats struct {
	processed  int
	skippedDone int
	skippedNoEmail int
	failed     int
}

func main() {
	var (
		dryRun       = flag.Bool("dry-run", false, "preview changes without creating masked emails or editing 1Password items")
		vault        = flag.String("vault", "", "only process items from this 1Password vault")
		verbose      = flag.Bool("verbose", false, "print detailed progress for each item")
		fieldLabel   = flag.String("field-label", "Masked Email", "label for the masked email field")
		sectionLabel = flag.String("section-label", "Fastmail", "label for the section containing the masked email field")
	)
	flag.Parse()

	token := os.Getenv("FASTMAIL_API_TOKEN")
	if token == "" {
		slog.Error("FASTMAIL_API_TOKEN environment variable is required")
		os.Exit(1)
	}

	ctx := context.Background()
	client := &http.Client{Timeout: 30 * time.Second}

	slog.Info("fetching Fastmail account ID")
	accountID, err := fetchAccountID(ctx, client, token)
	if err != nil {
		slog.Error("failed to fetch Fastmail account ID", "error", err)
		os.Exit(1)
	}
	slog.Info("authenticated with Fastmail", "accountID", accountID)

	slog.Info("listing 1Password items")
	items, err := opItemList(ctx, *vault)
	if err != nil {
		slog.Error("failed to list 1Password items", "error", err)
		os.Exit(1)
	}
	slog.Info("found items", "count", len(items))

	var s stats
	for i, item := range items {
		if *verbose {
			slog.Info("processing item", "index", i+1, "total", len(items), "title", item.Title, "id", item.ID)
		}

		detail, err := opItemGet(ctx, item.ID)
		if err != nil {
			slog.Error("failed to get item detail", "title", item.Title, "id", item.ID, "error", err)
			s.failed++
			continue
		}

		email := findEmailInItem(detail)
		if email == "" {
			if *verbose {
				slog.Info("skipping item (no email)", "title", item.Title)
			}
			s.skippedNoEmail++
			continue
		}

		if existing, ok := hasExistingMaskedEmail(detail, *sectionLabel, *fieldLabel); ok {
			if *verbose {
				slog.Info("skipping item (already has masked email)", "title", item.Title, "maskedEmail", existing)
			}
			s.skippedDone++
			continue
		}

		prefix := derivePrefix(detail)
		domain := deriveDomain(detail)

		if *dryRun {
			fmt.Printf("[DRY RUN] %s: would create masked email (prefix=%q, domain=%q, email=%s)\n", detail.Title, prefix, domain, email)
			s.processed++
			continue
		}

		maskedEmail, err := createMaskedEmail(ctx, client, token, accountID, prefix, domain, detail.Title)
		if err != nil {
			slog.Error("failed to create masked email", "title", detail.Title, "error", err)
			s.failed++
			continue
		}

		if err := opItemEdit(ctx, item.ID, *sectionLabel, *fieldLabel, maskedEmail, detail.Tags); err != nil {
			slog.Error("failed to save masked email to 1Password", "title", detail.Title, "maskedEmail", maskedEmail, "error", err)
			s.failed++
			continue
		}

		slog.Info("created masked email", "title", detail.Title, "maskedEmail", maskedEmail)
		s.processed++

		time.Sleep(200 * time.Millisecond)
	}

	fmt.Printf("\nSummary: %d processed, %d already done, %d no email, %d failed\n",
		s.processed, s.skippedDone, s.skippedNoEmail, s.failed)

	if s.failed > 0 {
		os.Exit(1)
	}
}
