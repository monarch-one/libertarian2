package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mmcdole/gofeed"
)

type Feed struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

type Article struct {
	Title       string
	Link        string
	Date        string
	Source      string
	Description string
	IsFav       bool
}

type FavoriteArticle struct {
	Title  string `json:"title"`
	Link   string `json:"link"`
	Date   string `json:"date"`
	Source string `json:"source"`
}

type TemplateData struct {
	Articles      []Article
	ImportMessage string
}

type OPML struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    Head     `xml:"head"`
	Body    Body     `xml:"body"`
}

type Head struct {
	Title string `xml:"title"`
}

type Body struct {
	Outlines []Outline `xml:"outline"`
}

type Outline struct {
	Text     string    `xml:"text,attr"`
	Title    string    `xml:"title,attr"`
	Type     string    `xml:"type,attr"`
	XMLURL   string    `xml:"xmlUrl,attr"`
	HTMLURL  string    `xml:"htmlUrl,attr"`
	Outlines []Outline `xml:"outline"`
}

func loadFeeds() []Feed {
	file, err := os.Open("feeds.json")
	if err != nil {
		return []Feed{}
	}
	defer file.Close()

	var feeds []Feed
	json.NewDecoder(file).Decode(&feeds)
	return feeds
}

func saveFeed(feed Feed) error {
	feeds := loadFeeds()
	for _, f := range feeds {
		if f.URL == feed.URL {
			return nil
		}
	}
	feeds = append(feeds, feed)

	file, err := os.Create("feeds.json")
	if err != nil {
		return err
	}
	defer file.Close()

	return json.NewEncoder(file).Encode(feeds)
}

func loadFavoriteArticles() []FavoriteArticle {
	file, err := os.Open("favorites.json")
	if err != nil {
		return []FavoriteArticle{}
	}
	defer file.Close()

	var favorites []FavoriteArticle
	json.NewDecoder(file).Decode(&favorites)
	return favorites
}

func saveFavoriteArticle(article FavoriteArticle) {
	favorites := loadFavoriteArticles()

	for i, fav := range favorites {
		if fav.Link == article.Link {
			favorites = append(favorites[:i], favorites[i+1:]...)
			file, err := os.Create("favorites.json")
			if err != nil {
				return
			}
			defer file.Close()
			json.NewEncoder(file).Encode(favorites)
			return
		}
	}

	favorites = append(favorites, article)
	file, err := os.Create("favorites.json")
	if err != nil {
		return
	}
	defer file.Close()
	json.NewEncoder(file).Encode(favorites)
}

func isArticleFavorite(link string) bool {
	favorites := loadFavoriteArticles()
	for _, fav := range favorites {
		if fav.Link == link {
			return true
		}
	}
	return false
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	feeds := loadFeeds()
	var allArticles []Article

	for _, feed := range feeds {
		articles := fetchFeedArticles(feed.URL)
		allArticles = append(allArticles, articles...)
	}

	for i := range allArticles {
		allArticles[i].IsFav = isArticleFavorite(allArticles[i].Link)
	}

	if len(allArticles) > 50 {
		allArticles = allArticles[:50]
	}

	importMessage := r.URL.Query().Get("imported")

	data := TemplateData{
		Articles:      allArticles,
		ImportMessage: importMessage,
	}

	renderHomePage(w, data)
}

func fetchFeedArticles(feedURL string) []Article {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	fp := gofeed.NewParser()
	fp.Client = client

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	log.Printf("🌐 Intentando acceder al feed: %s", feedURL)
	feed, err := fp.ParseURLWithContext(feedURL, ctx)
	if err != nil {
		log.Printf("❌ Error al acceder al feed %s: %v", feedURL, err)
		return []Article{}
	}

	log.Printf("✅ Feed obtenido exitosamente: %s", feed.Title)

	var articles []Article
	for _, item := range feed.Items {
		date := ""
		if item.PublishedParsed != nil {
			date = item.PublishedParsed.Format("2006-01-02 15:04")
		} else if item.Published != "" {
			date = item.Published
		}

		description := ""
		if item.Description != "" {
			description = item.Description
		} else if item.Content != "" {
			description = item.Content
		}

		article := Article{
			Title:       item.Title,
			Link:        item.Link,
			Date:        date,
			Source:      feed.Title,
			Description: description,
		}
		articles = append(articles, article)
	}

	return articles
}

