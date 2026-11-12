package dataform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// WorkflowNames are the names workflow_settings.yaml gives a compile that
// no option changes: the project and dataset it builds in, and what it
// adds to them. The prod compile uses them where prodCompilerOptions say
// nothing.
type WorkflowNames struct {
	Project        string `json:"project"`
	Dataset        string `json:"dataset"`
	Prefix         string `json:"prefix"`
	SchemaSuffix   string `json:"schemaSuffix"`
	DatabaseSuffix string `json:"databaseSuffix"`
}

// ReadWorkflowNames reads the WorkflowNames of the project at root from
// its workflow_settings.yaml: top-level keys only, line by line, as the
// file has them. A key it doesn't find is "".
func ReadWorkflowNames(root string) (WorkflowNames, error) {
	data, err := os.ReadFile(filepath.Join(root, SettingsFile))
	if err != nil {
		return WorkflowNames{}, err
	}
	keys := map[string]string{}
	for _, m := range workflowKeyRE.FindAllStringSubmatch(string(data), -1) {
		keys[m[1]] = workflowValue(m[2])
	}
	return WorkflowNames{Project: keys["defaultProject"], Dataset: keys["defaultDataset"], Prefix: keys["namePrefix"],
		SchemaSuffix: keys["datasetSuffix"], DatabaseSuffix: keys["projectSuffix"]}, nil
}

// ReadWorkflowSetting is one top-level key of the project's
// workflow_settings.yaml, "" when it has none: "defaultLocation", say.
func ReadWorkflowSetting(root, key string) string {
	data, err := os.ReadFile(filepath.Join(root, SettingsFile))
	if err != nil {
		return ""
	}
	for _, m := range workflowKeyRE.FindAllStringSubmatch(string(data), -1) {
		if m[1] == key {
			return workflowValue(m[2])
		}
	}
	return ""
}

var workflowKeyRE = regexp.MustCompile(`(?m)^([A-Za-z]+):[ \t]*(.*?)[ \t]*\r?$`)

// workflowValue is a YAML scalar without its quotes or a comment after it.
func workflowValue(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : 1+end]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	if strings.HasPrefix(v, "#") {
		return ""
	}
	return strings.TrimSpace(v)
}
