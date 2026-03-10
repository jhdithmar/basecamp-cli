// Skill Test Runner — tests whether an LLM can translate natural language
// queries into the correct CLI commands using only SKILL.md as guidance.
//
// Each test runs N times (default 3). A test only passes if it passes all runs.
// This accounts for LLM non-determinism and surfaces ambiguous skill docs.
//
// Usage:
//
//	go run ./e2e/skill-test-runner-go                      # Run all tests (3x each)
//	go run ./e2e/skill-test-runner-go --ids 1,2,3          # Run specific test IDs
//	go run ./e2e/skill-test-runner-go --section 2          # Run a specific section
//	go run ./e2e/skill-test-runner-go --model sonnet       # Use a specific model
//	go run ./e2e/skill-test-runner-go --runs 1             # Single run (quick check)
//	go run ./e2e/skill-test-runner-go --dry-run            # Show what would run
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TestCase represents a single row from the test matrix CSV.
type TestCase struct {
	ID           int
	Category     string
	Subcategory  string
	CLICommand   string
	NLQuery      string
	TestsSection string
}

// RunResult captures one invocation of claude -p.
type RunResult struct {
	Response string `json:"response"`
	Result   string `json:"result"`
}

// TestResult is a JSONL entry for one test case.
type TestResult struct {
	ID        int         `json:"id"`
	Query     string      `json:"query"`
	Expected  string      `json:"expected"`
	Runs      []RunResult `json:"runs"`
	Passes    int         `json:"passes"`
	TotalRuns int         `json:"total_runs"`
}

// Section ranges map section number to [min, max] ID range (inclusive).
var sectionRanges = map[int][2]int{
	1: {1, 9}, 2: {10, 49}, 3: {50, 69}, 4: {70, 79},
	5: {80, 89}, 6: {90, 99}, 7: {100, 109}, 8: {110, 119},
	9: {120, 129}, 10: {130, 139}, 11: {140, 149}, 12: {150, 159},
	13: {160, 169}, 14: {170, 179}, 15: {180, 189}, 16: {190, 199},
	17: {200, 209}, 18: {210, 229}, 19: {230, 239}, 20: {240, 249},
}

// Compiled regexes for normalization.
var (
	reOutputFlags = regexp.MustCompile(`\s+--(?:json|md|markdown)`)
	reMultiSpace  = regexp.MustCompile(`\s+`)
	reQuoted      = regexp.MustCompile(`"[^"]*"`)
	rePlaceholder = regexp.MustCompile(`<[a-z0-9_-]+>`)
	reNumbers     = regexp.MustCompile(`\d{3,}`)
	reScopeFlags  = regexp.MustCompile(`\s+(?:--in|--project|-p|--list|--column|--card-table)\s+<val>`)
	reAnnotation  = regexp.MustCompile(`\s*\([^)]*\)`)
	reCodeFence   = regexp.MustCompile("(?m)^```[a-z]*$")
	reBacktick    = regexp.MustCompile("^`|`$")
)

