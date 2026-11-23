package lineage

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ashish10alex/xf/internal/project"
)

// Consumer describes a SELECT-only external consumer (BI dashboard, reverse ETL,
// scheduled query, service account, or analyst) discovered from warehouse query
// history rather than table-to-table lineage.
type Consumer struct {
	// Tool is the detected BI or data tool when known (e.g. "Looker",
	// "Tableau", "Looker Studio", "Power BI", "Metabase", "Sigma", "Hex",
	// "Omni", "Lightdash", "Mode", "Hightouch", "Census",
	// "Scheduled query", "Connected Sheets"); "" for a generic SQL reader.
	Tool string `json:"tool,omitempty"`
	// Asset is a specific dashboard, explore, workbook or card identifier
	// extracted from query comments, tags or labels (e.g.
	// "explore orders, dashboard 412", "workbook a3f9c2e1").
	Asset string `json:"asset,omitempty"`
	// User is the warehouse principal (BigQuery user_email, Snowflake
	// USER_NAME, Databricks executed_by).
	User string `json:"user,omitempty"`
	// Queries is how many queries this consumer ran in the lookback window.
	Queries int `json:"queries,omitempty"`
	// Days is the lookback window in days (e.g. 14), used in display notes.
	Days int `json:"days,omitempty"`
	// LastSeen is the most recent query timestamp (RFC3339, UTC) when known.
	LastSeen string `json:"lastSeen,omitempty"`
	// SampleJobID is the most recent matching warehouse query/job ID when known.
	SampleJobID string `json:"sampleJobId,omitempty"`
	// SampleJobURL is a console link to the most recent matching warehouse job
	// (BigQuery Console or Databricks Query History) when constructible.
	SampleJobURL string `json:"sampleJobUrl,omitempty"`
}

// ID returns a stable canonical identifier for ColumnLink.Table so
// deduplication and ranking treat the same external consumer as one reader.
func (c *Consumer) ID() string {
	if c == nil {
		return ""
	}
	tool := slugPart(c.Tool)
	asset := slugPart(c.Asset)
	user := strings.ToLower(strings.TrimSpace(c.User))
	switch {
	case tool != "" && asset != "":
		return "external:" + tool + ":" + asset
	case tool != "" && user != "":
		return "external:" + tool + ":" + user
	case tool != "":
		return "external:" + tool
	case user != "":
		return "external:query:" + user
	default:
		return "external:query"
	}
}

// Label returns a human-readable display name for PR comments, terminal output
// and inline annotations, sanitized against Markdown/HTML/ANSI control chars.
func (c *Consumer) Label() string {
	if c == nil {
		return ""
	}
	tool := sanitizeMeta(c.Tool, 64)
	asset := sanitizeMeta(c.Asset, 96)
	user := sanitizeMeta(c.User, 96)
	switch {
	case tool != "" && asset != "":
		return tool + " (" + asset + ")"
	case tool != "" && user != "":
		return tool + " (" + user + ")"
	case tool != "":
		return tool
	case user != "":
		return user
	default:
		return "external query"
	}
}

// Note returns a concise frequency, recency and principal note for ReaderNote.
func (c *Consumer) Note() string { return c.note(false) }

// MarkdownNote returns Note with the recency date (or "latest job") linked to
// SampleJobURL when a validated https:// job URL is available.
func (c *Consumer) MarkdownNote() string { return c.note(true) }

func (c *Consumer) note(withLink bool) string {
	if c == nil {
		return ""
	}
	var parts []string
	if c.Queries > 0 {
		word := "queries"
		if c.Queries == 1 {
			word = "query"
		}
		q := fmt.Sprintf("%d %s", c.Queries, word)
		if c.Days > 0 {
			q += fmt.Sprintf(" in %dd", c.Days)
		}
		parts = append(parts, q)
	}
	link := ""
	if withLink {
		link = SafeJobURL(c.SampleJobURL)
	}
	if date := shortDate(c.LastSeen); date != "" {
		if link != "" {
			parts = append(parts, "[last "+date+"]("+link+")")
		} else {
			parts = append(parts, "last "+date)
		}
	} else if link != "" {
		parts = append(parts, "[latest job]("+link+")")
	}
	tool := sanitizeMeta(c.Tool, 64)
	asset := sanitizeMeta(c.Asset, 96)
	user := sanitizeMeta(c.User, 96)
	// When Label shows Tool + Asset, surface User in the note so operators know
	// which service account or analyst ran it.
	if tool != "" && asset != "" && user != "" {
		parts = append(parts, user)
	}
	if len(parts) == 0 {
		return "external query"
	}
	if tool == "" {
		parts = append(parts, "external query")
	}
	return strings.Join(parts, " · ")
}

