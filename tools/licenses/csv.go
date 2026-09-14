package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
)

// renderCSV writes the inventory in canonical form: the fixed header line,
// then one RFC 4180 record per row, in the order given (rows are sorted by
// buildRows, so the output is fully deterministic and can be compared byte
// for byte with the committed file).
func renderCSV(rows []row) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(csvHeader)
	buf.WriteByte('\n')
	w := csv.NewWriter(&buf)
	for _, r := range rows {
		if err := w.Write([]string{r.Component, r.Origin, r.License, r.Copyright}); err != nil {
			return nil, fmt.Errorf("render CSV record for %s: %w", r.Component, err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("render CSV: %w", err)
	}
	return buf.Bytes(), nil
}
