package conform

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const contractPath = "../../../../contracts/CONTRACT_LOG_SCHEMA.md"

var backticked = regexp.MustCompile("`([^`]+)`")

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

func readContract(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	return string(b)
}

func TestRequiredKeysMatchContract(t *testing.T) {
	rows := tableUnder(t, readContract(t), "## 2. Line structure (project-owned, every line)")
	keyCol := columnIndex(t, rows[0], "JSON key")
	reqCol := columnIndex(t, rows[0], "Required")

	var wantRequired []string
	for _, r := range rows[1:] {
		if r[reqCol] == "yes" {
			for _, m := range backticked.FindAllStringSubmatch(r[keyCol], -1) {
				wantRequired = append(wantRequired, m[1])
			}
		}
	}

	sort.Strings(wantRequired)
	gotRequired := make([]string, len(RequiredKeys))
	copy(gotRequired, RequiredKeys)
	sort.Strings(gotRequired)

	if len(wantRequired) != len(gotRequired) {
		t.Fatalf("Required keys drifted: contract has %v, conform has %v", wantRequired, gotRequired)
	}
	for i := range wantRequired {
		if wantRequired[i] != gotRequired[i] {
			t.Fatalf("Required key mismatch at %d: want %q, got %q", i, wantRequired[i], gotRequired[i])
		}
	}
}

func TestLevelsMatchContract(t *testing.T) {
	doc := readContract(t)
	rows := tableUnder(t, doc, "## 2. Line structure (project-owned, every line)")
	keyCol := columnIndex(t, rows[0], "JSON key")
	valCol := columnIndex(t, rows[0], "Values")

	var levelCell string
	for _, r := range rows[1:] {
		if strings.Contains(r[keyCol], "`level`") {
			levelCell = r[valCol]
			break
		}
	}
	if levelCell == "" {
		t.Fatalf("level row not found in contract table §2")
	}

	for _, lvl := range []string{"debug", "info", "warn", "error", "fatal"} {
		if _, ok := ValidLevels[lvl]; !ok {
			t.Errorf("ValidLevels missing standard level %q", lvl)
		}
	}
}

func TestCrashSignaturesMatchContract(t *testing.T) {
	rows := tableUnder(t, readContract(t), "### Runtime crash signatures")
	col := columnIndex(t, rows[0], "Line starts with")
	for _, r := range rows[1:] {
		for _, m := range backticked.FindAllStringSubmatch(r[col], -1) {
			sig := m[1]
			cls := Classify(sig + " test traceback details")
			if cls != ClassCrashGo && cls != ClassCrashPy && cls != ClassCrashJVM {
				t.Errorf("Classify failed to recognize signature %q: got %v", sig, cls)
			}
		}
	}
}