// BigQueryJobURL returns the Google Cloud Console URL for a BigQuery query job,
// or "" when project or jobID is empty.
func BigQueryJobURL(project, location, jobID string) string {
	project = strings.TrimSpace(project)
	jobID = strings.TrimSpace(jobID)
	if project == "" || jobID == "" {
		return ""
	}
	loc := cmp.Or(strings.TrimSpace(location), "US")
	return SafeJobURL(fmt.Sprintf(
		"https://console.cloud.google.com/bigquery?project=%s&j=bq:%s:%s&page=queryresults",
		url.QueryEscape(project), url.QueryEscape(loc), url.QueryEscape(jobID),
	))
}

// DatabricksJobURL returns the Databricks SQL Query History deep link for
// queryID on workspace host, or "" when host is not an https:// URL or queryID
// is empty.
func DatabricksJobURL(host, queryID string) string {
	host = strings.TrimSpace(host)
	queryID = strings.TrimSpace(queryID)
	if host == "" || queryID == "" {
		return ""
	}
	u, err := url.Parse(host)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return ""
	}
	return SafeJobURL(fmt.Sprintf("https://%s/sql/history?queryId=%s", u.Host, url.QueryEscape(queryID)))
}

// SafeJobURL validates that raw is an https:// URL with a non-empty host and no
// control, whitespace, or Markdown/HTML delimiter characters, returning "" when
// unsafe.
func SafeJobURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`()[]<>"'\`+"`|", r) {
			return ""
		}
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" {
		return ""
	}
	return raw
}

func shortDate(rfc3339 string) string {
	s := strings.TrimSpace(rfc3339)
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return sanitizeMeta(s[:10], 10)
	}
	return ""
}

// QuerySignal holds the metadata of a SELECT query from warehouse query history
// used to identify the external or BI consumer.
type QuerySignal struct {
	User      string            // BigQuery user_email, Snowflake USER_NAME, Databricks executed_by
	Role      string            // Snowflake ROLE_NAME (optional)
	Query     string            // SQL text (may be truncated)
	QueryTag  string            // Snowflake QUERY_TAG (optional)
	ClientApp string            // Databricks client_application (optional)
	Labels    map[string]string // BigQuery job labels (optional)
}

// IsOwnXFQuery reports whether a query job was issued by xf itself so xf never
// reports its own scratch or impact checks as external consumers.
func IsOwnXFQuery(jobID, sql string, labels map[string]string) bool {
	if strings.HasPrefix(jobID, "xf_scratch_") || strings.HasPrefix(jobID, "xf_impact_") {
		return true
	}
	if labels["xf"] != "" || strings.EqualFold(labels["app"], "xf") {
		return true
	}
	return strings.Contains(sql, "/* xf:query-history */") ||
		strings.Contains(sql, "/* xf:scratch */") ||
		strings.Contains(sql, "/* xf:check */")
}

// IsTransformToolQuery reports whether a SELECT query belongs to dbt or
// Dataform itself (such as dbt tests or Dataform assertions), which are
// transformation-project checks rather than external BI consumers.
func IsTransformToolQuery(sql, queryTag string, labels map[string]string) bool {
	if labels != nil {
		for k, v := range labels {
			lk := strings.ToLower(k)
			if (lk == "app" && strings.EqualFold(v, "dbt")) ||
				lk == "dbt_invocation_id" ||
				strings.HasPrefix(lk, "dataform") ||
				strings.HasPrefix(lk, "goog-dataform") ||
				strings.HasPrefix(lk, "goog_dataform") {
				return true
			}
		}
	}
	if strings.Contains(queryTag, `"app":"dbt"`) || strings.Contains(queryTag, `"app": "dbt"`) {
		return true
	}
	for _, c := range extractComments(sql) {
		trimmed := strings.TrimSpace(c)
		if strings.HasPrefix(trimmed, "{") && (strings.Contains(trimmed, `"app": "dbt"`) || strings.Contains(trimmed, `"app":"dbt"`)) {
			return true
		}
	}
	return false
}