func renderHomePage(w http.ResponseWriter, data TemplateData) {
	log.Printf("🔍 Rendering home page with %d articles", len(data.Articles))

	simpleHTML := `<!DOCTYPE html>
<html lang="es">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>LIBERTARIAN 2.0</title>
    <style>
        @font-face {
            font-family: 'JetBrains Mono';
            src: url('/static/fonts/JetBrainsMonoNerdFont-Regular.woff2') format('woff2');
            font-weight: normal;
            font-style: normal;
        }
        
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }
        
        body {
            background: #0a0a0a;
            color: #e0e0e0;
            font-family: 'JetBrains Mono', monospace;
            font-size: 14px;
            line-height: 1.4;
        }
        
        .header {
            background: #0a0a0a;
            padding: 20px;
            border-bottom: 1px solid #333;
        }
        
        .title {
            color: #00ff00;
            font-size: 18px;
            font-weight: bold;
            margin-bottom: 10px;
        }
        
        .nav-tabs {
            display: flex;
            gap: 20px;
            margin-bottom: 20px;
        }
        
        .tab {
            color: #888;
            text-decoration: none;
            padding: 8px 0;
            border-bottom: 2px solid transparent;
            transition: all 0.2s;
        }
        
        .tab:hover,
        .tab.active {
            color: #00ff00;
            border-bottom-color: #00ff00;
        }
        
        .content {
            padding: 0 20px 20px;
        }
        
        .tab-content {
            display: none;
        }
        
        .tab-content.active {
            display: block;
        }
        
        .import-form {
            background: #111;
            padding: 20px;
            border-radius: 5px;
            margin-bottom: 20px;
            border: 1px solid #333;
        }
        
        .form-group {
            margin-bottom: 15px;
        }
        
        label {
            display: block;
            margin-bottom: 5px;
            color: #ccc;
        }
        
        input[type="file"],
        input[type="url"] {
            width: 100%;
            padding: 8px;
            background: #222;
            border: 1px solid #444;
            border-radius: 3px;
            color: #e0e0e0;
            font-family: 'JetBrains Mono', monospace;
        }
        
        button {
            background: #00ff00;
            color: #000;
            border: none;
            padding: 10px 20px;
            border-radius: 3px;
            cursor: pointer;
            font-family: 'JetBrains Mono', monospace;
            font-weight: bold;
        }
        
        button:hover {
            background: #00cc00;
        }
        
        .article {
            background: #111;
            margin-bottom: 15px;
            border-radius: 5px;
            border: 1px solid #333;
            overflow: hidden;
        }
        
        .article-header {
            padding: 15px;
            cursor: pointer;
            display: flex;
            justify-content: space-between;
            align-items: center;
            border-bottom: 1px solid #333;
        }
        
        .article-header:hover {
            background: #1a1a1a;
        }
        
        .article-info {
            flex: 1;
        }
        
        .article-date {
            color: #888;
            font-size: 12px;
        }
        
        .article-title {
            color: #e0e0e0;
            margin: 5px 0;
            font-weight: normal;
        }
        
        .article-source {
            color: #00ff00;
            font-size: 12px;
        }
        
        .article-actions {
            display: flex;
            gap: 10px;
            align-items: center;
        }
        
        .save-btn {
            background: none;
            border: 1px solid #666;
            color: #ccc;
            padding: 5px 10px;
            font-size: 12px;
            border-radius: 3px;
            cursor: pointer;
        }
        
        .save-btn:hover {
            border-color: #00ff00;
            color: #00ff00;
        }
        
        .expand-icon {
            color: #666;
            font-size: 16px;
            transform: rotate(0deg);
            transition: transform 0.2s;
        }
        
        .article.expanded .expand-icon {
            transform: rotate(90deg);
        }
        
        .article-content {
            padding: 15px;
            background: #0a0a0a;
            border-top: 1px solid #333;
            display: none;
        }
        
        .article.expanded .article-content {
            display: block;
        }
        
        .article-description {
            color: #ccc;
            line-height: 1.6;
            margin-bottom: 10px;
        }
        
        .article-link {
            color: #00ff00;
            text-decoration: none;
        }
        
        .article-link:hover {
            text-decoration: underline;
        }
        
        .status-message {
            background: #1a4a1a;
            color: #00ff00;
            padding: 10px;
            border-radius: 3px;
            margin-bottom: 20px;
            border: 1px solid #2a5a2a;
        }
    </style>
</head>
<body>
    <div class="header">
        <div class="title">LIBERTARIAN 2.0</div>
        <nav class="nav-tabs">
            <a href="#" class="tab active" onclick="showTab('articles')">+ AGREGAR NUEVO FEED</a>
            <a href="#" class="tab" onclick="showTab('import')">📁 IMPORTAR OPML</a>
            <a href="#" class="tab" onclick="showTab('favorites')">⭐ VER FAVORITOS</a>
        </nav>
    </div>
    
    <div class="content">
        <div id="articles" class="tab-content active">` +
		fmt.Sprintf(`<div class="status-message">SAVE | %s | %d artículos cargados</div>`,
			time.Now().Format("02/01/2006 15:04"), len(data.Articles))

	// Mostrar artículos
	for i, article := range data.Articles {
		if i >= 50 {
			break
		}

		// Limpiar descripción
		description := article.Description
		if len(description) > 300 {
			description = description[:300] + "..."
		}

		simpleHTML += fmt.Sprintf(`
            <div class="article" onclick="toggleArticle(this)">
                <div class="article-header">
                    <div class="article-info">
                        <div class="article-date">SAVE | %s</div>
                        <div class="article-title">%s</div>
                        <div class="article-source">%s</div>
                    </div>
                    <div class="article-actions">
                        <button class="save-btn" onclick="event.stopPropagation(); toggleFavorite('%s', '%s', '%s', '%s')">SAVE</button>
                        <span class="expand-icon">▶</span>
                    </div>
                </div>
                <div class="article-content">
                    <div class="article-description">%s</div>
                    <a href="%s" target="_blank" class="article-link">Leer artículo completo →</a>
                </div>
            </div>`,
			article.Date,
			article.Title,
			article.Source,
			article.Title,
			article.Link,
			article.Date,
			article.Source,
			description,
			article.Link)
	}

	simpleHTML += `
        </div>
        
        <div id="import" class="tab-content">
            <div class="import-form">
                <h3 style="color: #00ff00; margin-bottom: 15px;">Importar feeds desde OPML</h3>
                <form action="/add" method="post" enctype="multipart/form-data">
                    <div class="form-group">
                        <label for="opml">Seleccionar archivo OPML:</label>
                        <input type="file" id="opml" name="opml" accept=".opml,.xml" required>
                    </div>
                    <button type="submit">Importar OPML</button>
                </form>
            </div>
            
            <div class="import-form">
                <h3 style="color: #00ff00; margin-bottom: 15px;">Agregar feed individual</h3>
                <form action="/add" method="post">
                    <div class="form-group">
                        <label for="feed_url">URL del feed RSS:</label>
                        <input type="url" id="feed_url" name="feed_url" placeholder="https://example.com/feed.xml" required>
                    </div>
                    <button type="submit">Agregar Feed</button>
                </form>
            </div>
        </div>
        
        <div id="favorites" class="tab-content">
            <div id="favorites-content">
                <p style="color: #888;">Cargando favoritos...</p>
            </div>
        </div>
    </div>

    <script>
        function showTab(tabName) {
            document.querySelectorAll('.tab-content').forEach(content => {
                content.classList.remove('active');
            });
            
            document.querySelectorAll('.tab').forEach(tab => {
                tab.classList.remove('active');
            });
            
            document.getElementById(tabName).classList.add('active');
            event.target.classList.add('active');
            
            if (tabName === 'favorites') {
                loadFavorites();
            }
        }
        
        function toggleArticle(element) {
            element.classList.toggle('expanded');
        }
        
        function toggleFavorite(title, link, date, source) {
            fetch('/favorite', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/x-www-form-urlencoded',
                },
                body: 'title=' + encodeURIComponent(title) + 
                      '&link=' + encodeURIComponent(link) + 
                      '&date=' + encodeURIComponent(date) + 
                      '&source=' + encodeURIComponent(source)
            })
            .then(response => response.text())
            .then(result => {
                console.log('Favorito:', result);
            });
        }
        
        function loadFavorites() {
            fetch('/api/favorites')
                .then(response => response.json())
                .then(favorites => {
                    const container = document.getElementById('favorites-content');
                    if (favorites.length === 0) {
                        container.innerHTML = '<p style="color: #888;">No hay artículos favoritos.</p>';
                        return;
                    }
                    
                    let html = '';
                    favorites.forEach(fav => {
                        html += '<div class="article">';
                        html += '<div class="article-header">';
                        html += '<div class="article-info">';
                        html += '<div class="article-date">SAVE | ' + fav.date + '</div>';
                        html += '<div class="article-title">' + fav.title + '</div>';
                        html += '<div class="article-source">' + fav.source + '</div>';
                        html += '</div>';
                        html += '</div>';
                        html += '</div>';
                    });
                    container.innerHTML = html;
                });
        }
    </script>
</body>
</html>`

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(simpleHTML))
	log.Printf("✅ HTML rendered successfully with %d articles", len(data.Articles))
}

