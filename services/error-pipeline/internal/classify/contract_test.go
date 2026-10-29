package classify

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const contractPath = "../../../../contracts/CONTRACT_LOG_SCHEMA.md"

var backticked = regexp.MustCompile("`([^`]+)`")

// tableUnder returns the cells of the first markdown table after the given
// heading line, header row first, separator row removed.
func tableUnder(t *testing.T, doc, heading string) [][]string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	i := 0
	for ; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == heading {
			break
		}
	}
	if i == len(lines) {
		t.Fatalf("contract drift: heading %q not found in %s", heading, contractPath)
	}
	i++
	for ; i < len(lines) && !strings.HasPrefix(lines[i], "|"); i++ {
		if strings.HasPrefix(lines[i], "#") {
			t.Fatalf("contract drift: no table under %q in %s", heading, contractPath)
		}
	}
	var rows [][]string
	for ; i < len(lines) && strings.HasPrefix(lines[i], "|"); i++ {
		cells := strings.Split(strings.Trim(strings.TrimSpace(lines[i]), "|"), "|")
		for j := range cells {
			cells[j] = strings.TrimSpace(cells[j])
		}
		if strings.Trim(cells[0], "-: ") == "" {
			continue // separator row
		}
		rows = append(rows, cells)
	}
	if len(rows) < 2 {
		t.Fatalf("contract drift: table under %q has no data rows", heading)
	}
	return rows
}

func columnIndex(t *testing.T, header []string, name string) int {
	t.Helper()
	for i, h := range header {
		if h == name {
			return i
		}
	}
	t.Fatalf("contract drift: column %q not found in header %v", name, header)
	return -1
}

func diff(want, got []string) (missing, extra []string) {
	w, g := map[string]bool{}, map[string]bool{}
	for _, s := range want {
		w[s] = true
	}
	for _, s := range got {
		g[s] = true
	}
	for s := range w {
		if !g[s] {
			missing = append(missing, s)
		}
	}
	for s := range g {
		if !w[s] {
			extra = append(extra, s)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return
}

func readContract(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	return string(b)
}

func TestCrashSignaturesMatchContract(t *testing.T) {
	rows := tableUnder(t, readContract(t), "### Runtime crash signatures")
	col := columnIndex(t, rows[0], "Line starts with")
	var fromContract []string
	for _, r := range rows[1:] {
		for _, m := range backticked.FindAllStringSubmatch(r[col], -1) {
			fromContract = append(fromContract, m[1])
		}
	}
	missing, extra := diff(fromContract, CrashSignatures)
	if len(missing) > 0 || len(extra) > 0 || len(fromContract) != len(CrashSignatures) {
		t.Fatalf("CrashSignatures drifted from %s section 4: in contract but not in code %q; in code but not in contract %q",
			contractPath, missing, extra)
	}
}

func TestNginxLevelsMatchContract(t *testing.T) {
	rows := tableUnder(t, readContract(t), "### Third-party server formats")
	col := columnIndex(t, rows[0], "Error log")
	var cell string
	for _, r := range rows[1:] {
		if r[0] == "nginx" {
			cell = r[col]
		}
	}
	if cell == "" {
		t.Fatalf("contract drift: no nginx row in third-party server table")
	}
	fromContract := map[string]string{}
	var pending []string
	for _, m := range backticked.FindAllStringSubmatch(cell, -1) {
		tok := m[1]
		if strings.HasPrefix(tok, "[") && strings.HasSuffix(tok, "]") {
			pending = append(pending, strings.Trim(tok, "[]"))
			continue
		}
		for _, p := range pending {
			fromContract[p] = tok
		}
		pending = nil
	}
	if len(pending) > 0 {
		t.Fatalf("contract drift: nginx levels %q have no level mapping in cell %q", pending, cell)
	}
	var problems []string
	for lvl, want := range fromContract {
		if got, ok := NginxLevels[lvl]; !ok {
			problems = append(problems, "contract maps ["+lvl+"] to "+want+" but NginxLevels lacks it")
		} else if got != want {
			problems = append(problems, "["+lvl+"]: contract says "+want+", NginxLevels says "+got)
		}
	}
	for lvl := range NginxLevels {
		if _, ok := fromContract[lvl]; !ok {
			problems = append(problems, "NginxLevels has ["+lvl+"] which the contract does not list")
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("NginxLevels drifted from %s section 4: %s", contractPath, strings.Join(problems, "; "))
	}
}
