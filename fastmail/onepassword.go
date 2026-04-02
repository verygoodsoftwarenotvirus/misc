package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type OPItem struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Category string   `json:"category"`
	Tags     []string `json:"tags,omitempty"`
	URLs     []OPURL  `json:"urls,omitempty"`
}

type OPURL struct {
	Href    string `json:"href"`
	Primary bool   `json:"primary"`
}

type OPItemDetail struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Category string      `json:"category"`
	Tags     []string    `json:"tags,omitempty"`
	Fields   []OPField   `json:"fields"`
	Sections []OPSection `json:"sections,omitempty"`
	URLs     []OPURL     `json:"urls,omitempty"`
}

type OPField struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Purpose string          `json:"purpose"`
	Label   string          `json:"label"`
	Value   string          `json:"value"`
	Section *OPFieldSection `json:"section,omitempty"`
}

type OPFieldSection struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

type OPSection struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

func runOP(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "op", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("op %s: %w\nstderr: %s", strings.Join(args, " "), err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("op %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func opItemList(ctx context.Context, vault string) ([]OPItem, error) {
	args := []string{"item", "list", "--format", "json"}
	if vault != "" {
		args = append(args, "--vault", vault)
	}

	out, err := runOP(ctx, args...)
	if err != nil {
		return nil, err
	}

	var items []OPItem
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parsing item list: %w", err)
	}
	return items, nil
}

func opItemGet(ctx context.Context, id string) (*OPItemDetail, error) {
	out, err := runOP(ctx, "item", "get", id, "--format", "json", "--reveal")
	if err != nil {
		return nil, err
	}

	var item OPItemDetail
	if err := json.Unmarshal(out, &item); err != nil {
		return nil, fmt.Errorf("parsing item detail: %w", err)
	}
	return &item, nil
}

func opItemEdit(ctx context.Context, id string, section, field, value string, existingTags []string) error {
	assignment := fmt.Sprintf("%s.%s[text]=%s", section, field, value)

	tags := make([]string, 0, len(existingTags)+1)
	for _, t := range existingTags {
		t = strings.TrimSpace(t)
		if t != "" && t != "needs-fastmail-migration" {
			tags = append(tags, t)
		}
	}
	tags = append(tags, "needs-fastmail-migration")

	args := []string{"item", "edit", id, assignment, "--tags", strings.Join(tags, ",")}

	_, err := runOP(ctx, args...)
	return err
}

func findEmailInItem(item *OPItemDetail) string {
	// First pass: look for USERNAME purpose with @
	for _, f := range item.Fields {
		if f.Purpose == "USERNAME" && strings.Contains(f.Value, "@") {
			return f.Value
		}
	}
	// Second pass: look for any EMAIL type field
	for _, f := range item.Fields {
		if f.Type == "EMAIL" && f.Value != "" {
			return f.Value
		}
	}
	return ""
}

func hasExistingMaskedEmail(item *OPItemDetail, sectionLabel, fieldLabel string) (string, bool) {
	for _, f := range item.Fields {
		if f.Label == fieldLabel && f.Section != nil && f.Section.Label == sectionLabel && f.Value != "" {
			return f.Value, true
		}
	}
	return "", false
}