func main() {
	model := flag.String("model", "sonnet", "Model to use")
	filterIDs := flag.String("ids", "", "Comma-separated test IDs to run")
	filterSection := flag.Int("section", 0, "Section number to run")
	numRuns := flag.Int("runs", 3, "Number of runs per test")
	timeout := flag.Duration("timeout", 60*time.Second, "Timeout per claude invocation")
	dryRun := flag.Bool("dry-run", false, "Show tests without running")
	flag.Parse()

	if *numRuns < 1 {
		fmt.Fprintln(os.Stderr, "--runs must be a positive integer")
		os.Exit(1)
	}

	projectRoot := findProjectRoot()
	matrixPath := filepath.Join(projectRoot, "e2e", "skill-test-matrix.csv")
	skillPath := filepath.Join(projectRoot, "skills", "basecamp", "SKILL.md")
	resultsDir := filepath.Join(projectRoot, "e2e", "skill-test-runs")

	skillContent, err := os.ReadFile(skillPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading SKILL.md: %v\n", err)
		os.Exit(1)
	}

	cases, err := loadTestCases(matrixPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading test matrix: %v\n", err)
		os.Exit(1)
	}

	// Build filter set
	idFilter := parseIDFilter(*filterIDs)

	// Filter cases
	var filtered []TestCase
	for _, tc := range cases {
		if len(idFilter) > 0 && !idFilter[tc.ID] {
			continue
		}
		if *filterSection > 0 {
			r, ok := sectionRanges[*filterSection]
			if !ok || tc.ID < r[0] || tc.ID > r[1] {
				continue
			}
		}
		filtered = append(filtered, tc)
	}

	systemPrompt := buildSystemPrompt(string(skillContent))

	_ = os.MkdirAll(resultsDir, 0o750)
	timestamp := time.Now().Format("20060102-150405")
	runFile := filepath.Join(resultsDir, "run-"+timestamp+".jsonl")
	summaryFile := filepath.Join(resultsDir, "run-"+timestamp+"-summary.md")

	jsonlF, err := os.Create(runFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating results file: %v\n", err)
		os.Exit(1)
	}
	defer jsonlF.Close()

	fmt.Println("Skill Test Runner")
	fmt.Println("=================")
	fmt.Printf("Model: %s\n", *model)
	fmt.Printf("Runs per test: %d\n", *numRuns)
	fmt.Printf("Matrix: %s\n", matrixPath)
	fmt.Printf("Results: %s\n", runFile)
	fmt.Println()

	var passAll, flaky, failAll, skipped, total int
	var errors []string

	for _, tc := range filtered {
		cmdType := classifyCommand(tc.CLICommand)
		if cmdType == "skip" {
			skipped++
			continue
		}
		total++

		if *dryRun {
			fmt.Printf("[%d] (%s) %s\n", tc.ID, cmdType, tc.NLQuery)
			fmt.Printf("  Expected: %s\n\n", tc.CLICommand)
			continue
		}

		fmt.Printf("[%d] %-55s ", tc.ID, tc.NLQuery)

		var runs []RunResult
		passes := 0
		for r := 0; r < *numRuns; r++ {
			actual := runQuery(tc.NLQuery, *model, systemPrompt, *timeout)
			level := compare(actual, tc.CLICommand, cmdType)
			runs = append(runs, RunResult{
				Response: truncateResponse(actual),
				Result:   level,
			})
			if level != "FAIL" {
				passes++
			}
		}

		// Score
		results := make([]string, len(runs))
		for i, r := range runs {
			results[i] = r.Result
		}
		resultsStr := strings.Join(results, " ")

		if passes == *numRuns {
			passAll++
			fmt.Printf("%d/%d PASS  (%s)\n", passes, *numRuns, resultsStr)
		} else if passes > 0 {
			flaky++
			fmt.Printf("%d/%d FLAKY (%s)\n", passes, *numRuns, resultsStr)
		} else {
			failAll++
			fmt.Printf("%d/%d FAIL  (%s)\n", passes, *numRuns, resultsStr)
		}

		fmt.Printf("  Expected: %s\n", tc.CLICommand)
		for i, r := range runs {
			fmt.Printf("  Run %d:    %s  [%s]\n", i+1, r.Response, r.Result)
		}

		if passes < *numRuns {
			errors = append(errors, fmt.Sprintf("| %d | %s | `%s` | %d/%d | %s |",
				tc.ID, tc.NLQuery, tc.CLICommand, passes, *numRuns, resultsStr))
		}

		// Write JSONL
		entry := TestResult{
			ID:        tc.ID,
			Query:     tc.NLQuery,
			Expected:  tc.CLICommand,
			Runs:      runs,
			Passes:    passes,
			TotalRuns: *numRuns,
		}
		data, _ := json.Marshal(entry)
		fmt.Fprintf(jsonlF, "%s\n", data)

		fmt.Println()
	}

	if *dryRun {
		fmt.Println("---")
		fmt.Printf("Total: %d tests would run (%d skipped), %d runs each\n", total, skipped, *numRuns)
		return
	}

	// Summary
	pct := 0
	if total > 0 {
		pct = (passAll * 100) / total
	}

	fmt.Println()
	fmt.Println("=================")
	fmt.Printf("Results (%dx each, must pass all):\n", *numRuns)
	fmt.Printf("  Pass:  %d (%d/%d)\n", passAll, *numRuns, *numRuns)
	fmt.Printf("  Flaky: %d (some passed, some failed — skill doc may be ambiguous)\n", flaky)
	fmt.Printf("  Fail:  %d (0/%d)\n", failAll, *numRuns)
	fmt.Printf("  Skip:  %d\n", skipped)
	fmt.Println()
	fmt.Printf("Consistent pass rate: %d%% (%d/%d)\n", pct, passAll, total)
	fmt.Println()

	// Write summary markdown
	writeSummary(summaryFile, *model, *numRuns, total, skipped, passAll, flaky, failAll, pct, errors)

	fmt.Printf("Summary: %s\n", summaryFile)
	fmt.Printf("Details: %s\n", runFile)
}

