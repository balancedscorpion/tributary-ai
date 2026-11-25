package tools

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ColumnType describes the inferred data type of a CSV column.
type ColumnType string

const (
	ColTypeString  ColumnType = "string"
	ColTypeNumber  ColumnType = "number"
	ColTypeDate    ColumnType = "date"
	ColTypeBoolean ColumnType = "boolean"
)

// ColumnProfile holds per-column profiling statistics.
type ColumnProfile struct {
	Name         string     `json:"name"`
	Type         ColumnType `json:"type"`
	NullCount    int64      `json:"null_count"`
	Min          any        `json:"min,omitempty"`
	Max          any        `json:"max,omitempty"`
	Mean         *float64   `json:"mean,omitempty"`
	Median       *float64   `json:"median,omitempty"`
	Stddev       *float64   `json:"stddev,omitempty"`
	Cardinality  any        `json:"cardinality,omitempty"` // int64 or ">10000"
	SampleValues []string   `json:"sample_values,omitempty"`
}

// DatasetProfile is the top-level result of profiling a CSV file.
type DatasetProfile struct {
	Path     string          `json:"path"`
	RowCount int64           `json:"row_count"`
	Columns  []ColumnProfile `json:"columns"`
}

// newCSVReader creates a csv.Reader with common forgiving settings:
// flexible field count, lazy quotes, and BOM stripping on the first read.
func newCSVReader(r io.Reader, delim rune) *csv.Reader {
	cr := csv.NewReader(r)
	cr.Comma = delim
	cr.FieldsPerRecord = -1 // flexible field count
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	return cr
}

// stripBOM removes a UTF-8 BOM from the beginning of data if present.
func stripBOM(data []byte) []byte {
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return data[3:]
	}
	return data
}

// detectDelimiter scans the first 4KB of r to pick the most consistent
// delimiter from {, \t ; |}. Returns ',' as default. Requires r to be seekable.
func detectDelimiter(r io.ReadSeeker) rune {
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	buf = stripBOM(buf[:n])
	r.Seek(0, io.SeekStart)

	if n == 0 {
		return ','
	}

	candidates := []rune{',', '\t', ';', '|'}
	type result struct {
		delim rune
		score float64
	}
	var results []result

	for _, d := range candidates {
		cr := csv.NewReader(bytes.NewReader(buf))
		cr.Comma = d
		cr.LazyQuotes = true
		cr.FieldsPerRecord = -1

		var counts []int
		for i := 0; i < 20; i++ {
			rec, err := cr.Read()
			if err != nil {
				break
			}
			counts = append(counts, len(rec))
		}
		if len(counts) < 2 {
			continue
		}

		// Score: prefer delimiters where lines have the same field count and > 1 field.
		first := counts[0]
		if first <= 1 {
			continue
		}
		consistent := 0
		for _, c := range counts[1:] {
			if c == first {
				consistent++
			}
		}
		score := float64(consistent) / float64(len(counts)-1) * float64(first)
		results = append(results, result{d, score})
	}

	if len(results) == 0 {
		return ','
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})
	return results[0].delim
}

var datePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`),
	regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}`),
	regexp.MustCompile(`^\d{2}/\d{2}/\d{4}$`),
	regexp.MustCompile(`^\d{2}-\d{2}-\d{4}$`),
}

// inferColumnType looks at sampled non-empty values and infers the best type.
// Priority: boolean → number → date → string.
func inferColumnType(values []string) ColumnType {
	if len(values) == 0 {
		return ColTypeString
	}

	allBool, allNum, allDate := true, true, true
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}

		// Boolean check
		low := strings.ToLower(v)
		if allBool && low != "true" && low != "false" && low != "yes" && low != "no" && low != "1" && low != "0" {
			allBool = false
		}

		// Number check
		if allNum {
			if _, err := parseNumber(v); err != nil {
				allNum = false
			}
		}

		// Date check
		if allDate {
			matched := false
			for _, p := range datePatterns {
				if p.MatchString(v) {
					matched = true
					break
				}
			}
			if !matched {
				allDate = false
			}
		}
	}

	if allBool {
		return ColTypeBoolean
	}
	if allNum {
		return ColTypeNumber
	}
	if allDate {
		return ColTypeDate
	}
	return ColTypeString
}

// parseNumber strips common formatting ($, commas, %) and parses a float.
func parseNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimPrefix(s, "$")
	s = strings.TrimSuffix(s, "%")
	return strconv.ParseFloat(s, 64)
}

