package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/gin-gonic/gin"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Apartment struct {
	Number  int       `json:"number" firestore:"number"`
	Name    string    `json:"name" firestore:"name"`
	VotedAt time.Time `json:"voted_at" firestore:"voted_at"`
}

type Vote struct {
	Value     string `json:"value" firestore:"value"`
	RandomKey int64  `json:"-" firestore:"random_key"` // For random ordering
}

type Results struct {
	For     int  `json:"for"`
	Against int  `json:"against"`
	Total   int  `json:"total"`
	Hidden  bool `json:"hidden"`
}

var client *firestore.Client
var pollEndTime time.Time
var ctx = context.Background()

func initPollEndTime() {
	// Default end date if environment variable is not set
	defaultEndTime := "2024-04-01 23:59:59"
	endTimeStr := os.Getenv("POLL_END_TIME")
	if endTimeStr == "" {
		endTimeStr = defaultEndTime
	}

	var err error
	pollEndTime, err = time.ParseInLocation("2006-01-02 15:04:05", endTimeStr, time.Local)
	if err != nil {
		log.Printf("Error parsing POLL_END_TIME, using default: %v", err)
		pollEndTime, _ = time.ParseInLocation("2006-01-02 15:04:05", defaultEndTime, time.Local)
	}
}

func initFirestore() error {
	var err error
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		projectID = "your-project-id" // Replace with your project ID
	}

	client, err = firestore.NewClient(ctx, projectID)
	if err != nil {
		return err
	}

	// Add dummy votes if they don't exist yet
	dummyKey := "dummy_votes_added"
	_, err = client.Collection("system").Doc(dummyKey).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			// Add one dummy vote for each option
			batch := client.Batch()

			// Dummy "for" vote
			dummyFor := Vote{
				Value:     "בעד",
				RandomKey: rand.Int63(),
			}
			batch.Create(client.Collection("votes").NewDoc(), dummyFor)

			// Dummy "against" vote
			dummyAgainst := Vote{
				Value:     "נגד",
				RandomKey: rand.Int63(),
			}
			batch.Create(client.Collection("votes").NewDoc(), dummyAgainst)

			// Mark that dummy votes were added
			batch.Create(client.Collection("system").Doc(dummyKey), map[string]interface{}{
				"added_at": time.Now(),
			})

			_, err := batch.Commit(ctx)
			if err != nil {
				return fmt.Errorf("failed to add dummy votes: %v", err)
			}
		} else {
			return err
		}
	}

	return nil
}

func main() {
	initPollEndTime()

	if err := initFirestore(); err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	r := gin.Default()

	r.Static("/static", "./static")
	r.LoadHTMLGlob("templates/*")

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", gin.H{
			"endDate": pollEndTime.Format("2006-01-02 15:04:05"),
		})
	})

	api := r.Group("/api")
	{
		api.GET("/check-apartment/:number", checkApartment)
		api.POST("/vote", submitVote)
		api.GET("/results", getResults)
		api.GET("/poll-status", getPollStatus)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	r.Run(":" + port)
}

func checkApartment(c *gin.Context) {
	if time.Now().After(pollEndTime) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ההצבעה הסתיימה"})
		return
	}

	apartmentNumber := c.Param("number")
	doc, err := client.Collection("apartments").Doc(apartmentNumber).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			c.JSON(http.StatusOK, gin.H{"voted": false})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	var apartment Apartment
	if err := doc.DataTo(&apartment); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"voted": true,
		"name":  apartment.Name,
	})
}

func submitVote(c *gin.Context) {
	if time.Now().After(pollEndTime) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ההצבעה הסתיימה"})
		return
	}

	var input struct {
		ApartmentNumber int    `json:"apartment_number" binding:"required"`
		VoterName       string `json:"voter_name" binding:"required"`
		Vote            string `json:"vote" binding:"required"`
	}

	if err := c.BindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Start a transaction
	err := client.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		apartmentRef := client.Collection("apartments").Doc(strconv.Itoa(input.ApartmentNumber))

		// Check if apartment already voted
		_, err := tx.Get(apartmentRef)
		if err == nil {
			return &gin.Error{Err: err, Type: gin.ErrorTypeBind, Meta: "דירה זו כבר הצביעה"}
		} else if status.Code(err) != codes.NotFound {
			return err
		}

		// Record apartment vote
		apartment := Apartment{
			Number:  input.ApartmentNumber,
			Name:    input.VoterName,
			VotedAt: time.Now(),
		}
		if err := tx.Set(apartmentRef, apartment); err != nil {
			return err
		}

		// Add vote with random key for ordering
		vote := Vote{
			Value:     input.Vote,
			RandomKey: rand.Int63(),
		}
		_, err = client.Collection("votes").NewDoc().Set(ctx, vote)
		return err
	})

	if err != nil {
		if ginErr, ok := err.(*gin.Error); ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": ginErr.Meta})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "success"})
}

func getResults(c *gin.Context) {
	if !time.Now().After(pollEndTime) {
		c.JSON(http.StatusOK, gin.H{
			"hidden":  true,
			"message": "התוצאות יפורסמו בתאריך " + pollEndTime.Format("02/01/2006") + " בשעה " + pollEndTime.Format("15:04"),
		})
		return
	}

	var results Results
	iter := client.Collection("votes").Documents(ctx)
	defer iter.Stop()

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		var vote Vote
		if err := doc.DataTo(&vote); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		results.Total++
		if vote.Value == "בעד" {
			results.For++
		} else if vote.Value == "נגד" {
			results.Against++
		}
	}

	// Deduct the dummy votes from the totals
	results.For--
	results.Against--
	results.Total -= 2
	results.Hidden = false
	c.JSON(http.StatusOK, results)
}

func getPollStatus(c *gin.Context) {
	isEnded := time.Now().After(pollEndTime)
	c.JSON(http.StatusOK, gin.H{
		"is_ended": isEnded,
		"end_date": pollEndTime.Format("2006-01-02T15:04:05"),
	})
}
