package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"os"
	"sync"

	"github.com/mmcdole/gofeed"
)

type Feed struct {
	URL string `json:"url"`
}

type Article struct {
	Title string
	Link  string
	Date  string
}

var mu sync.Mutex

func loadFeeds() []Feed {
	file, _ := os.ReadFile("feeds.json")
	var feeds []Feed
	json.Unmarshal(file, &feeds)
	return feeds
}

func saveFeed(newFeed Feed) {
	mu.Lock()
	defer mu.Unlock()

	feeds := loadFeeds()
	feeds = append(feeds, newFeed)

	data, _ := json.MarshalIndent(feeds, "", "  ")
	os.WriteFile("feeds.json", data, 0644)
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	feeds := loadFeeds()
	var articles []Article
	parser := gofeed.NewParser()

	for _, feed := range feeds {
		parsed, err := parser.ParseURL(feed.URL)
		if err != nil {
			continue
		}
		for _, item := range parsed.Items {
			articles = append(articles, Article{
				Title: item.Title,
				Link:  item.Link,
				Date:  item.Published,
			})
		}
	}

	tmpl := template.Must(template.ParseFiles("templates/index.html"))
	tmpl.Execute(w, articles)
}

func formHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("templates/form.html"))
	tmpl.Execute(w, nil)
}

func addHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		url := r.FormValue("url")
		if url != "" {
			saveFeed(Feed{URL: url})
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func main() {
	// Servir archivos estáticos como CSS
	fs := http.FileServer(http.Dir("static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/form", formHandler)
	http.HandleFunc("/add", addHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // fallback local
	}
	http.ListenAndServe(":"+port, nil)

}