// welford tracks running mean/variance using Welford's online algorithm.
type welford struct {
	n    int64
	mean float64
	m2   float64
	min  float64
	max  float64
}

func (w *welford) update(x float64) {
	w.n++
	if w.n == 1 {
		w.min = x
		w.max = x
	} else {
		if x < w.min {
			w.min = x
		}
		if x > w.max {
			w.max = x
		}
	}
	delta := x - w.mean
	w.mean += delta / float64(w.n)
	delta2 := x - w.mean
	w.m2 += delta * delta2
}

func (w *welford) variance() float64 {
	if w.n < 2 {
		return 0
	}
	return w.m2 / float64(w.n-1)
}

func (w *welford) stddev() float64 {
	return math.Sqrt(w.variance())
}

const (
	reservoirK       = 10000
	cardinalityCap   = 10001
	ctxCheckInterval = 10000
	maxSampleDisplay = 5
	maxGroupCount    = 1000
)

// profileDataset performs a single-pass streaming profile of a CSV file.
func profileDataset(ctx context.Context, f io.ReadSeeker, filePath string) (*DatasetProfile, error) {
	delim := detectDelimiter(f)
	f.Seek(0, io.SeekStart)

	// Re-read with BOM stripping
	all, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}
	all = stripBOM(all)

	cr := newCSVReader(bytes.NewReader(all), delim)

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading CSV header: %w", err)
	}
	numCols := len(header)

	// Per-column accumulators
	type colAcc struct {
		nullCount   int64
		welf        welford
		reservoir   []float64 // for median
		uniques     map[string]struct{}
		cardCapped  bool
		samples     []string
		typeSamples []string // for type inference (up to 100)
		minStr      string
		maxStr      string
		hasStr      bool
	}

	cols := make([]colAcc, numCols)
	for i := range cols {
		cols[i].uniques = make(map[string]struct{})
	}

	var rowCount int64
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		if rowCount%ctxCheckInterval == 0 && rowCount > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}

		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Skip malformed rows
			continue
		}
		rowCount++

		for i := 0; i < numCols; i++ {
			var val string
			if i < len(record) {
				val = strings.TrimSpace(record[i])
			}

			c := &cols[i]

			if val == "" {
				c.nullCount++
				continue
			}

			// Type inference sampling
			if len(c.typeSamples) < 100 {
				c.typeSamples = append(c.typeSamples, val)
			}

			// Sample values for display
			if len(c.samples) < maxSampleDisplay {
				c.samples = append(c.samples, val)
			}

			// Cardinality tracking
			if !c.cardCapped {
				c.uniques[val] = struct{}{}
				if len(c.uniques) > cardinalityCap {
					c.cardCapped = true
				}
			}

			// Numeric stats
			if num, err := parseNumber(val); err == nil {
				c.welf.update(num)
				// Reservoir sampling for median
				if int64(len(c.reservoir)) < reservoirK {
					c.reservoir = append(c.reservoir, num)
				} else {
					j := rng.Int63n(c.welf.n)
					if j < reservoirK {
						c.reservoir[j] = num
					}
				}
			}

			// String min/max
			if !c.hasStr {
				c.minStr = val
				c.maxStr = val
				c.hasStr = true
			} else {
				if val < c.minStr {
					c.minStr = val
				}
				if val > c.maxStr {
					c.maxStr = val
				}
			}
		}
	}

	// Build column profiles
	profiles := make([]ColumnProfile, numCols)
	for i := 0; i < numCols; i++ {
		c := &cols[i]
		colType := inferColumnType(c.typeSamples)

		p := ColumnProfile{
			Name:         header[i],
			Type:         colType,
			NullCount:    c.nullCount,
			SampleValues: c.samples,
		}

		if colType == ColTypeNumber && c.welf.n > 0 {
			p.Min = roundNum(c.welf.min)
			p.Max = roundNum(c.welf.max)
			mean := roundNum(c.welf.mean)
			p.Mean = &mean
			sd := roundNum(c.welf.stddev())
			p.Stddev = &sd

			// Compute median from reservoir
			if len(c.reservoir) > 0 {
				sort.Float64s(c.reservoir)
				mid := len(c.reservoir) / 2
				var med float64
				if len(c.reservoir)%2 == 0 {
					med = (c.reservoir[mid-1] + c.reservoir[mid]) / 2
				} else {
					med = c.reservoir[mid]
				}
				med = roundNum(med)
				p.Median = &med
			}
		} else if c.hasStr {
			p.Min = c.minStr
			p.Max = c.maxStr
		}

		if c.cardCapped {
			p.Cardinality = ">10000"
		} else {
			p.Cardinality = int64(len(c.uniques))
		}

		profiles[i] = p
	}

	return &DatasetProfile{
		Path:     filePath,
		RowCount: rowCount,
		Columns:  profiles,
	}, nil
}