// ParseConsumer identifies the BI or external consumer from query history
// metadata, falling back gracefully to the raw warehouse principal.
func ParseConsumer(sig QuerySignal) Consumer {
	c := Consumer{User: strings.TrimSpace(sig.User)}
	if c.User == "" {
		c.User = strings.TrimSpace(sig.Role)
	}
	comments := extractComments(sig.Query)

	if tool, asset, ok := parseLooker(sig, comments); ok {
		c.Tool, c.Asset = tool, asset
		return c
	}
	if tool, asset, ok := parseTableau(sig, comments); ok {
		c.Tool, c.Asset = tool, asset
		return c
	}
	if tool, asset, ok := parseMetabase(sig, comments); ok {
		c.Tool, c.Asset = tool, asset
		return c
	}
	if tool, asset, ok := parseLabels(sig.Labels); ok {
		c.Tool, c.Asset = tool, asset
		return c
	}
	if tool, asset, ok := parseOtherTags(sig, comments); ok {
		c.Tool, c.Asset = tool, asset
		return c
	}
	if tool, ok := parseClientApp(sig.ClientApp); ok {
		c.Tool = tool
		return c
	}
	if tool, ok := parsePrincipalTool(sig.User, sig.Role); ok {
		c.Tool = tool
		return c
	}
	return c
}

var (
	ansiCSIRE       = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	sqlJoinRE       = regexp.MustCompile(`(?i)\bJOIN\b`)
	lookerCommentRE = regexp.MustCompile(`(?i)Looker Query Context\s*'(\{[^'\n]*\})'`)
	tableauOriginRE = regexp.MustCompile(`(?i)["']tableau-query-origins["']\s*:\s*(\{[^}\n]*\}|"[^"\n]*")`)
	tableauLuidRE   = regexp.MustCompile(`(?i)["']?(workbook|datasource|view)[-_]luid["']?\s*[:=]\s*["']?([a-z0-9-]+)["']?`)
	metabaseRE      = regexp.MustCompile(`(?i)\bMetabase::([^\n]+)`)
	metabaseCardRE  = regexp.MustCompile(`(?i)\bcardID:\s*(\d+)`)
	metabaseDashRE  = regexp.MustCompile(`(?i)\bdashboardID:\s*(\d+)`)
	hexHeaderRE     = regexp.MustCompile(`(?i)^\s*Hex\s*:\s*(.*)$`)
	sigmaHeaderRE   = regexp.MustCompile(`(?i)^\s*Sigma(?:\s+Computing)?\s*[:{]\s*(.*)$`)
	omniHeaderRE    = regexp.MustCompile(`(?i)^\s*Omni\s*[:{]\s*(.*)$`)
	lightdashHdrRE  = regexp.MustCompile(`(?i)^\s*Lightdash\s*[:{]\s*(.*)$`)
	modeHeaderRE    = regexp.MustCompile(`(?i)^\s*Mode\s+Analytics\b`)
	powerBIHdrRE    = regexp.MustCompile(`(?i)^\s*(?:Power\s*BI|Microsoft\.Mashup)\b`)
	tableauSQLHint  = regexp.MustCompile(`(?i)(__tableau_|#Tableau_|` + "`Custom SQL Query`" + `|"Custom SQL Query"|\bAS\s+` + "`Custom SQL Query`" + `|\bTEMP_Calculation_\d+__\d+__\d+_|\bCalculation_\d{15,}|1\.1000000000000001\s+AS\s+[` + "`\"" + `]X__alias__0[` + "`\"" + `]|\bAS\s+[` + "`\"" + `](?:wd|wk|yr|qr|mn|dy|tdy|twk|tmn|tqr|tyr|md|my|none|cntd|attr)_[A-Za-z0-9_]+_(?:ok|nk|qk)[` + "`\"" + `])`)
)