// loadTestCases reads the CSV matrix, skipping comments and the header.
func loadTestCases(path string) ([]TestCase, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.Comment = '#'
	r.FieldsPerRecord = -1 // Allow variable fields
	r.LazyQuotes = true

	var cases []TestCase
	header := true
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("CSV parse error: %w", err)
		}
		if header {
			header = false
			continue
		}
		if len(record) < 5 {
			continue
		}
		id, err := strconv.Atoi(strings.TrimSpace(record[0]))
		if err != nil {
			continue
		}
		tc := TestCase{
			ID:          id,
			Category:    field(record, 1),
			Subcategory: field(record, 2),
			CLICommand:  field(record, 3),
			NLQuery:     field(record, 4),
		}
		if len(record) > 6 {
			tc.TestsSection = field(record, 6)
		}
		cases = append(cases, tc)
	}
	return cases, nil
}

func field(record []string, i int) string {
	if i < len(record) {
		return strings.TrimSpace(record[i])
	}
	return ""
}

func parseIDFilter(s string) map[int]bool {
	if s == "" {
		return nil
	}
	m := make(map[int]bool)
	for _, part := range strings.Split(s, ",") {
		if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			m[id] = true
		}
	}
	return m
}

func buildSystemPrompt(skillContent string) string {
	return `You are a command translator. Given a natural language query, respond with ONLY the CLI command. Your entire response must be a valid shell command and nothing else.

CRITICAL RULES — violating any of these is a test failure:
- Your ENTIRE response is the command. No words before it. No words after it.
- No backticks. No markdown. No code fences. No explanation. No apologies.
- Do NOT mention tools, access, or capabilities. Just the command.
- Do NOT run the command. Just output it as text.
- If multiple steps needed, one command per line.
- Placeholders for unknowns: <project>, <id>, <recording_id>, <person>, <url>, <list>, <todolist_id>, <column_id>, <question_id>, <folder_id>

DOCUMENTATION:
` + skillContent + `
END DOCUMENTATION`
}

// classifyCommand determines the comparison strategy for a test case.
func classifyCommand(cmd string) string {
	if strings.HasPrefix(cmd, "n/a") {
		return "skip"
	}
	if strings.Contains(cmd, " OR ") || strings.Contains(cmd, " vs ") {
		return "either"
	}
	if strings.Contains(cmd, " + ") || strings.Contains(cmd, "; then") {
		return "multi"
	}
	return "single"
}

// runQuery invokes claude -p and returns the cleaned response.
func runQuery(query, model, systemPrompt string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", "-p", //nolint:gosec // args are from test matrix, not user input
		"Translate to CLI command: "+query,
		"--model", model,
		"--append-system-prompt", systemPrompt,
		"--no-session-persistence",
		"--allowedTools", "",
	)
	// Filter out CLAUDECODE to prevent nested session errors
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			filtered = append(filtered, e)
		}
	}
	cmd.Env = filtered

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "ERROR: claude timed out after " + timeout.String()
		}
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "ERROR: " + errMsg
		}
		return "ERROR: claude command failed"
	}
	return cleanResponse(string(out))
}