// roundNum rounds to 4 decimal places to keep output concise.
func roundNum(f float64) float64 {
	return math.Round(f*10000) / 10000
}

// reservoirSample reads all rows and returns up to k random rows using Algorithm R.
func reservoirSample(ctx context.Context, reader *csv.Reader, k int) ([][]string, int64, error) {
	reservoir := make([][]string, 0, k)
	var total int64
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	for {
		if total%ctxCheckInterval == 0 && total > 0 {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			default:
			}
		}

		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		total++

		if int(total) <= k {
			row := make([]string, len(record))
			copy(row, record)
			reservoir = append(reservoir, row)
		} else {
			j := rng.Int63n(total)
			if j < int64(k) {
				row := make([]string, len(record))
				copy(row, record)
				reservoir[j] = row
			}
		}
	}

	return reservoir, total, nil
}

// evalFilter evaluates a single filter predicate against a cell value.
func evalFilter(value string, operator string, compareValue string, colType ColumnType) bool {
	value = strings.TrimSpace(value)

	switch operator {
	case "is_null":
		return value == ""
	case "not_null":
		return value != ""
	}

	if value == "" {
		return false
	}

	switch operator {
	case "eq":
		if colType == ColTypeNumber {
			a, err1 := parseNumber(value)
			b, err2 := parseNumber(compareValue)
			if err1 == nil && err2 == nil {
				return a == b
			}
		}
		return strings.EqualFold(value, compareValue)
	case "neq":
		if colType == ColTypeNumber {
			a, err1 := parseNumber(value)
			b, err2 := parseNumber(compareValue)
			if err1 == nil && err2 == nil {
				return a != b
			}
		}
		return !strings.EqualFold(value, compareValue)
	case "gt":
		a, err1 := parseNumber(value)
		b, err2 := parseNumber(compareValue)
		if err1 == nil && err2 == nil {
			return a > b
		}
		return value > compareValue
	case "lt":
		a, err1 := parseNumber(value)
		b, err2 := parseNumber(compareValue)
		if err1 == nil && err2 == nil {
			return a < b
		}
		return value < compareValue
	case "gte":
		a, err1 := parseNumber(value)
		b, err2 := parseNumber(compareValue)
		if err1 == nil && err2 == nil {
			return a >= b
		}
		return value >= compareValue
	case "lte":
		a, err1 := parseNumber(value)
		b, err2 := parseNumber(compareValue)
		if err1 == nil && err2 == nil {
			return a <= b
		}
		return value <= compareValue
	case "contains":
		return strings.Contains(strings.ToLower(value), strings.ToLower(compareValue))
	case "starts_with":
		return strings.HasPrefix(strings.ToLower(value), strings.ToLower(compareValue))
	case "ends_with":
		return strings.HasSuffix(strings.ToLower(value), strings.ToLower(compareValue))
	default:
		return false
	}
}

// MetricSpec describes a single aggregation metric.
type MetricSpec struct {
	Column   string `json:"column"`
	Function string `json:"function"` // count, sum, avg, min, max, count_distinct
}

// groupAccumulator holds per-group aggregation state.
type groupAccumulator struct {
	metrics []metricAcc
}

type metricAcc struct {
	count   int64
	sum     float64
	min     float64
	max     float64
	hasVal  bool
	uniques map[string]struct{}
}

