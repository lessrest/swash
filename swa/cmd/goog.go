package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

const (
	credentialsFile = "credentials.json"
	tokenFile       = "token.json"
)

func Goog() {
	ctx := context.Background()

	// Load client credentials
	config, err := loadClientConfig(credentialsFile)
	if err != nil {
		log.Fatalf("Unable to load client config: %v", err)
	}

	// Authenticate and get the OAuth2 client
	client := getClient(config)

	// Create Calendar service
	calendarService, err := calendar.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to create Calendar service: %v", err)
	}

	// Fetch and display upcoming events
	fetchUpcomingEvents(calendarService)

	// Create Gmail service
	gmailService, err := gmail.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to create Gmail service: %v", err)
	}

	// Fetch and display recent threads and messages
	fetchRecentThreadsAndMessages(gmailService)
}

func loadClientConfig(credentialsFile string) (*oauth2.Config, error) {
	b, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, calendar.CalendarReadonlyScope, gmail.GmailReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("unable to parse client secret file to config: %v", err)
	}

	return config, nil
}

func getClient(config *oauth2.Config) *http.Client {
	tok, err := tokenFromFile(tokenFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokenFile, tok)
	}
	return config.Client(context.Background(), tok)
}

func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Unable to read authorization code: %v", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web: %v", err)
	}
	return tok
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

func saveToken(path string, token *oauth2.Token) {
	fmt.Printf("Saving credential file to: %s\n", path)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

func fetchUpcomingEvents(srv *calendar.Service) {
	t := time.Now().Format(time.RFC3339)

	events, err := srv.Events.List("ieva.lange@gmail.com").ShowDeleted(false).
		SingleEvents(true).TimeMin(t).MaxResults(20).OrderBy("startTime").Do()
	if err != nil {
		log.Fatalf("Unable to retrieve next ten of the user's events: %v", err)
	}

	jsone, err := json.Marshal(events)
	if err != nil {
		log.Fatalf("Unable to marshal events: %v", err)
	}
	fmt.Println(string(jsone))
}

func fetchRecentThreadsAndMessages(srv *gmail.Service) {
	user := "me"
	r, err := srv.Users.Threads.List(user).MaxResults(10).Do()
	if err != nil {
		log.Fatalf("Unable to retrieve threads: %v", err)
	}
	json2, err := json.Marshal(r)
	if err != nil {
		log.Fatalf("Unable to marshal threads: %v", err)
	}
	fmt.Println(string(json2))

	for _, thread := range r.Threads {
		msg, err := srv.Users.Messages.Get(user, thread.Id).Do()
		if err != nil {
			log.Fatalf("Unable to retrieve message: %v", err)
		}
		fmt.Println(msg.Snippet)
	}
}