func parseOPML(content []byte) ([]string, error) {
	log.Printf("🔍 Parsing OPML content, size: %d bytes", len(content))

	var feeds []string
	var inOutline bool
	var currentURL string

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.Contains(line, "<outline") {
			inOutline = true
			currentURL = ""

			// Buscar xmlUrl en la línea
			if idx := strings.Index(line, "xmlUrl=\""); idx != -1 {
				start := idx + 8
				if end := strings.Index(line[start:], "\""); end != -1 {
					currentURL = line[start : start+end]
				}
			}

			// Si la línea se cierra en la misma línea, procesar
			if strings.Contains(line, "/>") || strings.Contains(line, "</outline>") {
				if currentURL != "" && isValidURL(currentURL) {
					feeds = append(feeds, currentURL)
					log.Printf("✅ Found feed URL: %s", currentURL)
				}
				inOutline = false
			}
		} else if inOutline && strings.Contains(line, "</outline>") {
			if currentURL != "" && isValidURL(currentURL) {
				feeds = append(feeds, currentURL)
				log.Printf("✅ Found feed URL: %s", currentURL)
			}
			inOutline = false
		}
	}

	log.Printf("✅ Parsed OPML successfully, found %d feeds", len(feeds))
	return feeds, nil
}

func isValidURL(urlStr string) bool {
	if urlStr == "" {
		return false
	}
	u, err := url.Parse(urlStr)
	return err == nil && u.Scheme != "" && u.Host != ""
}