// aggregateCSV performs single-pass grouped aggregation on a CSV file.
func aggregateCSV(ctx context.Context, f io.ReadSeeker, groupBy []string, metrics []MetricSpec) ([]map[string]any, error) {
	delim := detectDelimiter(f)
	f.Seek(0, io.SeekStart)

	all, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}
	all = stripBOM(all)

	cr := newCSVReader(bytes.NewReader(all), delim)

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading CSV header: %w", err)
	}

	// Build column index
	colIdx := make(map[string]int, len(header))
	for i, h := range header {
		colIdx[strings.TrimSpace(h)] = i
	}

	// Validate group_by columns
	groupIndices := make([]int, len(groupBy))
	for i, g := range groupBy {
		idx, ok := colIdx[g]
		if !ok {
			return nil, fmt.Errorf("group_by column %q not found", g)
		}
		groupIndices[i] = idx
	}

	// Validate metric columns
	metricIndices := make([]int, len(metrics))
	for i, m := range metrics {
		idx, ok := colIdx[m.Column]
		if !ok {
			return nil, fmt.Errorf("metric column %q not found", m.Column)
		}
		metricIndices[i] = idx
	}

	groups := make(map[string]*groupAccumulator)
	groupOrder := make([]string, 0) // preserve insertion order
	groupCapped := false
	var rowCount int64

	for {
		if rowCount%ctxCheckInterval == 0 && rowCount > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}

		record, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}
		rowCount++

		// Build group key
		var keyParts []string
		for _, gi := range groupIndices {
			val := ""
			if gi < len(record) {
				val = strings.TrimSpace(record[gi])
			}
			keyParts = append(keyParts, val)
		}
		key := strings.Join(keyParts, "\x00")

		acc, exists := groups[key]
		if !exists {
			if len(groups) >= maxGroupCount {
				groupCapped = true
				continue
			}
			acc = &groupAccumulator{
				metrics: make([]metricAcc, len(metrics)),
			}
			for i, m := range metrics {
				if m.Function == "count_distinct" {
					acc.metrics[i].uniques = make(map[string]struct{})
				}
			}
			groups[key] = acc
			groupOrder = append(groupOrder, key)
		}

		// Update accumulators
		for i, m := range metrics {
			val := ""
			if metricIndices[i] < len(record) {
				val = strings.TrimSpace(record[metricIndices[i]])
			}
			ma := &acc.metrics[i]

			switch m.Function {
			case "count":
				if val != "" {
					ma.count++
				}
			case "sum", "avg":
				if num, err := parseNumber(val); err == nil {
					ma.count++
					ma.sum += num
				}
			case "min":
				if num, err := parseNumber(val); err == nil {
					if !ma.hasVal || num < ma.min {
						ma.min = num
						ma.hasVal = true
					}
					ma.count++
				}
			case "max":
				if num, err := parseNumber(val); err == nil {
					if !ma.hasVal || num > ma.max {
						ma.max = num
						ma.hasVal = true
					}
					ma.count++
				}
			case "count_distinct":
				if val != "" {
					ma.uniques[val] = struct{}{}
					ma.count++
				}
			}
		}
	}

	// Build output
	results := make([]map[string]any, 0, len(groupOrder))
	for _, key := range groupOrder {
		acc := groups[key]
		row := make(map[string]any)

		// Add group-by values
		if len(groupBy) > 0 {
			parts := strings.SplitN(key, "\x00", len(groupBy))
			for i, g := range groupBy {
				if i < len(parts) {
					row[g] = parts[i]
				} else {
					row[g] = ""
				}
			}
		}

		// Add metric values
		for i, m := range metrics {
			ma := acc.metrics[i]
			label := m.Function + "(" + m.Column + ")"

			switch m.Function {
			case "count":
				row[label] = ma.count
			case "sum":
				row[label] = roundNum(ma.sum)
			case "avg":
				if ma.count > 0 {
					row[label] = roundNum(ma.sum / float64(ma.count))
				} else {
					row[label] = nil
				}
			case "min":
				if ma.hasVal {
					row[label] = roundNum(ma.min)
				} else {
					row[label] = nil
				}
			case "max":
				if ma.hasVal {
					row[label] = roundNum(ma.max)
				} else {
					row[label] = nil
				}
			case "count_distinct":
				row[label] = int64(len(ma.uniques))
			}
		}

		results = append(results, row)
	}

	if groupCapped && len(results) > 0 {
		results[len(results)-1]["_note"] = fmt.Sprintf("Group count capped at %d", maxGroupCount)
	}

	return results, nil
}

// rowToMap converts a CSV record to a map using the header as keys.
func rowToMap(header []string, record []string) map[string]any {
	m := make(map[string]any, len(header))
	for i, h := range header {
		if i < len(record) {
			m[h] = record[i]
		} else {
			m[h] = ""
		}
	}
	return m
}

// isValidUTF8File does a quick check that the content is valid UTF-8.
func isValidUTF8File(data []byte) bool {
	return utf8.Valid(data)
}
