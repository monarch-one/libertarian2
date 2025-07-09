package main

import (
	"encoding/json"
	"fmt" // Importar fmt para el formato de fecha
	"html/template"
	"log" // Importar log para errores de parseo de feeds
	"net/http"
	"os"
	"sync"
	"time" // Importar time para manejo de fechas

	"github.com/mmcdole/gofeed"
)

// Feed represents the structure of a saved RSS feed.
type Feed struct {
	URL string `json:"url"`
}

// Article represents an article from an RSS feed.
type Article struct {
	Title  string
	Link   string
	Date   string
	Source string // NUEVO CAMPO: Nombre de la fuente del feed
	IsFav  bool   // Indicates if the article is a favorite
}

// FavoriteArticle represents the structure of a saved favorite article.
type FavoriteArticle struct {
	Title  string `json:"title"`
	Link   string `json:"link"`
	Date   string `json:"date"`
	Source string `json:"source"` // NUEVO CAMPO
}

// mu is a Mutex to protect access to JSON files during concurrent writes.
var mu sync.Mutex

// loadFeeds loads the list of feeds from feeds.json.
func loadFeeds() []Feed {
	file, err := os.ReadFile("feeds.json")
	if err != nil {
		// If the file doesn't exist or there's an error, return an empty list.
		return []Feed{}
	}
	var feeds []Feed
	json.Unmarshal(file, &feeds)
	return feeds
}

// saveFeed saves a new feed to feeds.json.
func saveFeed(newFeed Feed) {
	mu.Lock()         // Lock the Mutex to prevent concurrent writes
	defer mu.Unlock() // Ensure the Mutex is unlocked at the end of the function

	feeds := loadFeeds()
	feeds = append(feeds, newFeed)

	data, _ := json.MarshalIndent(feeds, "", "  ")
	os.WriteFile("feeds.json", data, 0644)
}

// saveFavorite saves an article as a favorite to favorites.json.
func saveFavorite(article FavoriteArticle) {
	mu.Lock()
	defer mu.Unlock()

	var favorites []FavoriteArticle
	data, err := os.ReadFile("favorites.json")
	if err == nil { // Only unmarshal if file exists and no error
		json.Unmarshal(data, &favorites)
	}

	// Avoid duplicates
	for _, fav := range favorites {
		if fav.Link == article.Link {
			return // Article already favorited
		}
	}

	favorites = append(favorites, article)

	updated, _ := json.MarshalIndent(favorites, "", "  ")
	os.WriteFile("favorites.json", updated, 0644)
}

// loadFavorites loads the list of favorite articles from favorites.json.
func loadFavorites() []FavoriteArticle {
	var favorites []FavoriteArticle
	data, err := os.ReadFile("favorites.json")
	if err != nil {
		return []FavoriteArticle{} // Return empty list if file doesn't exist
	}
	json.Unmarshal(data, &favorites)
	return favorites
}

// homeHandler displays the main feed page.
func homeHandler(w http.ResponseWriter, r *http.Request) {
	feeds := loadFeeds()
	var articles []Article
	fp := gofeed.NewParser()

	favorites := loadFavorites()

	for _, f := range feeds {
		feed, err := fp.ParseURL(f.URL)
		if err != nil {
			log.Printf("Error parsing feed %s: %v", f.URL, err)
			continue
		}

		// Obtener el título del feed como fuente
		feedSource := feed.Title // Usamos el título del feed como la fuente

		for _, item := range feed.Items {
			var parsedDate time.Time
			if item.PublishedParsed != nil {
				parsedDate = *item.PublishedParsed
			} else if item.UpdatedParsed != nil {
				parsedDate = *item.UpdatedParsed
			} else {
				parsedDate = time.Now() // Fallback to current time
			}

			isFavorite := false
			for _, fav := range favorites {
				if fav.Link == item.Link {
					isFavorite = true
					break
				}
			}

			articles = append(articles, Article{
				Title:  item.Title,
				Link:   item.Link,
				Date:   parsedDate.Format("02/01/2006"), // Format: DD/MM/YYYY
				Source: feedSource,                      // Añadir la fuente
				IsFav:  isFavorite,
			})
		}
	}

	tmpl := template.Must(template.ParseFiles("templates/index.html"))
	tmpl.Execute(w, articles)
}

// formHandler displays the form to add a new feed.
func formHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("templates/form.html"))
	tmpl.Execute(w, nil)
}

// addHandler processes the new feed submission.
func addHandler(w http.ResponseWriter, r *http.Request) {
	url := r.FormValue("url")
	if url == "" {
		http.Redirect(w, r, "/form", http.StatusSeeOther)
		return
	}

	newFeed := Feed{URL: url}
	saveFeed(newFeed)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// favoriteHandler processes the favorite action for an article.
func favoriteHandler(w http.ResponseWriter, r *http.Request) {
	title := r.FormValue("title")
	link := r.FormValue("link")
	date := r.FormValue("date")
	source := r.FormValue("source") // RECUPERAR LA FUENTE

	if title == "" || link == "" || date == "" || source == "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	article := FavoriteArticle{
		Title:  title,
		Link:   link,
		Date:   date,
		Source: source, // ASIGNAR LA FUENTE
	}
	saveFavorite(article)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// favoritesPage displays the list of favorite articles.
func favoritesPage(w http.ResponseWriter, r *http.Request) {
	favorites := loadFavorites()

	tmpl := template.Must(template.ParseFiles("templates/favorites.html"))
	tmpl.Execute(w, favorites)
}

// removeFavoriteHandler processes the removal of an article from favorites.
func removeFavoriteHandler(w http.ResponseWriter, r *http.Request) {
	link := r.FormValue("link")
	if link == "" {
		http.Redirect(w, r, "/favorites", http.StatusSeeOther)
		return
	}

	mu.Lock()
	defer mu.Unlock()

	var favorites []FavoriteArticle
	data, err := os.ReadFile("favorites.json")
	if err != nil {
		// If the file doesn't exist or there's an error, nothing to remove, just redirect.
		http.Redirect(w, r, "/favorites", http.StatusSeeOther)
		return
	}
	json.Unmarshal(data, &favorites)

	var updated []FavoriteArticle
	for _, fav := range favorites {
		if fav.Link != link { // If the link doesn't match, add it to the updated list
			updated = append(updated, fav)
		}
	}

	newData, _ := json.MarshalIndent(updated, "", "  ")
	os.WriteFile("favorites.json", newData, 0644)

	http.Redirect(w, r, "/favorites", http.StatusSeeOther)
}

func main() {
	// Configure the static file server (CSS, JS, images, etc.)
	fs := http.FileServer(http.Dir("static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	// Define routes and their handlers
	http.HandleFunc("/remove", removeFavoriteHandler)
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/form", formHandler)
	http.HandleFunc("/add", addHandler)
	http.HandleFunc("/favorite", favoriteHandler)
	http.HandleFunc("/favorites", favoritesPage) // Asegúrate de que esta línea esté presente

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080" // Default port if not specified
	}

	fmt.Printf("SERVER LISTENING ON :%s\n", port) // Cambiado a mayúsculas
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