// extractComments returns the text inside SQL line comments (--) and block
// comments (/* ... */), skipping single-quoted, double-quoted and backticked
// spans so string literals can never trigger comment-based vendor detection.
func extractComments(sql string) []string {
	var out []string
	for i := 0; i < len(sql); {
		switch sql[i] {
		case '\'', '"', '`':
			q := sql[i]
			i++
			for i < len(sql) {
				if sql[i] == '\\' && q != '`' && i+1 < len(sql) {
					i += 2
					continue
				}
				if sql[i] == q {
					i++
					if i < len(sql) && sql[i] == q {
						i++
						continue
					}
					break
				}
				i++
			}
		case '-':
			if i+1 < len(sql) && sql[i+1] == '-' {
				start := i + 2
				end := start
				for end < len(sql) && sql[end] != '\n' {
					end++
				}
				out = append(out, sql[start:end])
				i = end
				continue
			}
			i++
		case '/':
			if i+1 < len(sql) && sql[i+1] == '*' {
				start := i + 2
				end := strings.Index(sql[start:], "*/")
				if end < 0 {
					out = append(out, sql[start:])
					return out
				}
				out = append(out, sql[start:start+end])
				i = start + end + 2
				continue
			}
			i++
		default:
			i++
		}
	}
	return out
}

func parseLooker(sig QuerySignal, comments []string) (string, string, bool) {
	for _, c := range comments {
		if m := lookerCommentRE.FindStringSubmatch(c); len(m) == 2 {
			return "Looker", formatLookerJSON(m[1]), true
		}
	}
	if tag := strings.TrimSpace(sig.QueryTag); tag != "" && hasToken(tag, "looker") {
		return "Looker", formatLookerJSON(tag), true
	}
	return "", "", false
}

func formatLookerJSON(raw string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	var parts []string
	if exp := jsonStr(m, "explore"); exp != "" {
		parts = append(parts, "explore "+exp)
	} else if model := jsonStr(m, "model"); model != "" {
		parts = append(parts, "model "+model)
	}
	if dash := jsonStr(m, "dashboard_id"); dash != "" {
		parts = append(parts, "dashboard "+dash)
	} else if look := jsonStr(m, "look_id"); look != "" {
		parts = append(parts, "look "+look)
	}
	return strings.Join(parts, ", ")
}

func parseTableau(sig QuerySignal, comments []string) (string, string, bool) {
	// Only check query comments and QueryTag for Tableau metadata — never raw
	// SQL predicates like WHERE datasource_luid = '...'.
	sources := make([]string, 0, len(comments)+1)
	if sig.QueryTag != "" {
		sources = append(sources, sig.QueryTag)
	}
	sources = append(sources, comments...)
	for _, src := range sources {
		if m := tableauOriginRE.FindStringSubmatch(src); len(m) == 2 {
			asset := parseTableauAsset(m[1])
			if asset == "" {
				asset = parseTableauAsset(src)
			}
			return "Tableau", asset, true
		}
		if m := tableauLuidRE.FindStringSubmatch(src); len(m) == 3 {
			return "Tableau", strings.ToLower(m[1]) + " " + shortLUID(m[2]), true
		}
	}
	if hasToken(sig.ClientApp, "tableau") {
		return "Tableau", "", true
	}
	// Strip comments and single-quoted string literals before checking SQL structure.
	stripped := project.StripSQL(sig.Query, project.StripOptions{BlankSingleStrings: true})
	if tableauSQLHint.MatchString(stripped) {
		return "Tableau", "", true
	}
	return "", "", false
}

