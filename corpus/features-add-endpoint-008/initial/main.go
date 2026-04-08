package main

import (
	"encoding/json"
	"log"
	"net/http"
)

// User represents a user in the system.
type User struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// usersHandler returns a static list of users.
func usersHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	users := []User{
		{ID: 1, Name: "Alice"},
		{ID: 2, Name: "Bob"},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

// SetupRoutes registers all HTTP handlers on the given mux.
// If mux is nil, http.DefaultServeMux is used.
func SetupRoutes(mux *http.ServeMux) {
	if mux == nil {
		mux = http.DefaultServeMux
	}
	mux.HandleFunc("/api/users", usersHandler)
}

func main() {
	mux := http.NewServeMux()
	SetupRoutes(mux)

	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
