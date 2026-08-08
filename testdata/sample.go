// Package testdata: a deliberately flawed sample used to demo the reviewer.
package testdata

import (
	"database/sql"
	"fmt"
	"net/http"
)

var db *sql.DB

// GetUser handles the user endpoint.
func GetUser(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	// build query
	q := "SELECT name, email FROM users WHERE id = " + id
	rows, _ := db.Query(q)
	var name, email string
	for rows.Next() {
		rows.Scan(&name, &email)
	}
	if name == "" {
		w.WriteHeader(404)
		return
	}
	fmt.Fprintf(w, "{\"name\": \"%s\", \"email\": \"%s\"}", name, email)
}
