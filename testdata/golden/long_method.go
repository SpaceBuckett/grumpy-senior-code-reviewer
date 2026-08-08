package golden

import (
	"fmt"
	"strconv"
	"strings"
)

func ProcessOrderReport(lines []string) (string, error) {
	var report strings.Builder
	var total float64
	var errorCount int
	report.WriteString("ORDER REPORT\n")
	for _, line := range lines {
		fields := strings.Split(line, ",")
		if len(fields) != 4 {
			errorCount++
			continue
		}
		id := strings.TrimSpace(fields[0])
		name := strings.TrimSpace(fields[1])
		qtyText := strings.TrimSpace(fields[2])
		priceText := strings.TrimSpace(fields[3])
		if id == "" || name == "" {
			errorCount++
			continue
		}
		qty, err := strconv.Atoi(qtyText)
		if err != nil {
			errorCount++
			continue
		}
		price, err := strconv.ParseFloat(priceText, 64)
		if err != nil {
			errorCount++
			continue
		}
		subtotal := float64(qty) * price
		if qty > 100 {
			if price > 50 {
				subtotal = subtotal * 0.85
			} else {
				subtotal = subtotal * 0.9
			}
		} else if qty > 10 {
			if price > 50 {
				subtotal = subtotal * 0.95
			} else {
				subtotal = subtotal * 0.97
			}
		}
		if strings.HasPrefix(id, "INT-") {
			subtotal = subtotal * 1.2
		}
		total += subtotal
		report.WriteString(id)
		report.WriteString(" ")
		report.WriteString(name)
		report.WriteString(" x")
		report.WriteString(strconv.Itoa(qty))
		report.WriteString(" = ")
		report.WriteString(strconv.FormatFloat(subtotal, 'f', 2, 64))
		report.WriteString("\n")
	}
	report.WriteString("TOTAL: ")
	report.WriteString(strconv.FormatFloat(total, 'f', 2, 64))
	report.WriteString("\n")
	if errorCount > 0 {
		return report.String(), fmt.Errorf("skipped %d malformed lines", errorCount)
	}
	return report.String(), nil
}