// cleanResponse strips markdown fences, backticks, and empty lines.
func cleanResponse(raw string) string {
	// Remove code fences
	cleaned := reCodeFence.ReplaceAllString(raw, "")
	// Process line by line
	var lines []string
	for _, line := range strings.Split(cleaned, "\n") {
		line = strings.TrimSpace(line)
		// Strip leading/trailing backticks
		line = reBacktick.ReplaceAllString(line, "")
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// normalizeLine normalizes a command for fuzzy comparison.
func normalizeLine(s string) string {
	s = strings.TrimSpace(s)
	s = reOutputFlags.ReplaceAllString(s, "")
	s = reMultiSpace.ReplaceAllString(s, " ")
	s = reQuoted.ReplaceAllString(s, "<val>")
	s = rePlaceholder.ReplaceAllString(s, "<val>")
	s = reNumbers.ReplaceAllString(s, "<val>")
	return strings.TrimSpace(s)
}

// extractStem removes scope flags to get the core command structure.
func extractStem(s string) string {
	return strings.TrimSpace(reScopeFlags.ReplaceAllString(s, ""))
}

// stripAnnotations removes parenthetical notes like "(needs --in or config)".
func stripAnnotations(s string) string {
	return strings.TrimSpace(reAnnotation.ReplaceAllString(s, ""))
}

// compare dispatches to the appropriate comparison function.
func compare(actual, expected, cmdType string) string {
	switch cmdType {
	case "single":
		return compareSingle(actual, expected)
	case "multi":
		return compareMulti(actual, expected)
	case "either":
		return compareEither(actual, expected)
	default:
		return "FAIL"
	}
}

// compareSingle compares a single expected command against actual output.
// Checks each line of actual (not just the first) to handle chatty responses.
func compareSingle(actual, expected string) string {
	expected = stripAnnotations(expected)
	expectedTrimmed := strings.TrimSpace(expected)
	normExpected := normalizeLine(expected)
	stemExpected := extractStem(normExpected)

	bestResult := "FAIL"
	for _, line := range strings.Split(actual, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Exact match
		if line == expectedTrimmed {
			return "EXACT"
		}

		normActual := normalizeLine(line)
		if normExpected == normActual {
			bestResult = betterResult(bestResult, "PASS")
			continue
		}

		stemActual := extractStem(normActual)
		if stemExpected == stemActual {
			bestResult = betterResult(bestResult, "MINOR")
		}
	}
	return bestResult
}

// betterResult returns the higher-priority result.
func betterResult(a, b string) string {
	priority := map[string]int{"EXACT": 4, "PASS": 3, "MINOR": 2, "FAIL": 1}
	if priority[b] > priority[a] {
		return b
	}
	return a
}

// compareMulti checks that each expected fragment appears in the actual output.
func compareMulti(actual, expected string) string {
	normActual := normalizeLine(actual)

	// Split on " + " or "; then "
	fragments := strings.Split(expected, " + ")
	if len(fragments) == 1 {
		fragments = strings.Split(expected, "; then")
	}

	for _, frag := range fragments {
		frag = strings.TrimSpace(frag)
		if frag == "" {
			continue
		}
		// Extract key command word (first word after "basecamp")
		normFrag := normalizeLine(frag)
		keyWord := extractKeyWord(normFrag)
		if keyWord == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(normActual), strings.ToLower(keyWord)) {
			return "FAIL"
		}
	}
	return "PASS"
}

// extractKeyWord gets the first subcommand word from a normalized command.
func extractKeyWord(norm string) string {
	norm = strings.TrimPrefix(norm, "basecamp ")
	parts := strings.Fields(norm)
	if len(parts) > 0 {
		return parts[0]
	}
	return ""
}

// compareEither passes if any alternative matches.
func compareEither(actual, expected string) string {
	// Split on " OR " or " vs "
	var alternatives []string
	if strings.Contains(expected, " OR ") {
		alternatives = strings.Split(expected, " OR ")
	} else {
		alternatives = strings.Split(expected, " vs ")
	}

	for _, alt := range alternatives {
		alt = strings.TrimSpace(alt)
		if alt == "" {
			continue
		}
		result := compareSingle(actual, alt)
		if result != "FAIL" {
			return result
		}
	}
	return "FAIL"
}

// truncateResponse returns first 3 lines joined for display.
func truncateResponse(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > 3 {
		lines = lines[:3]
	}
	return strings.Join(lines, " ")
}

// writeSummary writes the markdown summary file.
func writeSummary(path, model string, numRuns, total, skipped, passAll, flaky, failAll, pct int, errors []string) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing summary: %v\n", err)
		return
	}
	defer f.Close()

	timestamp := time.Now().Format("20060102-150405")
	fmt.Fprintf(f, "# Skill Test Run — %s\n\n", timestamp)
	fmt.Fprintf(f, "**Model:** %s\n", model)
	fmt.Fprintf(f, "**Runs per test:** %d\n", numRuns)
	fmt.Fprintf(f, "**Tested:** %d (skipped %d)\n\n", total, skipped)
	fmt.Fprintf(f, "## Results\n\n")
	fmt.Fprintf(f, "- **Pass (%d/%d):** %d\n", numRuns, numRuns, passAll)
	fmt.Fprintf(f, "- **Flaky (partial):** %d\n", flaky)
	fmt.Fprintf(f, "- **Fail (0/%d):** %d\n", numRuns, failAll)
	fmt.Fprintf(f, "- **Consistent pass rate:** %d%%\n\n", pct)

	if len(errors) > 0 {
		fmt.Fprintf(f, "## Failures & Flaky Tests\n\n")
		fmt.Fprintf(f, "| ID | Query | Expected | Score | Run Results |\n")
		fmt.Fprintf(f, "|----|-------|----------|-------|-------------|\n")
		for _, e := range errors {
			fmt.Fprintf(f, "%s\n", e)
		}
	}
}

// findProjectRoot walks up from the executable or working directory to find go.mod.
func findProjectRoot() string {
	// Try from working directory first
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Fallback: relative to the binary
	exe, _ := os.Executable()
	dir = filepath.Dir(exe)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	fmt.Fprintln(os.Stderr, "Error: could not find project root (no go.mod found)")
	os.Exit(1)
	return ""
}
