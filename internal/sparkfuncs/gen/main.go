// Command gen writes internal/sparkfuncs/spark-functions.json from Apache
// Spark's built-in function reference (Apache License 2.0), in the format of
// bqfuncs' catalog. `just spark-functions [version]` runs it.
//
// Each function links to its page in the Databricks SQL reference when
// that page exists, else to Spark's own.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const databricksDocs = "https://docs.databricks.com/aws/en/sql/language-manual/functions/"

var groupLink = regexp.MustCompile(`href="([a-z-]+-functions)/"`)

func main() {
	version := flag.String("version", "4.3.0", "the Spark release whose reference is read")
	out := flag.String("out", "internal/sparkfuncs/spark-functions.json", "the file to write")
	dir := flag.String("dir", "", "read the pages from this folder (index.html, <group>.html) instead of spark.apache.org")
	links := flag.Bool("links", true, "link each function to its Databricks page when it has one")
	flag.Parse()

	base := "https://spark.apache.org/docs/" + *version + "/api/sql/"
	read := func(name string) string {
		if *dir != "" {
			b, err := os.ReadFile(filepath.Join(*dir, name+".html"))
			if err != nil {
				log.Fatal(err)
			}
			return string(b)
		}
		u := base
		if name != "index" {
			u += name + "/"
		}
		b, err := fetch(u)
		if err != nil {
			log.Fatal(err)
		}
		return string(b)
	}

	var groups []string
	for _, m := range groupLink.FindAllStringSubmatch(read("index"), -1) {
		if !slices.Contains(groups, m[1]) {
			groups = append(groups, m[1])
		}
	}
	if len(groups) == 0 {
		log.Fatal("no function groups on the index page")
	}
	slices.Sort(groups)

	byName := map[string]int{}
	var all []Function
	for _, g := range groups {
		for _, f := range parsePage(read(g), g) {
			f.URL = base + g + "/#" + strings.ToLower(f.Name)
			if i, ok := byName[f.Name]; ok {
				all[i].Aggregate = all[i].Aggregate || f.Aggregate
				continue
			}
			byName[f.Name] = len(all)
			all = append(all, f)
		}
	}
	if *links {
		linkDatabricks(all)
	}
	slices.SortFunc(all, func(a, b Function) int { return strings.Compare(a.Name, b.Name) })

	file := struct {
		Comment   string              `json:"$comment"`
		ValueSets map[string][]string `json:"valueSets"`
		Functions []Function          `json:"functions"`
	}{
		Comment:   "Written by `just spark-functions` from Apache Spark " + *version + "'s built-in function reference (" + base + "), Apache License 2.0: see LICENSE. Don't edit it by hand: overrides.json is xf's own.",
		ValueSets: map[string][]string{},
		Functions: all,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(file); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d functions from %d groups → %s\n", len(all), len(groups), *out)
}

var client = &http.Client{Timeout: 30 * time.Second}

func fetch(u string) ([]byte, error) {
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// linkDatabricks points each function at its Databricks page, where there
// is one, a few requests at a time.
func linkDatabricks(all []Function) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	missing := 0
	var mu sync.Mutex
	for i := range all {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			u := databricksDocs + strings.ToLower(all[i].Name)
			resp, err := client.Head(u)
			if err == nil {
				resp.Body.Close()
			}
			if err == nil && resp.StatusCode == http.StatusOK {
				all[i].URL = u
				return
			}
			mu.Lock()
			missing++
			mu.Unlock()
		})
	}
	wg.Wait()
	fmt.Printf("%d functions have no Databricks page; they link to Spark's\n", missing)
}
