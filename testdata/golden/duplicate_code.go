package golden

import "strings"

func FormatCustomerLabel(name, city, country string) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(strings.TrimSpace(name)))
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(city))
	b.WriteString(", ")
	b.WriteString(strings.ToUpper(strings.TrimSpace(country)))
	b.WriteString("\n")
	return b.String()
}

func FormatSupplierLabel(name, city, country string) string {
	var b strings.Builder
	b.WriteString(strings.ToUpper(strings.TrimSpace(name)))
	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(city))
	b.WriteString(", ")
	b.WriteString(strings.ToUpper(strings.TrimSpace(country)))
	b.WriteString("\n")
	return b.String()
}
