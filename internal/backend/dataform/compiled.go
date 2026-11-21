package dataform

// Raw shapes of `dataform compile --json` output. One struct covers every
// action array: fields that don't apply to a kind are simply absent, and empty
// arrays are omitted by Dataform, so everything decodes to its zero value.

type compiledTarget struct {
	Database string `json:"database"`
	Schema   string `json:"schema"`
	Name     string `json:"name"`
}

type compiledAction struct {
	Type               string           `json:"type"`
	Target             compiledTarget   `json:"target"`
	CanonicalTarget    compiledTarget   `json:"canonicalTarget"`
	FileName           string           `json:"fileName"`
	Tags               []string         `json:"tags"`
	Disabled           bool             `json:"disabled"`
	Query              string           `json:"query"`
	IncrementalQuery   string           `json:"incrementalQuery"`
	PreOps             []string         `json:"preOps"`
	PostOps            []string         `json:"postOps"`
	IncrementalPreOps  []string         `json:"incrementalPreOps"`
	IncrementalPostOps []string         `json:"incrementalPostOps"`
	Queries            []string         `json:"queries"`
	HasOutput          bool             `json:"hasOutput"`
	DependencyTargets  []compiledTarget `json:"dependencyTargets"`
	ParentAction       *compiledTarget  `json:"parentAction"`
	NotebookContents   string           `json:"notebookContents"`
	GraphBody          string           `json:"graphBody"`
	UniqueKey          []string         `json:"uniqueKey"`
	OnSchemaChange     string           `json:"onSchemaChange"`
	// Description is a property graph's; other actions keep theirs in
	// ActionDescriptor.
	Description string `json:"description"`
	// Nodes and Edges are a property graph's tables.
	Nodes            []compiledGraphTable `json:"nodes"`
	Edges            []compiledGraphTable `json:"edges"`
	ActionDescriptor struct {
		Description string `json:"description"`
		Columns     []struct {
			Path        []string `json:"path"`
			Description string   `json:"description"`
		} `json:"columns"`
	} `json:"actionDescriptor"`
	Bigquery struct {
		PartitionBy string `json:"partitionBy"`
	} `json:"bigquery"`
}

// compiledGraphTable is a property graph's node or edge table. A node with
// no labels has PROPERTIES ALL COLUMNS; an edge has NO PROPERTIES.
type compiledGraphTable struct {
	Name       string         `json:"name"`
	DataSource compiledTarget `json:"dataSource"`
	Keys       []string       `json:"keys"`
	Labels     []struct {
		Fields []struct {
			Name       string `json:"name"`
			Expression string `json:"expression"`
		} `json:"fields"`
		ImportAll bool `json:"importAll"`
	} `json:"labels"`
	Source      *compiledGraphEnd `json:"source"`
	Destination *compiledGraphEnd `json:"destination"`
}

// compiledGraphEnd is the node an edge starts or ends at, and the edge
// table's columns that point at it.
type compiledGraphEnd struct {
	Node        string   `json:"node"`
	EdgeColumns []string `json:"edgeColumns"`
}

type compiledJSON struct {
	Tables         []compiledAction `json:"tables"`
	Operations     []compiledAction `json:"operations"`
	Assertions     []compiledAction `json:"assertions"`
	Declarations   []compiledAction `json:"declarations"`
	Notebooks      []compiledAction `json:"notebooks"`
	PropertyGraphs []compiledAction `json:"propertyGraphs"`
	GraphErrors    struct {
		CompilationErrors []struct {
			FileName string `json:"fileName"`
			Message  string `json:"message"`
			Stack    string `json:"stack"`
		} `json:"compilationErrors"`
	} `json:"graphErrors"`
	ProjectConfig struct {
		DefaultDatabase string `json:"defaultDatabase"`
		DefaultLocation string `json:"defaultLocation"`
		DefaultSchema   string `json:"defaultSchema"`
		TablePrefix     string `json:"tablePrefix"`
		SchemaSuffix    string `json:"schemaSuffix"`
		DatabaseSuffix  string `json:"databaseSuffix"`
	} `json:"projectConfig"`
	DataformCoreVersion string `json:"dataformCoreVersion"`
}