func getAllArticles() ([]Article, error) {
	feeds := loadFeeds()
	var allArticles []Article

	for _, feed := range feeds {
		if !feed.Active {
			continue
		}
		articles := fetchFeedArticles(feed.URL)
		allArticles = append(allArticles, articles...)
	}

	// Marcar favoritos
	for i := range allArticles {
		allArticles[i].IsFav = isArticleFavorite(allArticles[i].Link)
	}

	// Limitar a 50 artículos
	if len(allArticles) > 50 {
		allArticles = allArticles[:50]
	}

	return allArticles, nil
}

func addHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("🔄 Add handler called, method: %s", r.Method)

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var feedURLs []string

	// Verificar si es un archivo OPML
	if err := r.ParseMultipartForm(10 << 20); err == nil {
		if file, _, err := r.FormFile("opml"); err == nil {
			defer file.Close()

			content, err := io.ReadAll(file)
			if err != nil {
				log.Printf("❌ Error reading OPML file: %v", err)
				http.Error(w, "Error reading OPML file", http.StatusBadRequest)
				return
			}

			urls, err := parseOPML(content)
			if err != nil {
				log.Printf("❌ Error parsing OPML: %v", err)
				http.Error(w, "Error parsing OPML file", http.StatusBadRequest)
				return
			}

			feedURLs = urls
			log.Printf("✅ Extracted %d URLs from OPML", len(feedURLs))
		}
	}

	// Si no es OPML, verificar URL individual
	if len(feedURLs) == 0 {
		feedURL := r.FormValue("feed_url")
		if feedURL == "" {
			log.Printf("❌ No feed URL or OPML file provided")
			http.Error(w, "Feed URL or OPML file required", http.StatusBadRequest)
			return
		}
		feedURLs = []string{feedURL}
	}

	// Agregar cada feed
	for _, feedURL := range feedURLs {
		log.Printf("🔄 Processing feed URL: %s", feedURL)

		feeds := loadFeeds()

		// Verificar si ya existe
		exists := false
		for _, existingFeed := range feeds {
			if existingFeed.URL == feedURL {
				exists = true
				break
			}
		}

		if !exists {
			newFeed := Feed{
				URL:    feedURL,
				Active: true,
			}

			if err := saveFeed(newFeed); err != nil {
				log.Printf("❌ Error saving feed %s: %v", feedURL, err)
				continue
			}

			log.Printf("✅ Feed added successfully: %s", feedURL)
		} else {
			log.Printf("ℹ️ Feed already exists: %s", feedURL)
		}
	}

	// Redirigir de vuelta a la página principal
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func favoriteHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("⭐ Favorite handler called")

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	title := r.FormValue("title")
	link := r.FormValue("link")
	date := r.FormValue("date")
	source := r.FormValue("source")

	if title == "" || link == "" {
		log.Printf("❌ Missing required favorite data")
		http.Error(w, "Title and link required", http.StatusBadRequest)
		return
	}

	favorite := FavoriteArticle{
		Title:  title,
		Link:   link,
		Date:   date,
		Source: source,
	}

	favorites := loadFavoriteArticles()

	// Verificar si ya existe
	exists := false
	for _, fav := range favorites {
		if fav.Link == link {
			exists = true
			break
		}
	}

	if !exists {
		favorites = append(favorites, favorite)

		favoritesJSON, err := json.MarshalIndent(favorites, "", "  ")
		if err != nil {
			log.Printf("❌ Error marshaling favorites: %v", err)
			http.Error(w, "Error saving favorite", http.StatusInternalServerError)
			return
		}

		if err := os.WriteFile("favorites.json", favoritesJSON, 0644); err != nil {
			log.Printf("❌ Error saving favorites file: %v", err)
			http.Error(w, "Error saving favorite", http.StatusInternalServerError)
			return
		}

		log.Printf("✅ Favorite added: %s", title)
		w.Write([]byte("Added"))
	} else {
		log.Printf("ℹ️ Favorite already exists: %s", title)
		w.Write([]byte("Already exists"))
	}
}

func apiFavoritesHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("📋 API favorites handler called")

	favorites := loadFavoriteArticles()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(favorites); err != nil {
		log.Printf("❌ Error encoding favorites: %v", err)
		http.Error(w, "Error loading favorites", http.StatusInternalServerError)
		return
	}

	log.Printf("✅ Returned %d favorites", len(favorites))
}

func staticHandler(w http.ResponseWriter, r *http.Request) {
	// Servir archivos estáticos
	http.ServeFile(w, r, "."+r.URL.Path)
}

func main() {
	log.Printf("🚀 Starting LIBERTARIAN 2.0 Server...")

	// Verificar que existan los archivos necesarios
	if _, err := os.Stat("feeds.json"); os.IsNotExist(err) {
		log.Printf("📝 Creating feeds.json with default feeds")
		defaultFeeds := []Feed{
			{URL: "https://feeds.feedburner.com/oreilly/radar", Active: true},
			{URL: "https://rss.cnn.com/rss/edition.rss", Active: true},
		}
		feedsJSON, _ := json.MarshalIndent(defaultFeeds, "", "  ")
		os.WriteFile("feeds.json", feedsJSON, 0644)
	}

	if _, err := os.Stat("favorites.json"); os.IsNotExist(err) {
		log.Printf("📝 Creating empty favorites.json")
		os.WriteFile("favorites.json", []byte("[]"), 0644)
	}

	// Configurar rutas
	http.HandleFunc("/", homeHandler)
	http.HandleFunc("/add", addHandler)
	http.HandleFunc("/favorite", favoriteHandler)
	http.HandleFunc("/api/favorites", apiFavoritesHandler)
	http.HandleFunc("/static/", staticHandler)

	port := ":8081"
	log.Printf("🌐 Server starting on http://localhost%s", port)
	log.Printf("📡 Ready to serve RSS feeds with tabbed interface!")
	log.Printf("✨ Features: OPML import, favorites, collapsible articles")

	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("❌ Server failed to start: %v", err)
	}
}
