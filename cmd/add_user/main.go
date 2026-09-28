package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "github.com/lib/pq"
)

func main() {
	connStr := "user=postgres password=715192 dbname=kido_garage sslmode=disable"
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		INSERT INTO users (id, email, password_hash, role, first_name)
		VALUES (COALESCE((SELECT MAX(id) FROM users), 0) + 1, 'cliente1', 'cliente1', 'CLIENT', 'Cliente 1')
		ON CONFLICT (email) DO UPDATE SET password_hash = 'cliente1', role = 'CLIENT'
	`)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("User cliente1 added successfully.")
}