func parseTableauAsset(s string) string {
	if m := tableauLuidRE.FindStringSubmatch(s); len(m) == 3 {
		return strings.ToLower(m[1]) + " " + shortLUID(m[2])
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err == nil {
		for _, k := range []string{"workbook-luid", "workbook_luid", "workbook", "datasource-luid", "datasource_luid", "view-luid"} {
			if v := jsonStr(m, k); v != "" {
				kind, _, _ := strings.Cut(strings.ReplaceAll(k, "_", "-"), "-")
				return kind + " " + shortLUID(v)
			}
		}
	}
	return ""
}

func shortLUID(id string) string {
	runes := []rune(strings.TrimSpace(id))
	if len(runes) > 12 {
		return string(runes[:8])
	}
	return string(runes)
}

func parseMetabase(sig QuerySignal, comments []string) (string, string, bool) {
	for _, c := range comments {
		if m := metabaseRE.FindStringSubmatch(c); len(m) == 2 {
			return "Metabase", formatMetabaseMeta(m[1]), true
		}
	}
	if hasToken(sig.QueryTag, "metabase") {
		return "Metabase", formatMetabaseMeta(sig.QueryTag), true
	}
	return "", "", false
}

func formatMetabaseMeta(meta string) string {
	var parts []string
	if m := metabaseDashRE.FindStringSubmatch(meta); len(m) == 2 {
		parts = append(parts, "dashboard "+m[1])
	}
	if m := metabaseCardRE.FindStringSubmatch(meta); len(m) == 2 {
		parts = append(parts, "card "+m[1])
	}
	return strings.Join(parts, ", ")
}

// parseLabels inspects BigQuery job labels in deterministic priority order.
func parseLabels(labels map[string]string) (string, string, bool) {
	if len(labels) == 0 {
		return "", "", false
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	// Priority 1: known BigQuery first-party label keys.
	var reportID string
	for _, k := range keys {
		lk := strings.ToLower(k)
		if lk == "looker_studio_report_id" || lk == "goog_datastudio_report_id" {
			if v := strings.TrimSpace(labels[k]); v != "" {
				reportID = "report " + shortLUID(v)
			}
		}
	}
	for _, k := range keys {
		lk, lv := strings.ToLower(k), strings.ToLower(labels[k])
		if lv == "looker_studio" || lv == "data_studio" || lv == "looker-studio" || lv == "datastudio" || hasToken(lk, "datastudio") || reportID != "" && lk == "looker_studio_report_id" {
			return "Looker Studio", reportID, true
		}
	}
	for _, k := range keys {
		lk, lv := strings.ToLower(k), strings.ToLower(labels[k])
		if lk == "data_source_id" && lv == "scheduled_query" {
			return "Scheduled query", "", true
		}
		if lv == "sheets_connector" || lv == "connected_sheets" || hasToken(lk, "sheets_connector") {
			return "Connected Sheets", "", true
		}
	}
	// Priority 2: vendor tokens in tool-identifying label keys (e.g. app, tool,
	// requestor, client, source) — avoid matching ordinary dataset/domain labels
	// such as dataset=us_census_data.
	toolLabelKeys := map[string]bool{
		"app": true, "application": true, "tool": true, "bi": true, "bi_tool": true,
		"source": true, "requestor": true, "client": true, "client_app": true,
		"creator": true, "service": true, "vendor": true, "origin": true, "connector": true,
	}
	type rule struct {
		tokens []string
		tool   string
	}
	rules := []rule{
		{[]string{"looker"}, "Looker"},
		{[]string{"tableau"}, "Tableau"},
		{[]string{"powerbi", "power_bi"}, "Power BI"},
		{[]string{"hightouch"}, "Hightouch"},
		{[]string{"census"}, "Census"},
		{[]string{"metabase"}, "Metabase"},
		{[]string{"sigma"}, "Sigma"},
		{[]string{"lightdash"}, "Lightdash"},
		{[]string{"omni"}, "Omni"},
		{[]string{"hex"}, "Hex"},
	}
	for _, r := range rules {
		for _, k := range keys {
			lk := strings.ToLower(k)
			v := labels[k]
			for _, tok := range r.tokens {
				if hasToken(lk, tok) || (toolLabelKeys[lk] && hasToken(v, tok)) {
					return r.tool, "", true
				}
			}
		}
	}
	return "", "", false
}

func parseOtherTags(sig QuerySignal, comments []string) (string, string, bool) {
	if tag := strings.TrimSpace(sig.QueryTag); tag != "" {
		// For JSON query tags, match against top-level keys or tool-identifying
		// fields (app/tool/source/vendor) rather than arbitrary pipeline names
		// like {"pipeline":"omnichannel_us_census_data"}.
		if strings.HasPrefix(tag, "{") {
			if tool, asset, ok := parseJSONQueryTag(tag); ok {
				return tool, asset, true
			}
		} else {
			switch {
			case hasToken(tag, "lightdash"):
				return "Lightdash", "", true
			case hasToken(tag, "sigma"):
				return "Sigma", "", true
			case hasToken(tag, "hex"):
				return "Hex", "", true
			case hasToken(tag, "omni"):
				return "Omni", "", true
			case hasToken(tag, "powerbi") || hasPhrase(tag, "power", "bi") || hasToken(tag, "mashup"):
				return "Power BI", "", true
			case hasToken(tag, "hightouch"):
				return "Hightouch", "", true
			case hasToken(tag, "census"):
				return "Census", "", true
			case hasToken(tag, "superset") || strings.EqualFold(strings.TrimSpace(tag), "preset"):
				return "Superset", "", true
			case hasToken(tag, "thoughtspot"):
				return "ThoughtSpot", "", true
			}
		}
	}
	for _, c := range comments {
		switch {
		case lightdashHdrRE.MatchString(c):
			return "Lightdash", "", true
		case sigmaHeaderRE.MatchString(c):
			return "Sigma", "", true
		case hexHeaderRE.MatchString(c):
			return "Hex", "", true
		case omniHeaderRE.MatchString(c):
			return "Omni", "", true
		case modeHeaderRE.MatchString(c):
			return "Mode", "", true
		case powerBIHdrRE.MatchString(c):
			return "Power BI", "", true
		}
	}
	return "", "", false
}

func parseJSONQueryTag(tag string) (string, string, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(tag), &m); err != nil {
		return "", "", false
	}
	// Collect top-level keys plus values of explicit app/tool/source/vendor fields.
	var candidates []string
	for k := range m {
		candidates = append(candidates, k)
	}
	for _, field := range []string{"app", "application", "tool", "source", "vendor", "client", "origin"} {
		if v := jsonStr(m, field); v != "" {
			candidates = append(candidates, v)
		}
	}
	combined := strings.Join(candidates, " ")
	switch {
	case hasToken(combined, "lightdash"):
		return "Lightdash", jsonFirst(tag, "dashboard_name", "chart_name", "explore"), true
	case hasToken(combined, "sigma"):
		return "Sigma", jsonFirst(tag, "workbook", "workbookId", "workbook_id"), true
	case hasToken(combined, "hex"):
		return "Hex", jsonFirst(tag, "project", "project_id", "projectId"), true
	case hasToken(combined, "omni"):
		return "Omni", jsonFirst(tag, "dashboard", "dashboard_id", "topic"), true
	case hasToken(combined, "powerbi") || hasPhrase(combined, "power", "bi") || hasToken(combined, "mashup"):
		return "Power BI", "", true
	case hasToken(combined, "hightouch"):
		return "Hightouch", "", true
	case hasToken(combined, "census"):
		return "Census", "", true
	case hasToken(combined, "superset"):
		return "Superset", "", true
	case hasToken(combined, "thoughtspot"):
		return "ThoughtSpot", "", true
	}
	return "", "", false
}

func parseClientApp(app string) (string, bool) {
	if strings.TrimSpace(app) == "" {
		return "", false
	}
	switch {
	case hasToken(app, "tableau"):
		return "Tableau", true
	case hasToken(app, "powerbi") || hasPhrase(app, "power", "bi") || hasToken(app, "mashup"):
		return "Power BI", true
	case hasToken(app, "looker"):
		return "Looker", true
	case hasToken(app, "sigma"):
		return "Sigma", true
	case hasToken(app, "hex"):
		return "Hex", true
	case hasToken(app, "metabase"):
		return "Metabase", true
	case hasToken(app, "hightouch"):
		return "Hightouch", true
	case hasToken(app, "census"):
		return "Census", true
	case hasToken(app, "thoughtspot"):
		return "ThoughtSpot", true
	case hasToken(app, "superset") || strings.EqualFold(strings.TrimSpace(app), "preset") || strings.Contains(strings.ToLower(app), "preset.io"):
		return "Superset", true
	case hasToken(app, "lightdash"):
		return "Lightdash", true
	case hasToken(app, "omni"):
		return "Omni", true
	}
	return "", false
}

var principalTools = []struct {
	token string
	tool  string
}{
	{"looker", "Looker"},
	{"tableau", "Tableau"},
	{"powerbi", "Power BI"},
	{"metabase", "Metabase"},
	{"sigma", "Sigma"},
	{"lightdash", "Lightdash"},
	{"hightouch", "Hightouch"},
	{"census", "Census"},
	{"superset", "Superset"},
	{"thoughtspot", "ThoughtSpot"},
	{"omni", "Omni"},
	{"hex", "Hex"},
}

// parsePrincipalTool detects a BI/reverse-ETL tool from word-bounded tokens in
// a service account email's local part (e.g. "tableau-prod@...") or Snowflake
// role name (e.g. "LOOKER_ROLE").
func parsePrincipalTool(user, role string) (string, bool) {
	local, _, _ := strings.Cut(strings.TrimSpace(user), "@")
	tokens := splitWordTokens(local + " " + role)
	for _, tok := range tokens {
		for _, pt := range principalTools {
			if tok == pt.token {
				return pt.tool, true
			}
		}
	}
	return "", false
}

func splitWordTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func hasToken(s, target string) bool {
	for _, tok := range splitWordTokens(s) {
		if tok == target {
			return true
		}
	}
	return false
}

func hasPhrase(s, a, b string) bool {
	toks := splitWordTokens(s)
	for i := 0; i+1 < len(toks); i++ {
		if toks[i] == a && toks[i+1] == b {
			return true
		}
	}
	return false
}

func jsonStr(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%g", x)
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}

func jsonFirst(raw string, keys ...string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	for _, k := range keys {
		if v := jsonStr(m, k); v != "" {
			return v
		}
	}
	return ""
}

// slugPart preserves Unicode letters and digits separated by hyphens so
// non-ASCII asset names keep distinct canonical consumer IDs.
func slugPart(s string) string {
	return strings.Join(splitWordTokens(s), "-")
}

// sanitizeMeta strips ANSI escape sequences, Unicode control (Cc) and format
// (Cf) runes, and Markdown/HTML formatting characters so untrusted SQL comment
// text cannot spoof PR comments, terminal output, or HTML rendering.
func sanitizeMeta(s string, maxLen int) string {
	s = ansiCSIRE.ReplaceAllString(s, "")
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r) || unicode.In(r, unicode.Cf):
			continue
		case r == '`' || r == '*' || r == '<' || r == '>' || r == '[' || r == ']' || r == '\\':
			continue
		case r == '|':
			b.WriteByte('/')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if maxLen > 0 {
		runes := []rune(out)
		if len(runes) > maxLen {
			out = strings.TrimSpace(string(runes[:maxLen])) + "…"
		}
	}
	return out
}

