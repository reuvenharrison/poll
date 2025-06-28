package main

import (
	"database/sql"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	_ "github.com/mattn/go-sqlite3"
)

type Apartment struct {
	Number  int    `json:"number"`
	Name    string `json:"name"`
	VotedAt string `json:"voted_at"`
}

type Vote struct {
	Value     string `json:"value"`
	Timestamp string `json:"timestamp"`
}

var db *sql.DB

func initDB() error {
	var err error
	dbPath := filepath.Join(".", "poll.db")
	db, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		return err
	}

	// Create apartments table
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS apartments (
			number INTEGER PRIMARY KEY,
			voter_name TEXT NOT NULL,
			voted_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return err
	}

	// Create votes table (anonymous)
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS votes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			vote_value TEXT NOT NULL,
			timestamp TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)
	`)
	return err
}

func main() {
	if err := initDB(); err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	r := gin.Default()

	// Serve static files
	r.Static("/static", "./static")
	r.LoadHTMLGlob("templates/*")

	// Routes
	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})

	// API endpoints
	api := r.Group("/api")
	{
		api.GET("/check-apartment/:number", checkApartment)
		api.POST("/vote", submitVote)
		api.GET("/results", getResults)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}

func checkApartment(c *gin.Context) {
	var apartment Apartment
	row := db.QueryRow("SELECT number, voter_name, voted_at FROM apartments WHERE number = ?", c.Param("number"))
	err := row.Scan(&apartment.Number, &apartment.Name, &apartment.VotedAt)

	if err == sql.ErrNoRows {
		c.JSON(http.StatusOK, gin.H{"voted": false})
		return
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"voted": true,
		"name":  apartment.Name,
	})
}

func submitVote(c *gin.Context) {
	var input struct {
		ApartmentNumber int    `json:"apartment_number"`
		VoterName       string `json:"voter_name"`
		Vote            string `json:"vote"`
	}

	if err := c.BindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Start transaction
	tx, err := db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	defer tx.Rollback()

	// Check if apartment already voted
	var exists bool
	err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM apartments WHERE number = ?)", input.ApartmentNumber).Scan(&exists)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Apartment already voted"})
		return
	}

	// Record apartment vote (without choice)
	_, err = tx.Exec("INSERT INTO apartments (number, voter_name) VALUES (?, ?)",
		input.ApartmentNumber, input.VoterName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Record anonymous vote
	_, err = tx.Exec("INSERT INTO votes (vote_value) VALUES (?)", input.Vote)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err = tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func getResults(c *gin.Context) {
	var results struct {
		For     int `json:"for"`
		Against int `json:"against"`
		Total   int `json:"total"`
	}

	err := db.QueryRow(`
		SELECT 
			COUNT(CASE WHEN vote_value = 'בעד' THEN 1 END) as for_votes,
			COUNT(CASE WHEN vote_value = 'נגד' THEN 1 END) as against_votes,
			COUNT(*) as total
		FROM votes
	`).Scan(&results.For, &results.Against, &results.Total)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, results)
}
