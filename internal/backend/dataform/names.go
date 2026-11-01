package dataform

// Names are what a compile or run adds to every name the project builds:
// the CLI's --table-prefix, --schema-suffix and --database-suffix. Dataform
// joins each with an underscore, so a schema suffix "ashish" builds into
// dataset_ashish. Declarations keep their names. "" leaves the
// workflow settings' namePrefix, datasetSuffix or projectSuffix in force.
type Names struct {
	TablePrefix    string `json:"tablePrefix,omitempty"`
	SchemaSuffix   string `json:"schemaSuffix,omitempty"`
	DatabaseSuffix string `json:"databaseSuffix,omitempty"`
}

// IsZero says n adds nothing to what the workflow settings say.
func (n Names) IsZero() bool { return n == Names{} }

// Args are n as the CLI's options, in the order its help lists them.
func (n Names) Args() []string {
	var out []string
	if n.DatabaseSuffix != "" {
		out = append(out, "--database-suffix="+n.DatabaseSuffix)
	}
	if n.SchemaSuffix != "" {
		out = append(out, "--schema-suffix="+n.SchemaSuffix)
	}
	if n.TablePrefix != "" {
		out = append(out, "--table-prefix="+n.TablePrefix)
	}
	return out
}

// settingsKeys are the workflow settings' names of xf's keys.
var settingsKeys = map[string]string{
	"tablePrefix":    "namePrefix",
	"schemaSuffix":   "datasetSuffix",
	"databaseSuffix": "projectSuffix",
}

// SettingsKey is the workflow_settings.yaml key that sets what the xf
// config key does: namePrefix for tablePrefix, datasetSuffix for
// schemaSuffix, projectSuffix for databaseSuffix; "" for any other key.
func SettingsKey(key string) string { return settingsKeys[key] }