// StrippedQuery holds a SQL query pre-stripped of comments and string literals
// so multiple columns can be checked against it without re-stripping.
type StrippedQuery struct {
	sql     string
	hasStar bool
	hasJoin bool
}

var (
	shapeDigitsRE = regexp.MustCompile(`\b\d+\b`)
	shapeSpaceRE  = regexp.MustCompile(`\s+`)
)

// StripQueryForMatch strips comments and string literals from sql once.
// Pass doubleQuotedStrings=true for BigQuery (where "..." is a string literal)
// and false for Snowflake/Databricks (where "..." can quote an identifier).
func StripQueryForMatch(sql string, doubleQuotedStrings bool) StrippedQuery {
	if strings.TrimSpace(sql) == "" {
		return StrippedQuery{}
	}
	stripped := project.StripSQL(sql, project.StripOptions{
		BlankSingleStrings: true,
		BlankDoubleStrings: doubleQuotedStrings,
	})
	return StrippedQuery{
		sql:     stripped,
		hasStar: hasSelectStar(stripped),
		hasJoin: sqlJoinRE.MatchString(stripped),
	}
}

// HasJoin reports whether the stripped SQL contains a JOIN keyword.
func (q StrippedQuery) HasJoin() bool { return q.hasJoin }

// ShapeKey returns a whitespace- and literal-normalized key for grouping
// repeated BI queries that differ only by filter literals or formatting.
func (q StrippedQuery) ShapeKey() string {
	s := shapeDigitsRE.ReplaceAllString(q.sql, "?")
	return strings.TrimSpace(shapeSpaceRE.ReplaceAllString(s, " "))
}

