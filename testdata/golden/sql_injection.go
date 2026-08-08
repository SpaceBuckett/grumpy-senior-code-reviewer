package golden

import (
	"database/sql"
	"fmt"
)

func FindUserEmail(db *sql.DB, name string) (string, error) {
	query := fmt.Sprintf("SELECT email FROM users WHERE name = '%s'", name)
	var email string
	if err := db.QueryRow(query).Scan(&email); err != nil {
		return "", err
	}
	return email, nil
}
