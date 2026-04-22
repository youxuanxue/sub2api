package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type clusterRow struct {
	Signature       string
	Platform        string
	StatusCode      int
	InboundEndpoint string
	RequestSHA      string
	ResponseSHA     string
	Count24h        int
	Count7d         int
	FirstSeen       time.Time
	LastSeen        time.Time
}

type reportCluster struct {
	Signature      string   `json:"signature"`
	Count24h       int      `json:"count_24h"`
	Count7d        int      `json:"count_7d"`
	FirstSeen      string   `json:"first_seen"`
	PersistentDays int      `json:"persistent_days"`
	SuggestedTags  []string `json:"suggested_tags"`
}

type report struct {
	ReportPeriod struct {
		Since string `json:"since"`
		Until string `json:"until"`
	} `json:"report_period"`
	Summary            string          `json:"summary"`
	Clusters           []reportCluster `json:"clusters"`
	PersistentClusters []reportCluster `json:"persistent_clusters"`
}

func main() {
	var (
		sinceHours int
		outputPath string
		markdownPath string
	)
	flag.IntVar(&sinceHours, "since-hours", 24, "lookback window in hours")
	flag.StringVar(&outputPath, "output", "report.json", "json output path")
	flag.StringVar(&markdownPath, "markdown", "report.md", "markdown output path")
	flag.Parse()

	dsn := strings.TrimSpace(os.Getenv("PG_DSN"))
	if dsn == "" {
		exitf("PG_DSN is required")
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		exitf("open postgres: %v", err)
	}
	defer db.Close()

	until := time.Now().UTC()
	since := until.Add(-time.Duration(sinceHours) * time.Hour)
	rows, err := db.Query(`
		SELECT
			platform,
			status_code,
			inbound_endpoint,
			request_sha256,
			response_sha256,
			count(*) FILTER (WHERE created_at >= $1) AS count_24h,
			count(*) FILTER (WHERE created_at >= $2) AS count_7d,
			min(created_at) AS first_seen,
			max(created_at) AS last_seen
		FROM qa_records
		WHERE status_code >= 400
			AND created_at >= $2
		GROUP BY platform, status_code, inbound_endpoint, request_sha256, response_sha256
		HAVING count(*) FILTER (WHERE created_at >= $1) >= 1
		ORDER BY count(*) FILTER (WHERE created_at >= $1) DESC
		LIMIT 20
	`, since, until.Add(-7*24*time.Hour))
	if err != nil {
		exitf("query qa_records: %v", err)
	}
	defer rows.Close()

	var clusters []clusterRow
	for rows.Next() {
		var row clusterRow
		if err := rows.Scan(&row.Platform, &row.StatusCode, &row.InboundEndpoint, &row.RequestSHA, &row.ResponseSHA, &row.Count24h, &row.Count7d, &row.FirstSeen, &row.LastSeen); err != nil {
			exitf("scan row: %v", err)
		}
		row.Signature = fmt.Sprintf("%s|%d|%s|%s|%s", row.Platform, row.StatusCode, row.InboundEndpoint, shortHash(row.RequestSHA), shortHash(row.ResponseSHA))
		clusters = append(clusters, row)
	}

	rep := report{}
	rep.ReportPeriod.Since = since.Format(time.RFC3339)
	rep.ReportPeriod.Until = until.Format(time.RFC3339)
	for _, row := range clusters {
		item := reportCluster{
			Signature:      row.Signature,
			Count24h:       row.Count24h,
			Count7d:        row.Count7d,
			FirstSeen:      row.FirstSeen.UTC().Format(time.RFC3339),
			PersistentDays: approximatePersistentDays(row),
			SuggestedTags:  suggestedTags(row),
		}
		rep.Clusters = append(rep.Clusters, item)
		if item.PersistentDays >= 3 && item.Count24h >= 10 {
			rep.PersistentClusters = append(rep.PersistentClusters, item)
		}
	}
	rep.Summary = fmt.Sprintf("%dh window: %d clusters, %d persistent", sinceHours, len(rep.Clusters), len(rep.PersistentClusters))

	sort.Slice(rep.Clusters, func(i, j int) bool {
		return rep.Clusters[i].Count24h > rep.Clusters[j].Count24h
	})
	sort.Slice(rep.PersistentClusters, func(i, j int) bool {
		return rep.PersistentClusters[i].Count24h > rep.PersistentClusters[j].Count24h
	})

	if err := writeJSON(outputPath, rep); err != nil {
		exitf("write json: %v", err)
	}
	if err := writeMarkdown(markdownPath, rep); err != nil {
		exitf("write markdown: %v", err)
	}
}

func writeJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

func writeMarkdown(path string, rep report) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Error Clustering Report\n\n")
	fmt.Fprintf(&b, "- Period: %s to %s\n", rep.ReportPeriod.Since, rep.ReportPeriod.Until)
	fmt.Fprintf(&b, "- Summary: %s\n\n", rep.Summary)
	fmt.Fprintf(&b, "## Top Clusters\n\n")
	for _, cluster := range rep.Clusters {
		fmt.Fprintf(&b, "- `%s` 24h=%d 7d=%d persistent_days=%d\n", cluster.Signature, cluster.Count24h, cluster.Count7d, cluster.PersistentDays)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func approximatePersistentDays(row clusterRow) int {
	days := int(row.LastSeen.Sub(row.FirstSeen).Hours()/24) + 1
	if days < 1 {
		return 1
	}
	if days > 7 {
		return 7
	}
	return days
}

func suggestedTags(row clusterRow) []string {
	tags := []string{}
	if row.StatusCode == 429 {
		tags = append(tags, "upstream_rate_limit")
	}
	if row.StatusCode >= 500 {
		tags = append(tags, "upstream_5xx")
	}
	if strings.Contains(strings.ToLower(row.InboundEndpoint), "responses") {
		tags = append(tags, "responses_api")
	}
	if len(tags) == 0 {
		tags = append(tags, "needs_triage")
	}
	return tags
}

func shortHash(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[:8]
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