// ColumnMatcher precompiles identifier matchers for a column (and its nested
// leaf when dotted) so matching many queries avoids repeated regex compilation.
type ColumnMatcher struct {
	full *regexp.Regexp
	leaf *regexp.Regexp
}

// NewColumnMatcher prepares a matcher for column. Returns a zero matcher when
// column is empty or malformed (e.g. leading/trailing dot).
func NewColumnMatcher(column string) ColumnMatcher {
	col := strings.TrimSpace(column)
	if col == "" {
		return ColumnMatcher{}
	}
	parts := strings.Split(col, ".")
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return ColumnMatcher{}
		}
	}
	var m ColumnMatcher
	m.full = compileIdentRE(parts)
	if len(parts) > 1 {
		m.leaf = compileIdentRE([]string{parts[len(parts)-1]})
	}
	return m
}

// NewExactIdentMatcher prepares a matcher for a dotted identifier (such as a
// catalog.schema.table or schema.table reference) WITHOUT a leaf fallback, so
// "main.shop.orders" never falsely matches bare "orders".
func NewExactIdentMatcher(identifier string) ColumnMatcher {
	id := strings.TrimSpace(identifier)
	if id == "" {
		return ColumnMatcher{}
	}
	parts := strings.Split(id, ".")
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return ColumnMatcher{}
		}
	}
	return ColumnMatcher{full: compileIdentRE(parts)}
}

func compileIdentRE(parts []string) *regexp.Regexp {
	escaped := make([]string, len(parts))
	for i, p := range parts {
		escaped[i] = regexp.QuoteMeta(p)
	}
	return regexp.MustCompile(`(?i)` + strings.Join(escaped, `["`+"`"+`]?\.["`+"`"+`]?`))
}

// Match reports whether q references this column (best-effort static SQL
// matching; comments and literals are ignored) and whether q uses SELECT *.
func (m ColumnMatcher) Match(q StrippedQuery) (reads bool, isStar bool) {
	if m.full == nil || q.sql == "" {
		return false, false
	}
	if matchesWithBoundary(q.sql, m.full) {
		return true, false
	}
	if m.leaf != nil && matchesWithBoundary(q.sql, m.leaf) {
		return true, false
	}
	if q.hasStar {
		return false, true
	}
	return false, false
}

// QueryReadsColumn is a convenience wrapper around StripQueryForMatch and
// NewColumnMatcher for single checks (BigQuery string semantics by default).
func QueryReadsColumn(sql, column string) (reads bool, isStar bool) {
	m := NewColumnMatcher(column)
	if m.full == nil {
		return false, false
	}
	return m.Match(StripQueryForMatch(sql, true))
}

func isIdentRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func matchesWithBoundary(s string, re *regexp.Regexp) bool {
	for from := 0; from <= len(s); {
		loc := re.FindStringIndex(s[from:])
		if loc == nil {
			return false
		}
		start, end := from+loc[0], from+loc[1]
		leftOK := start == 0
		if !leftOK {
			r, _ := utf8.DecodeLastRuneInString(s[:start])
			leftOK = !isIdentRune(r)
		}
		rightOK := end == len(s)
		if !rightOK {
			r, _ := utf8.DecodeRuneInString(s[end:])
			rightOK = !isIdentRune(r)
		}
		if leftOK && rightOK {
			return true
		}
		_, size := utf8.DecodeRuneInString(s[start:])
		if size <= 0 {
			size = 1
		}
		from = start + size
	}
	return false
}

// hasSelectStar reports whether stripped SQL contains a projection wildcard
// (* or qualifier.*) inside a SELECT list, handling SELECT *, SELECT a, *,
// SELECT t.*, `p.d.t`.*, SELECT AS STRUCT *, and SELECT TOP n * while ignoring
// COUNT(*) and arithmetic multiplication (a * b).
func hasSelectStar(stripped string) bool {
	tokens := scanSQLTokens(stripped)
	for i := 0; i < len(tokens); i++ {
		if tokens[i] != "*" {
			continue
		}
		if i == 0 {
			continue
		}
		prev := strings.ToUpper(tokens[i-1])
		// Qualified wildcard: alias.* or `p.d.t`.*
		if prev == "." {
			return true
		}
		// Unqualified wildcard directly after SELECT, comma, DISTINCT, ALL,
		// STRUCT, VALUE, or TOP <n> (but NOT after '(' like COUNT(*) or after
		// an identifier/number like a * b).
		switch prev {
		case "SELECT", ",", "DISTINCT", "ALL", "STRUCT", "VALUE":
			return true
		}
		// SELECT TOP 10 *
		if i >= 2 && isDigits(prev) && strings.EqualFold(tokens[i-2], "TOP") {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func scanSQLTokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case unicode.IsSpace(r):
			i += size
		case r == '`', r == '"':
			q := byte(r)
			j := i + 1
			for j < len(s) && s[j] != q {
				j++
			}
			if j < len(s) {
				j++
			}
			out = append(out, s[i:j])
			i = j
		case isIdentRune(r):
			j := i + size
			for j < len(s) {
				nr, ns := utf8.DecodeRuneInString(s[j:])
				if !isIdentRune(nr) {
					break
				}
				j += ns
			}
			out = append(out, s[i:j])
			i = j
		default:
			out = append(out, s[i:i+size])
			i += size
		}
	}
	return out
}
