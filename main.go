// ANCAP WEB - A Libertarian RSS Reader
// =================================
//
// DESARROLLO CON AIR HOT RELOADING:
// - Para desarrollo usar: air (hot reloading automático)
// - Para producción usar: go run main.go
// - Configuración en: .air.toml
// - Script rápido: dev-hot.bat
//
// CARACTERÍSTICAS:
// - Sistema de autenticación con sesiones
// - Lector RSS con cache y paralelización
// - Interfaz terminal/retro con JetBrains Mono
// - Navegación por teclado (F1-F4, flechas, Space, etc.)
// - Funciones: guardar, favoritos, compartir
// - Scraping de contenido completo de artículos
//
// CREDENCIALES POR DEFECTO:
// - admin / admin123
// - ancap / libertad

package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
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

type User struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Session struct {
	Username string
	Expires  time.Time
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

type CachedFeed struct {
	Articles  []Article
	LastFetch time.Time
	URL       string
}

type CachedArticleContent struct {
	Content   string
	Timestamp time.Time
	Success   bool
}

type FeedCache struct {
	mutex sync.RWMutex
	feeds map[string]CachedFeed
}

type ArticleContentCache struct {
	mutex    sync.RWMutex
	articles map[string]CachedArticleContent
}

var globalCache = &FeedCache{
	feeds: make(map[string]CachedFeed),
}

var articleContentCache = &ArticleContentCache{
	articles: make(map[string]CachedArticleContent),
}

var sessions = make(map[string]Session)
var sessionMutex sync.RWMutex

const CACHE_DURATION = 1 * time.Minute
const SESSION_DURATION = 24 * time.Hour

func loadUsers() []User {
	file, err := os.Open("users.json")
	if err != nil {
		// Si no existe users.json, crear usuario por defecto
		defaultUsers := []User{
			{"admin", "admin123"},
			{"ancap", "libertad"},
		}
		saveUsers(defaultUsers)
		return defaultUsers
	}
	defer file.Close()

	var users []User
	json.NewDecoder(file).Decode(&users)
	return users
}

func saveUsers(users []User) error {
	file, err := os.Create("users.json")
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(users)
}

func validateLogin(username, password string) bool {
	users := loadUsers()
	for _, user := range users {
		if user.Username == username && user.Password == password {
			return true
		}
	}
	return false
}

func generateSessionID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func createSession(username string) string {
	sessionMutex.Lock()
	defer sessionMutex.Unlock()

	sessionID := generateSessionID()
	sessions[sessionID] = Session{
		Username: username,
		Expires:  time.Now().Add(SESSION_DURATION),
	}
	return sessionID
}

func validateSession(sessionID string) (string, bool) {
	sessionMutex.RLock()
	defer sessionMutex.RUnlock()

	session, exists := sessions[sessionID]
	if !exists || time.Now().After(session.Expires) {
		return "", false
	}
	return session.Username, true
}

func clearExpiredSessions() {
	sessionMutex.Lock()
	defer sessionMutex.Unlock()

	for id, session := range sessions {
		if time.Now().After(session.Expires) {
			delete(sessions, id)
		}
	}
}

func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Si es la página de login, permitir acceso
		if r.URL.Path == "/login" || r.URL.Path == "/api/login" {
			next.ServeHTTP(w, r)
			return
		}

		// Verificar sesión
		cookie, err := r.Cookie("session_id")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			return
		}

		username, valid := validateSession(cookie.Value)
		if !valid {
			http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
			return
		}

		// Agregar username al contexto si es necesario
		r.Header.Set("X-Username", username)
		next.ServeHTTP(w, r)
	})
}

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		gzw := &gzipResponseWriter{ResponseWriter: w, Writer: gz}
		next.ServeHTTP(gzw, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	Writer io.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func getUserFromRequest(r *http.Request) string {
	cookie, err := r.Cookie("session_id")
	if err != nil {
		log.Printf("🍪 No session_id cookie found: %v", err)
		return ""
	}

	log.Printf("🍪 Found session_id cookie: %s", cookie.Value)

	if username, valid := validateSession(cookie.Value); valid {
		log.Printf("✅ Valid session for user: %s", username)
		return username
	}

	log.Printf("❌ Invalid session for cookie: %s", cookie.Value)
	return ""
}

func getFeedsFilename(username string) string {
	if username == "" {
		return "feeds.json" // fallback para compatibilidad
	}
	return fmt.Sprintf("feeds_%s.json", username)
}

func loadFeeds() []Feed {
	return loadFeedsForUser("")
}

func loadFeedsForUser(username string) []Feed {
	filename := getFeedsFilename(username)
	file, err := os.Open(filename)
	if err != nil {
		// Si no existe el archivo de feeds del usuario, crear algunos feeds de prueba
		return []Feed{
			{"https://feeds.feedburner.com/oreilly/radar", true},
			{"https://rss.cnn.com/rss/edition.rss", true},
		}
	}
	defer file.Close()

	var feeds []Feed
	json.NewDecoder(file).Decode(&feeds)
	return feeds
}

func saveFeed(feed Feed) error {
	return saveFeedForUser(feed, "")
}

func saveFeedForUser(feed Feed, username string) error {
	log.Printf("💾 Attempting to save feed for user '%s': %s", username, feed.URL)

	feeds := loadFeedsForUser(username)
	for _, f := range feeds {
		if f.URL == feed.URL {
			log.Printf("⏭️  Feed already exists for user '%s': %s", username, feed.URL)
			return nil
		}
	}
	feeds = append(feeds, feed)

	filename := getFeedsFilename(username)
	log.Printf("📁 Saving to file: %s", filename)

	file, err := os.Create(filename)
	if err != nil {
		log.Printf("❌ Error creating file %s: %v", filename, err)
		return err
	}
	defer file.Close()

	if err := json.NewEncoder(file).Encode(feeds); err != nil {
		log.Printf("❌ Error encoding feeds to %s: %v", filename, err)
		return err
	}

	log.Printf("✅ Successfully saved feed to %s", filename)
	return nil
}

// Función para guardar toda la lista de feeds
func saveFeeds(feeds []Feed) error {
	return saveFeedsForUser(feeds, "")
}

func saveFeedsForUser(feeds []Feed, username string) error {
	filename := getFeedsFilename(username)
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	return json.NewEncoder(file).Encode(feeds)
}

// Función para verificar si un feed está accesible
func fetchFeed(feedURL string) (*gofeed.Feed, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 15 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
	}

	fp := gofeed.NewParser()
	fp.Client = client

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	return fp.ParseURLWithContext(feedURL, ctx)
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

func getCachedOrFetch(feedURL string) []Article {
	globalCache.mutex.RLock()
	cached, exists := globalCache.feeds[feedURL]
	globalCache.mutex.RUnlock()
	if exists && time.Since(cached.LastFetch) < CACHE_DURATION {
		log.Printf("🟢 Cache HIT para %s (edad: %v)", feedURL, time.Since(cached.LastFetch))
		return cached.Articles
	}
	log.Printf("🔴 Cache MISS para %s - fetching...", feedURL)
	articles := fetchFeedArticles(feedURL)
	globalCache.mutex.Lock()
	globalCache.feeds[feedURL] = CachedFeed{
		Articles:  articles,
		LastFetch: time.Now(),
		URL:       feedURL,
	}
	globalCache.mutex.Unlock()
	return articles
}

func renderHomePage(w http.ResponseWriter, data TemplateData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	html := `<!DOCTYPE html>
<html lang="es">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>ANCAP - ` + time.Now().Format("15:04:05") + ` [v2.2]</title>
    <style>
        @font-face {
            font-family: 'JetBrains Mono';
            src: url('/static/fonts/JetBrainsMonoNerdFont-Regular.woff2') format('woff2'),
                 url('/static/fonts/JetBrainsMono/JetBrainsMonoNerdFont-Regular.ttf') format('truetype');
            font-weight: 400;
            font-style: normal;
            font-display: swap;
        }
        @font-face {
            font-family: 'JetBrains Mono';
            src: url('/static/fonts/JetBrainsMono/JetBrainsMonoNerdFont-Light.ttf') format('truetype');
            font-weight: 300;
            font-style: normal;
            font-display: swap;
        }
        body { 
            background: #000; 
            color: #00ff00; 
            font-family: 'JetBrains Mono', 'Courier New', monospace; 
            font-size: 14px; 
            font-weight: 300;
            margin: 0; 
            padding: 20px;
            line-height: 1.2;
            display: flex;
            justify-content: center;
        }
        
        .container { 
            max-width: 720px; 
            margin: 0; 
            text-align: left;
            width: 100%;
            padding-top: 130px;
            overflow: hidden;
            position: relative;
        }
        .main-header {
            position: fixed;
            top: 0;
            left: 50%;
            transform: translateX(-50%);
            width: 100%;
            max-width: 720px;
            background: #000;
            z-index: 1000;
            margin-bottom: 20px;
            padding: 10px 20px;
            border-bottom: 1px solid #333;
            box-sizing: border-box;
            box-shadow: 0 2px 10px rgba(0, 0, 0, 0.8);
        }
        .datetime-display {
            position: absolute;
            top: 25px;
            right: 10px;
            font-size: 11px;
            color: #00ff00;
            font-family: 'JetBrains Mono', monospace;
            font-weight: 400;
            text-align: right;
            line-height: 1.2;
            text-shadow: 0 0 5px #00ff00;
        }
        .ascii-logo {
            font-size: 12px;
            color: #00ff00;
            font-family: 'Courier New', monospace;
            white-space: pre;
            line-height: 1.0;
            text-align: center;
        }
        .subtitle {
            font-size: 10px;
            color: #ffff00;
            text-align: center;
            margin-top: 5px;
        }
        .tabs {
            display: flex;
            width: 100%;
            max-width: 720px;
            margin-bottom: 20px;
            position: fixed;
            top: 100px;
            left: 50%;
            transform: translateX(-50%);
            background: #000;
            z-index: 1100;
            padding: 0 20px;
            box-sizing: border-box;
            box-shadow: 0 2px 10px rgba(0, 0, 0, 0.8);
        }
        .tabs::before {
            content: '';
            position: absolute;
            top: -20px;
            left: 0;
            right: 0;
            height: 20px;
            background: #000;
            z-index: 1101;
        }
        .tabs::after {
            content: '';
            position: absolute;
            bottom: -20px;
            left: 0;
            right: 0;
            height: 20px;
            background: #000;
            z-index: 1101;
        }
        .tab {
            flex: 1;
            text-align: center;
            padding: 10px;
            color: #00ff00;
            text-decoration: none;
            border: 1px dashed #00ff00;
            border-radius: 8px 8px 0 0;
            background: #000;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-size: 12px;
            cursor: pointer;
            margin: 2px 2px 0 2px;
            transition: all 0.3s ease;
            backdrop-filter: blur(5px);
            position: relative;
        }
        .tab:hover {
            background: #000;
            transform: scale(1.02);
        }
        .tab.tab-active {
            background: #000;
            color: #00ff00;
            font-weight: bold;
            border-color: #00ff00;
            border-bottom: 1px solid #000;
            z-index: 1001;
        }
        .tab.tab-active::after {
            content: '';
            position: absolute;
            bottom: -1px;
            left: 0;
            right: 0;
            height: 2px;
            background: #000;
            z-index: 1002;
        }
        h1 { 
            color: #00ff00; 
            margin-bottom: 20px; 
            font-size: 14px;
            text-align: left;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-weight: 300;
            line-height: 1.2;
        }
        .info { 
            margin-bottom: 20px; 
            color: #00ff00; 
            font-size: 14px;
            text-align: left;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-weight: 300;
            line-height: 1.2;
        }
        .article-line { 
            display: flex;
            align-items: center;
            padding: 2px 0;
            margin-bottom: 0px;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
            max-width: 100%;
            height: 18px;
            line-height: 1.2;
        }
        .article-line:hover {
            background: #111;
        }
        .full-line-link {
            color: #00ff00;
            text-decoration: none;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-weight: 300;
            font-size: 14px;
            line-height: 1.2;
            display: flex;
            align-items: center;
            width: 100%;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
        }
        .full-line-link:hover {
            text-decoration: none;
        }
        a {
            color: #ffff00;
            text-decoration: none;
        }
        a:hover {
            text-decoration: none;
            color: #ffff88;
        }
        .meta { 
            color: #00ff00; 
            font-size: 14px;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-weight: 300;
            flex-shrink: 0;
            white-space: nowrap;
            margin-right: 0px;
            line-height: 1.2;
        }
        .title { 
            color: #00ff00; 
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-weight: 300;
            flex-shrink: 1;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
            font-size: 14px;
            line-height: 1.2;
            margin-left: 5px;
        }
        .article-content {
            display: none;
            background: #000;
            color: #00ff00;
            padding: 15px;
            margin: 5px 0;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-size: 14px;
            line-height: 1.4;
            white-space: normal;
            word-wrap: break-word;
            overflow: hidden;
            position: relative;
        }
        .article-content.expanded {
            display: block;
        }
        .article-container {
            margin-bottom: 1px;
        }
        .article-line {
            cursor: pointer;
        }
        .article-line.selected {
            background: #222;
        }
        .article-line.read {
            color: #666 !important;
        }
        .article-line.read .meta {
            color: #666 !important;
        }
        .article-line.read .title {
            color: #666 !important;
        }
        .article-line.read .meta span {
            color: #666 !important;
        }
        
        /* Modal/Window styles */
        .modal {
            display: none;
            position: fixed;
            z-index: 1000;
            left: 0;
            top: 0;
            width: 100%;
            height: 100%;
            background: rgba(0, 0, 0, 0.8);
        }
        .modal.active {
            display: block;
        }
        .modal-content {
            background: #000;
            margin: 5% auto;
            padding: 20px;
            border: 2px solid #00ff00;
            width: 80%;
            max-width: 800px;
            color: #00ff00;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-size: 14px;
            position: relative;
            max-height: 80vh;
            overflow-y: auto;
        }
        .modal-header {
            font-size: 18px;
            font-weight: bold;
            margin-bottom: 20px;
            border-bottom: 1px solid #00ff00;
            padding-bottom: 10px;
        }
        .close {
            color: #00ff00;
            float: right;
            font-size: 28px;
            font-weight: bold;
            cursor: pointer;
            position: absolute;
            right: 15px;
            top: 10px;
        }
        .close:hover {
            color: #fff;
        }
        .config-section {
            margin: 15px 0;
            padding: 10px;
        }
        .config-label {
            display: block;
            margin: 5px 0;
        }
        .config-input, .config-select {
            background: #000;
            color: #00ff00;
            border: 1px solid #00ff00;
            font-family: 'JetBrains Mono', monospace;
            font-size: 12px;
            padding: 3px 5px;
            width: 200px;
        }
        .tab {
            position: relative;
        }
        .tab-shortcut {
            font-size: 10px;
            color: #888;
            margin-left: 5px;
        }
        
        /* Page content styles */
        .page-content {
            width: 100%;
            color: #00ff00;
            font-family: 'JetBrains Mono', 'Courier New', monospace;
            font-size: 14px;
            padding: 20px 0;
        }
        .page-header {
            font-size: 18px;
            font-weight: bold;
            margin-bottom: 20px;
            border-bottom: 1px solid #00ff00;
            padding-bottom: 10px;
            color: #00ff00;
        }
        
        /* Feed management styles */
        .feeds-list {
            margin: 10px 0;
        }
        
        /* Import section styles */
        .import-section {
            margin: 15px 0;
            padding: 15px;
            border: 1px dashed #00ff00;
            border-radius: 8px;
        }
        .upload-area {
            margin-bottom: 10px;
            text-align: center;
        }
        .upload-button {
            background: #000;
            color: #00ff00;
            border: 2px solid #00ff00;
            border-radius: 5px;
            padding: 8px 15px;
            cursor: pointer;
            font-family: 'JetBrains Mono', monospace;
            font-size: 12px;
        }
        .upload-button:hover {
            background: #00ff00;
            color: #000;
        }
        .import-button {
            background: #000;
            color: #00ff00;
            border: 2px solid #00ff00;
            border-radius: 5px;
            padding: 8px 15px;
            cursor: pointer;
            font-family: 'JetBrains Mono', monospace;
            font-size: 12px;
            width: 100%;
        }
        .import-button:hover {
            background: #00ff00;
            color: #000;
        }
        .manual-add {
            margin-top: 20px;
            padding-top: 15px;
            border-top: 1px solid #333;
        }
        .manual-add h4 {
            margin: 0 0 10px 0;
            color: #00ff00;
            font-size: 13px;
        }
        .manual-add input[type="url"] {
            background: #000;
            color: #00ff00;
            border: 1px solid #00ff00;
            border-radius: 3px;
            padding: 6px 10px;
            width: 70%;
            font-family: 'JetBrains Mono', monospace;
            font-size: 11px;
        }
        .manual-add button {
            background: #000;
            color: #00ff00;
            border: 1px solid #00ff00;
            border-radius: 3px;
            padding: 6px 10px;
            cursor: pointer;
            font-family: 'JetBrains Mono', monospace;
            font-size: 11px;
            margin-left: 5px;
        }
        .manual-add button:hover {
            background: #00ff00;
            color: #000;
        }
        .file-name {
            display: block;
            color: #888;
            font-size: 11px;
            margin-top: 5px;
        }
        .feeds-management {
            margin: 15px 0;
        }
        .feeds-stats {
            margin-bottom: 10px;
            color: #888;
            font-size: 12px;
        }
        .feeds-stats span {
            margin-right: 15px;
        }
        .management-actions button {
            background: #000;
            color: #00ff00;
            border: 1px solid #00ff00;
            border-radius: 3px;
            padding: 6px 12px;
            cursor: pointer;
            font-family: 'JetBrains Mono', monospace;
            font-size: 11px;
            margin-right: 10px;
        }
        .management-actions button:hover {
            background: #00ff00;
            color: #000;
        }
        
        .feed-item {
            display: flex;
            align-items: center;
            padding: 8px 10px;
            margin: 2px 0;
            background: #000;
            cursor: pointer;
            font-family: 'JetBrains Mono', monospace;
            font-size: 13px;
            border-left: 3px solid transparent;
        }
        .feed-item.selected {
            background: #001100;
            border-left: 3px solid #00ff00;
        }
        .feed-item.broken {
            color: #ff4444;
            background: #110000;
        }
        .feed-item.checking {
            color: #ffaa00;
            background: #111100;
        }
        .feed-item.working {
            color: #00ff00;
        }
        .feed-status {
            width: 60px;
            margin-right: 10px;
            font-size: 11px;
            color: #888;
        }
        .feed-title {
            flex: 1;
            margin-right: 10px;
        }
        .feed-url {
            color: #888;
            font-size: 11px;
            max-width: 300px;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
        }
        .feeds-section-header {
            color: #00ff00;
            font-weight: bold;
            margin: 15px 0 5px 0;
            font-size: 14px;
        }
    </style>
    
    <!-- JAVASCRIPT PROBLEMÁTICO COMENTADO TEMPORALMENTE 
    <script>
    /*
            console.log('toggleArticle called:', index, event);
            if (event) {
                // Si es doble clic o Ctrl+clic, abrir enlace
                if (event.detail === 2 || event.ctrlKey) {
                    window.open(articles[index].link, '_blank');
                    return;
                }
                event.preventDefault();
                event.stopPropagation();
            }
            
            const content = document.getElementById('content-' + index);
            const isCurrentlyExpanded = content.classList.contains('expanded');
            
            // Cerrar todos los artículos
            document.querySelectorAll('.article-content').forEach(el => {
                el.classList.remove('expanded');
            });
            
            if (!isCurrentlyExpanded) {
                // Abrir el artículo
                content.classList.add('expanded');
                selectedIndex = index;
                currentArticleIndex = index;
                
                // Marcar artículo como leído y agregarlo al stack
                const articleLine = document.querySelector('.article-line[onclick*="toggleArticle(' + index + ',"]');
                if (articleLine) {
                    articleLine.classList.add('read');
                    articles[index].isRead = true;
                    
                    // Si el artículo ya está en el stack, actualizar posición
                    const existingPos = readStack.indexOf(index);
                    if (existingPos !== -1) {
                        currentStackPosition = existingPos;
                    } else {
                        // Si es nuevo, agregarlo al stack
                        addToReadStack(index);
                    }
                }
                
                // Cargar contenido completo
                loadArticleContent(index);
                
                // Scroll al artículo con offset para evitar el header
                const articleElement = document.querySelector('.article-line[onclick*="toggleArticle(' + index + ',"]');
                if (articleElement) {
                    const rect = articleElement.getBoundingClientRect();
                    const offset = 160; // Offset aumentado para no cortar tipografías
                    window.scrollTo({
                        top: window.scrollY + rect.top - offset,
                        behavior: 'smooth'
                    });
                }
            } else {
                // Solo cerrar sin mover
                selectedIndex = index;
            }
        }
        

        
        let readStack = []; // Stack de artículos leídos en orden de lectura
        let currentStackPosition = 0; // Posición actual en el stack
        
        function addToReadStack(index) {
            // Remover del stack si ya existe
            readStack = readStack.filter(i => i !== index);
            // Agregar al principio del stack
            readStack.unshift(index);
            // Resetear posición al principio
            currentStackPosition = 0;
        }
        
        function markAsRead(index) {
            if (currentPage === 'feeds') {
                const articleLine = document.querySelector('.article-line[onclick*="toggleArticle(' + index + ',"]');
                if (articleLine) {
                    articleLine.classList.add('read');
                    articles[index].isRead = true;
                }
            } else {
                // Para SAVED/LOVED, marcar el contenedor correspondiente
                const containers = document.querySelectorAll('.article-container');
                if (containers[index]) {
                    containers[index].classList.add('read');
                    // Buscar el artículo original en la lista para marcarlo
                    const link = containers[index].querySelector('.full-line-link')?.getAttribute('data-link');
                    if (link) {
                        const originalIndex = articles.findIndex(a => a.link === link);
                        if (originalIndex >= 0) {
                            articles[originalIndex].isRead = true;
                        }
                    }
                }
            }
        }
        
        function getNextUnreadArticle() {
            for (let i = 0; i < articles.length; i++) {
                if (!articles[i].isRead) {
                    return i;
                }
            }
            return -1;
        }
        
        function getPreviousReadArticle() {
            // Moverse hacia adelante en el stack (hacia artículos más antiguos)
            if (currentStackPosition < readStack.length - 1) {
                currentStackPosition++;
                return readStack[currentStackPosition];
            }
            return -1;
        }
        
        function moveToNextInStack() {
            // Para navegación J - moverse hacia atrás en el stack (artículos más nuevos)
            if (currentStackPosition > 0) {
                currentStackPosition--;
                return readStack[currentStackPosition];
            }
            return -1;
        }
        

        
        // Función mejorada para cargar contenido completo del artículo
        function loadArticleContent(index) {
            const article = articles[index];
            const content = document.getElementById('content-' + index);
            
            content.innerHTML = 'Loading full content...';
            
            // Intentar obtener el contenido completo del artículo
            fetchFullArticleContent(article.link)
                .then(fullContent => {
                    displayArticleContent(content, article, fullContent, index);
                })
                .catch(() => {
                    // Fallback al contenido de descripción
                    displayArticleContent(content, article, null, index);
                });
        }
        
        // Función para obtener contenido completo del artículo
        async function fetchFullArticleContent(url) {
            try {
                // Intentar hacer scraping del contenido completo
                const response = await fetch('/api/scrape-article', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                    },
                    body: JSON.stringify({ url: url })
                });
                
                if (response.ok) {
                    const data = await response.json();
                    return data.content;
                } else {
                    throw new Error('No se pudo obtener el contenido completo');
                }
            } catch (error) {
                console.log('Fallback a descripción RSS:', error);
                throw error;
            }
        }
        
        // Función para mostrar el contenido del artículo
        function displayArticleContent(content, article, fullContent, index) {
            // Usar contenido completo si está disponible, sino usar descripción
            let articleText = fullContent || article.description || 'Content not available.';
            
            // Función para formatear texto mejorado
            function formatText(text) {
                // Primero preservar elementos de bloque convirtiéndolos a marcadores temporales
                text = text.replace(/<\/p>/gi, '|||PARAGRAPH|||');
                text = text.replace(/<br\s*\/?>/gi, '|||BREAK|||');
                text = text.replace(/<\/div>/gi, '|||PARAGRAPH|||');
                text = text.replace(/<\/h[1-6]>/gi, '|||PARAGRAPH|||');
                
                // Limpiar etiquetas HTML
                text = text.replace(/<[^>]*>/g, '');
                
                // Decodificar entidades HTML comunes
                text = text.replace(/&nbsp;/g, ' ');
                text = text.replace(/&amp;/g, '&');
                text = text.replace(/&lt;/g, '<');
                text = text.replace(/&gt;/g, '>');
                text = text.replace(/&quot;/g, '"');
                text = text.replace(/&#39;/g, "'");
                text = text.replace(/&apos;/g, "'");
                text = text.replace(/&[^;]+;/g, ' ');
                
                // Restaurar marcadores como saltos de párrafo
                text = text.replace(/\|\|\|PARAGRAPH\|\|\|/g, '\n\n');
                text = text.replace(/\|\|\|BREAK\|\|\|/g, '\n');
                
                // Detectar y crear párrafos automáticamente
                // Buscar oraciones que terminan con punto seguido de mayúscula
                text = text.replace(/\.\s+([A-ZÁÉÍÓÚÑÜ])/g, '.\n\n$1');
                
                // Limpiar espacios múltiples
                text = text.replace(/\s+/g, ' ');
                
                // Limpiar saltos de línea excesivos pero mantener párrafos
                text = text.replace(/\n\s*\n\s*\n+/g, '\n\n');
                text = text.trim();
                
                return text;
            }
            
            // Aplicar formateo mejorado
            articleText = formatText(articleText);
            
            // Convertir a HTML manteniendo la estructura de párrafos
            if (articleText.includes('\n\n')) {
                // Si hay párrafos dobles, tratarlos como párrafos separados
                const paragraphs = articleText.split('\n\n').filter(p => p.trim());
                articleText = paragraphs.map(p => '<p style="margin-bottom: 15px; line-height: 1.6; text-align: left;">' + p.replace(/\n/g, '<br>') + '</p>').join('');
            } else {
                // Si no hay párrafos dobles, solo reemplazar saltos de línea simples
                articleText = '<p style="margin-bottom: 15px; line-height: 1.6; text-align: left;">' + articleText.replace(/\n/g, '<br>') + '</p>';
            }
            
            // Determinar posición de botones según configuración
            const buttonsPosition = localStorage.getItem('buttonsPosition') || 'right';
            
            const buttonStyle = buttonsPosition === 'right' ? 
                'position: absolute; right: 15px; top: 15px; text-align: right;' :
                'position: absolute; left: 15px; top: 15px; text-align: left;';
            
            const buttonsHTML = '<div style="' + buttonStyle + '">' +
                '<div id="saved-btn-' + index + '" onclick="toggleSaved(' + index + ', event)" style="color: ' + (article.isSaved ? '#FFD700' : '#00ff00') + '; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">' + (article.isSaved ? '[SAVED]' : '[SAVE S]') + '</div>' +
                '<div id="loved-btn-' + index + '" onclick="toggleLoved(' + index + ')" style="color: ' + (article.isLoved ? '#FF69B4' : '#00ff00') + '; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">' + (article.isLoved ? '[LOVED]' : '[LOVE L]') + '</div>' +
                '<div onclick="shareArticle(' + index + ')" style="color: #00ff00; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">[SHARE C]</div>' +
                '<div onclick="window.open(\'' + article.link + '\', \'_blank\')" style="color: #ffff00; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px;">[ORIGINAL O]</div>' +
                '</div>';
            
            // Extraer y procesar imágenes del contenido original
            let imageHTML = '';
            const originalDescription = article.description || '';
            
            if (originalDescription && originalDescription.trim().length > 0) {
                const imgRegex = /<img[^>]+src=["\']([^"\']+)["\'][^>]*>/gi;
                let match;
                
                const images = [];
                while ((match = imgRegex.exec(originalDescription)) !== null && images.length < 3) {
                    const imgSrc = match[1];
                    if (imgSrc && (imgSrc.startsWith('http://') || imgSrc.startsWith('https://') || imgSrc.startsWith('//'))) {
                        images.push(imgSrc);
                    }
                }
                
                if (images.length > 0) {
                    imageHTML = '<div style="margin-bottom: 15px; text-align: left;">';
                    images.forEach(imgSrc => {
                        imageHTML += '<img src="' + imgSrc + '" style="max-width: 280px; max-height: 200px; margin: 5px 10px 5px 0; border: 1px solid #333; object-fit: cover; display: none;" onerror="this.style.display=\'none\'" onload="this.style.display=\'inline-block\'">';
                    });
                    imageHTML += '</div>';
                }
            }
            
            const contentPadding = buttonsPosition === 'right' ? 
                'padding-right: 120px;' : 
                'padding-left: 120px;';
            
            content.innerHTML = buttonsHTML +
                              '<div style="' + contentPadding + '">' +
                              '<div style="margin-bottom: 10px; color: #888; font-size: 12px;">' + article.date + ' | ' + article.source + '</div>' +
                              '<div style="margin-bottom: 15px;"><a href="' + article.link + '" target="_blank" style="color: #ffff00; text-decoration: none;">' + article.title + '</a></div>' +
                              (imageHTML || '') +
                              '<div style="line-height: 1.6; color: #ccc; text-align: justify;">' + articleText + '</div>' +
                              (fullContent ? '<div style="margin-top: 15px; padding-top: 10px; border-top: 1px solid #333; color: #888; font-size: 11px;">FULL CONTENT LOADED</div>' : '<div style="margin-top: 15px; padding-top: 10px; border-top: 1px solid #333; color: #888; font-size: 11px;">RSS feed content. Double click on the line to view the complete article on the original site</div>') +
                              '</div>';
        }
        
        document.addEventListener('DOMContentLoaded', function() {
            console.log('=== DOM Content Loaded - Initializing ===');
            
            // Agregar event listener para navegación por teclado
            document.addEventListener('keydown', function(event) {
                console.log('Key pressed:', event.code, event.key);
                console.log('Current page:', currentPage);
                console.log('Selected index:', selectedIndex);
            // Usar la variable global currentPage en lugar de buscar en el DOM
            let totalArticles;
            let articleContainers;
            if (currentPage === 'feeds') {
                articleContainers = document.querySelectorAll('#feeds-content .article-container');
                totalArticles = articleContainers.length;
            } else if (currentPage === 'saved') {
                articleContainers = document.querySelectorAll('#saved-articles-list .article-container');
                totalArticles = articleContainers.length;
            } else if (currentPage === 'loved') {
                articleContainers = document.querySelectorAll('#loved-articles-list .article-container');
                totalArticles = articleContainers.length;
            } else {
                totalArticles = 0;
                articleContainers = [];
            }
            
            const expandedArticle = document.querySelector('.article-content.expanded');
            
            // Evitar atajos si hay un input activo
            if (event.target.tagName === 'INPUT' || event.target.tagName === 'TEXTAREA') {
                return;
            }
            
            switch(event.code) {
                case 'Space':
                    event.preventDefault();
                    
                    // Space = abrir/cerrar el artículo seleccionado
                    if (selectedIndex >= 0 && totalArticles > 0) {
                        if (currentPage === 'feeds') {
                            toggleArticle(selectedIndex);
                        } else if (currentPage === 'saved' || currentPage === 'loved') {
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                const articleLine = container.querySelector('.article-line');
                                if (articleLine) {
                                    const onclickAttr = articleLine.getAttribute('onclick');
                                    const match = onclickAttr.match(/toggleArticleContent\(this, '([^']+)'/);
                                    if (match) {
                                        const link = match[1];
                                        const prefix = currentPage === 'saved' ? 'content-saved-' : 'content-loved-';
                                        const contentId = prefix + btoa(link).replace(/=/g, '').substring(0, 10);
                                        toggleArticleGeneric(container, contentId, link);
                                    }
                                }
                            }
                        }
                    }
                    break;
                    
                case 'ArrowDown':
                    event.preventDefault();
                    // Flecha abajo = scroll hacia abajo
                    if (expandedArticle) {
                        const scrollAmount = window.innerHeight * 0.8;
                        window.scrollBy({
                            top: scrollAmount,
                            behavior: 'smooth'
                        });
                    }
                    break;
                    
                case 'KeyJ':
                    event.preventDefault();
                    if (currentPage === 'feeds') {
                        // Para FEEDS: lógica original
                        if (expandedArticle) {
                            let nextIndex = moveToNextInStack();
                            if (nextIndex === -1) {
                                nextIndex = getNextUnreadArticle();
                            }
                            if (nextIndex !== -1) {
                                toggleArticle(nextIndex, false);
                            }
                        } else {
                            if (selectedIndex < totalArticles - 1) {
                                selectedIndex++;
                                currentArticleIndex = selectedIndex;
                                highlightSelected();
                            }
                        }
                    } else if (currentPage === 'saved' || currentPage === 'loved') {
                        // Para SAVED/LOVED: nueva lógica unificada
                        const articleLines = document.querySelectorAll('#' + currentPage + '-articles-list .article-line');
                        
                        if (expandedArticle) {
                            // Si hay un artículo abierto, cerrarlo y marcar como leído
                            const currentLine = document.querySelector('.article-line.article-selected');
                            if (currentLine) {
                                const content = currentLine.parentElement.querySelector('.article-content');
                                if (content) {
                                    content.style.display = 'none';
                                }
                                currentLine.classList.remove('article-selected');
                                
                                // Obtener el link para remover de favoritos (marcar como leído)
                                const onclickAttr = currentLine.getAttribute('onclick');
                                if (onclickAttr) {
                                    const match = onclickAttr.match(/toggleArticleContent\(this, '([^']+)'/);
                                    if (match) {
                                        console.log('Marking as read and removing:', match[1]);
                                        if (currentPage === 'saved') {
                                            removeFavoriteFromSaved(match[1]);
                                        } else {
                                            removeFavoriteFromLoved(match[1]);
                                        }
                                    }
                                }
                            }
                            expandedArticle = null;
                        } else {
                            // Navegar al siguiente artículo sin expandir
                            if (selectedIndex < articleLines.length - 1) {
                                selectedIndex++;
                                highlightSelected();
                            }
                        }
                    }
                    break;
                    
                case 'ArrowUp':
                    event.preventDefault();
                    // Flecha arriba = scroll hacia arriba
                    if (expandedArticle) {
                        const scrollAmount = window.innerHeight * 0.8;
                        window.scrollBy({
                            top: -scrollAmount,
                            behavior: 'smooth'
                        });
                    }
                    break;
                    
                case 'KeyK':
                    event.preventDefault();
                    if (expandedArticle) {
                        if (currentPage === 'feeds') {
                            // Para FEEDS: usar el stack system
                            const prevIndex = getPreviousReadArticle();
                            if (prevIndex !== -1) {
                                toggleArticle(prevIndex, false);
                            } else {
                                window.scrollBy({
                                    top: -window.innerHeight * 0.5,
                                    behavior: 'smooth'
                                });
                            }
                        } else {
                            // Para SAVED/LOVED: navegación simple al anterior
                            if (selectedIndex > 0) {
                                selectedIndex--;
                                const containers = document.querySelectorAll('.article-container');
                                if (containers[selectedIndex]) {
                                    // Cerrar todos los artículos
                                    document.querySelectorAll('.article-content').forEach(el => {
                                        el.classList.remove('expanded');
                                    });
                                    // Abrir el seleccionado
                                    const content = containers[selectedIndex].querySelector('.article-content');
                                    if (content) {
                                        content.classList.add('expanded');
                                        markAsRead(selectedIndex); // Marcar como leído
                                        containers[selectedIndex].scrollIntoView({
                                            behavior: 'smooth',
                                            block: 'start'
                                        });
                                    }
                                }
                                highlightSelected();
                            }
                        }
                    } else {
                        // Navegación normal
                        if (selectedIndex > 0) {
                            selectedIndex--;
                            currentArticleIndex = selectedIndex;
                            highlightSelected();
                        } else if (selectedIndex === -1 && totalArticles > 0) {
                            selectedIndex = 0;
                            currentArticleIndex = 0;
                            highlightSelected();
                        }
                    }
                    break;
                    break;
                    
                case 'Enter':
                    event.preventDefault();
                    if (selectedIndex >= 0 && totalArticles > 0) {
                        // Obtener el enlace según la página
                        let link;
                        if (currentPage === 'feeds') {
                            link = articles[selectedIndex].link;
                        } else if (currentPage === 'saved' || currentPage === 'loved') {
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                link = container.querySelector('.full-line-link').getAttribute('data-link');
                            }
                        }
                        if (link) {
                            window.open(link, '_blank');
                        }
                    }
                    break;
                    
                case 'KeyS':
                    event.preventDefault();
                    if (selectedIndex >= 0 && totalArticles > 0) {
                        // Obtener el artículo actual según la página
                        if (currentPage === 'feeds') {
                            toggleSaved(selectedIndex);
                        } else if (currentPage === 'saved' || currentPage === 'loved') {
                            // Para saved/loved, obtenemos el artículo del DOM
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                const link = container.querySelector('.full-line-link').getAttribute('data-link');
                                const originalIndex = articles.findIndex(a => a.link === link);
                                if (originalIndex >= 0) {
                                    toggleSaved(originalIndex);
                                }
                            }
                        }
                    }
                    break;
                    
                case 'KeyL':
                    event.preventDefault();
                    if (selectedIndex >= 0 && totalArticles > 0) {
                        // Obtener el artículo actual según la página
                        if (currentPage === 'feeds') {
                            toggleLoved(selectedIndex);
                        } else if (currentPage === 'saved' || currentPage === 'loved') {
                            // Para saved/loved, obtenemos el artículo del DOM
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                const link = container.querySelector('.full-line-link').getAttribute('data-link');
                                const originalIndex = articles.findIndex(a => a.link === link);
                                if (originalIndex >= 0) {
                                    toggleLoved(originalIndex);
                                }
                            }
                        }
                    }
                    break;
                    
                case 'KeyC':
                    event.preventDefault();
                    if (selectedIndex >= 0) {
                        shareArticle(selectedIndex);
                    }
                    break;
                    
                case 'KeyO':
                    event.preventDefault();
                    if (selectedIndex >= 0 && totalArticles > 0) {
                        // Obtener el enlace según la página y abrirlo
                        let link;
                        if (currentPage === 'feeds') {
                            link = articles[selectedIndex].link;
                        } else if (currentPage === 'saved' || currentPage === 'loved') {
                            const container = articleContainers[selectedIndex];
                            if (container) {
                                link = container.querySelector('.full-line-link').getAttribute('data-link');
                            }
                        }
                        if (link) {
                            window.open(link, '_blank');
                        }
                    }
                    break;
                    
                case 'Home':
                    event.preventDefault();
                    selectedIndex = 0;
                    highlightSelected();
                    break;
                    
                case 'End':
                    event.preventDefault();
                    selectedIndex = totalArticles - 1;
                    highlightSelected();
                    break;
                    
                case 'Escape':
                    event.preventDefault();
                    // Cerrar todos los artículos expandidos pero mantener la posición
                    document.querySelectorAll('.article-content').forEach(el => {
                        el.classList.remove('expanded');
                    });
                    // Mantener el selectedIndex y la posición - NO volver a feeds automáticamente
                    highlightSelected();
                    break;
                    
                case 'F1':
                    event.preventDefault();
                    showPage('feeds');
                    break;
                    
                case 'F2':
                    event.preventDefault();
                    showPage('saved');
                    break;
                    
                case 'F3':
                    event.preventDefault();
                    showPage('loved');
                    break;
                    
                case 'F4':
                    event.preventDefault();
                    showPage('config');
                    break;
            }
            
            // Manejar navegación en config
            handleConfigKeyDown(event);
            }); // Cierre del event listener de keydown
            
            // Función para actualizar fecha y hora
            function updateDateTime() {
                const now = new Date();
                const dateOptions = { 
                    weekday: 'short', 
                    year: 'numeric', 
                    month: 'short', 
                    day: 'numeric' 
                };
                const timeOptions = { 
                    hour: '2-digit', 
                    minute: '2-digit', 
                    second: '2-digit',
                    hour12: false
                };
                
                const dateElement = document.getElementById('current-date');
                const timeElement = document.getElementById('current-time');
                
                if (dateElement && timeElement) {
                    dateElement.textContent = now.toLocaleDateString('es-ES', dateOptions);
                    timeElement.textContent = now.toLocaleTimeString('es-ES', timeOptions);
                }
            }
            
            // Actualizar inmediatamente
            updateDateTime();
            
            // Actualizar cada segundo
            setInterval(updateDateTime, 1000);
            
            // Recopilar información de artículos
            document.querySelectorAll('.article-container').forEach((container, index) => {
                const span = container.querySelector('.full-line-link');
                const meta = container.querySelector('.meta').textContent;
                const title = container.querySelector('.title').textContent;
                
                const parts = meta.split(' | ');
                articles.push({
                    title: title,
                    date: parts[0] || '',
                    source: parts[1] || '',
                    link: span.getAttribute('data-link'),
                    description: span.getAttribute('data-description') || '',
                    isRead: false,
                    isSaved: false,
                    isLoved: false
                });
            });
            
            // Actualizar contador de feeds
            const feedsCount = document.getElementById('feeds-count');
            if (feedsCount) {
                feedsCount.textContent = articles.length > 0 ? '(' + articles.length + ')' : '';
            }
            
            // Inicializar selección en el primer artículo
            if (articles.length > 0) {
                selectedIndex = 0;
                highlightSelected();
            }
            
            // Cargar configuración de posición de botones
            const savedPosition = localStorage.getItem('buttonsPosition') || 'right';
            const buttonsSelect = document.getElementById('buttonsConfig');
            const buttonsModalSelect = document.getElementById('buttonsConfigModal');
            if (buttonsSelect) {
                buttonsSelect.value = savedPosition;
            }
            if (buttonsModalSelect) {
                buttonsModalSelect.value = savedPosition;
            }
            
            // Configurar handlers para importación
            setupImportHandlers();
            
        }); // Cierre del DOMContentLoaded principal
        
        function highlightSelected() {
            // Quitar highlight previo de todas las páginas
            document.querySelectorAll('.article-line').forEach(el => {
                el.classList.remove('selected');
            });
            
            // Agregar highlight al seleccionado
            if (selectedIndex >= 0) {
                // Buscar en la página activa usando currentPage
                let lines;
                
                if (currentPage === 'feeds') {
                    lines = document.querySelectorAll('#feeds-content .article-line');
                } else if (currentPage === 'saved') {
                    lines = document.querySelectorAll('#saved-articles-list .article-line');
                } else if (currentPage === 'loved') {
                    lines = document.querySelectorAll('#loved-articles-list .article-line');
                }
                
                if (lines && lines[selectedIndex]) {
                    lines[selectedIndex].classList.add('selected');
                    
                    // Calcular la posición para que el elemento seleccionado esté en la primera línea visible
                    const element = lines[selectedIndex];
                    const headerHeight = 140; // Altura del header + pestañas ajustada
                    
                    // Obtener la posición absoluta del elemento en la página
                    const elementTop = element.offsetTop;
                    
                    // Scroll para que el elemento esté exactamente debajo del header (primera línea visible)
                    const scrollTarget = elementTop - headerHeight;
                    
                    window.scrollTo({
                        top: Math.max(0, scrollTarget), // Evitar scroll negativo
                        behavior: 'smooth'
                    });
                }
            }
        }
        
        function toggleArticleGeneric(container, contentId, link, forceOpen = false) {
            let content = container.querySelector('.article-content');
            if (!content) {
                content = document.createElement('div');
                content.className = 'article-content';
                content.id = contentId;
                container.appendChild(content);
            }
            
            const isCurrentlyExpanded = content.classList.contains('expanded');
            
            // Cerrar todos los artículos
            document.querySelectorAll('.article-content').forEach(el => {
                el.classList.remove('expanded');
            });
            
            // Abrir el seleccionado si no estaba abierto O si forceOpen es true
            if (!isCurrentlyExpanded || forceOpen) {
                content.classList.add('expanded');
                
                // Scroll para que el artículo aparezca después del header fijo
                setTimeout(() => {
                    container.scrollIntoView({ 
                        behavior: 'smooth', 
                        block: 'start' 
                    });
                    // Ajuste adicional para el header fijo
                    window.scrollBy(0, -140);
                }, 100);
                
                // Cargar contenido si no está cargado
                if (!content.innerHTML.trim()) {
                    content.innerHTML = 'Loading content...';
                    loadArticleContentGeneric(content, container, link);
                }
            }
        }
        
        function loadArticleContentGeneric(content, container, link) {
            // Obtener datos del artículo desde el DOM
            const titleElement = container.querySelector('.title');
            const metaElement = container.querySelector('.meta');
            const linkElement = container.querySelector('.full-line-link');
            
            const title = titleElement ? titleElement.textContent : 'Sin título';
            const meta = metaElement ? metaElement.textContent : '';
            const description = linkElement ? linkElement.getAttribute('data-description') || '' : '';
            
            const metaParts = meta.split(' | ');
            const date = metaParts[0] || '';
            const source = metaParts[1] || '';
            
            setTimeout(() => {
                // Limpiar la descripción preservando estructura de párrafos
                let cleanDescription = description || 'Content not available. Double click on the line to view the complete article.';
                
                // Convertir elementos de bloque a saltos de línea antes de limpiar
                cleanDescription = cleanDescription.replace(/<\/p>/gi, '\n\n');
                cleanDescription = cleanDescription.replace(/<br\s*\/?>/gi, '\n');
                cleanDescription = cleanDescription.replace(/<\/div>/gi, '\n');
                cleanDescription = cleanDescription.replace(/<\/h[1-6]>/gi, '\n\n');
                
                // Limpiar etiquetas HTML y entidades
                cleanDescription = cleanDescription.replace(/<[^>]*>/g, '');
                cleanDescription = cleanDescription.replace(/&[^;]+;/g, ' ');
                
                // Limpiar saltos de línea excesivos pero mantener párrafos
                cleanDescription = cleanDescription.replace(/\n\s*\n\s*\n/g, '\n\n');
                cleanDescription = cleanDescription.trim();
                
                // Convertir saltos de línea a HTML para mostrar correctamente
                if (cleanDescription.includes('\n\n')) {
                    // Si hay párrafos dobles, tratarlos como párrafos separados
                    const paragraphs = cleanDescription.split('\n\n').filter(p => p.trim());
                    cleanDescription = paragraphs.map(p => '<p>' + p.replace(/\n/g, '<br>') + '</p>').join('');
                } else {
                    // Si no hay párrafos dobles, solo reemplazar saltos de línea simples
                    cleanDescription = '<p>' + cleanDescription.replace(/\n/g, '<br>') + '</p>';
                }
                
                // Determinar posición de botones según configuración
                const buttonsPosition = localStorage.getItem('buttonsPosition') || 'right';
                
                // Botones con posición configurable
                const buttonStyle = buttonsPosition === 'right' ? 
                    'position: absolute; right: 15px; top: 15px; text-align: right;' :
                    'position: absolute; left: 15px; top: 15px; text-align: left;';
                
                const buttonsHTML = '<div style="' + buttonStyle + '">' +
                    '<div onclick="toggleSavedByLink(\'' + link + '\')" style="color: #FFD700; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">[SAVED]</div>' +
                    '<div onclick="toggleLovedByLink(\'' + link + '\')" style="color: #FF69B4; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px; margin-bottom: 5px;">[FAVORITE]</div>' +
                    '<div onclick="shareArticleByLink(\'' + link + '\')" style="color: #00ff00; cursor: pointer; font-family: \'JetBrains Mono\', monospace; font-size: 12px;">[SHARE]</div>' +
                    '</div>';
                
                // NO mostrar imágenes en guardados/favoritos para evitar imágenes rotas
                // Las imágenes solo están disponibles en la vista principal de feeds
                let imageHTML = '';
                // Solo mostrar imágenes si estamos en la vista de feeds (no en guardados/favoritos)
                // donde tenemos la descripción completa
                
                // Ajustar padding según posición de botones
                const contentPadding = buttonsPosition === 'right' ? 
                    'padding-right: 100px;' : 
                    'padding-left: 100px;';
                
                content.innerHTML = buttonsHTML +
                                  '<div style="' + contentPadding + '">' +
                                  (imageHTML || '') +
                                  '<div style="margin-bottom: 10px; color: #888; font-size: 12px;">' + date + ' | ' + source + '</div>' +
                                  '<div style="margin-bottom: 10px;"><a href="' + link + '" target="_blank" style="color: #ffff00; text-decoration: none;">' + title + '</a></div>' +
                                  '<div style="line-height: 1.6; color: #ccc; text-align: justify;">' + cleanDescription + '</div>' +
                                  '</div>';
            }, 300);
        }
        
        function toggleSavedByLink(link) {
            const articleIndex = articles.findIndex(a => a.link === link);
            if (articleIndex >= 0) {
                toggleSaved(articleIndex);
                updateSavedList();
                updateLovedList();
            }
        }
        
        function toggleLovedByLink(link) {
            const articleIndex = articles.findIndex(a => a.link === link);
            if (articleIndex >= 0) {
                toggleLoved(articleIndex);
                updateSavedList();
                updateLovedList();
            }
        }
        
        function shareArticleByLink(link) {
            if (navigator.share) {
                navigator.share({
                    title: 'Interesting Article',
                    url: link
                });
            } else {
                // Fallback: copy to clipboard
                navigator.clipboard.writeText(link).then(() => {
                    alert('Link copied to clipboard');
                });
            }
        }
        
        function toggleSaved(index, event) {
            if (event) {
                event.stopPropagation();
            }
            
            const article = articles[index];
            const button = document.getElementById('saved-btn-' + index);
            
            // Alternar estado (simulado - aquí harías fetch al servidor)
            article.isSaved = !article.isSaved;
            
            if (button) {
                button.textContent = article.isSaved ? '[SAVED]' : '[SAVE]';
                button.style.color = article.isSaved ? '#FFD700' : '#00ff00';
            }
            
            // Actualizar lista de guardados
            updateSavedList();
            
            console.log('Alternado guardado para:', article.title);
        }
        
        function toggleLoved(index) {
            const article = articles[index];
            const button = document.getElementById('loved-btn-' + index);
            
            // Alternar estado (simulado - aquí harías fetch al servidor)
            article.isLoved = !article.isLoved;
            
            if (button) {
                button.textContent = article.isLoved ? '[LOVED]' : '[LOVE]';
                button.style.color = article.isLoved ? '#FF69B4' : '#00ff00';
            }
            
            // Actualizar lista de favoritos
            updateLovedList();
            
            console.log('Alternado favorito para:', article.title);
        }
        
        function updateLovedList() {
            const lovedList = document.getElementById('loved-articles-list');
            
            // Obtener artículos favoritos desde el servidor
            fetch('/api/favorites')
                .then(response => response.json())
                .then(favorites => {
                    // Actualizar contador en la pestaña
                    const lovedCount = document.getElementById('loved-count');
                    if (lovedCount) {
                        lovedCount.textContent = favorites.length > 0 ? '(' + favorites.length + ')' : '';
                    }
                    
                    if (favorites.length === 0) {
                        lovedList.innerHTML = '<p style="color: #888; text-align: center; padding: 40px;">No favorite articles yet.</p>';
                        return;
                    }
                    
                    let listHTML = '';
                    favorites.forEach((fav, index) => {
                        const contentId = 'content-loved-' + index;
                        // Limpiar descripción
                        let cleanDescription = (fav.Description || 'No hay descripción disponible.');
                        cleanDescription = cleanDescription.replace(/<[^>]*>/g, '');
                        cleanDescription = cleanDescription.replace(/&[^;]+;/g, ' ');
                        cleanDescription = cleanDescription.replace(/\s+/g, ' ').trim();
                        cleanDescription = cleanDescription.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
                        
                        listHTML += '<div class="article-container">' +
                                   '<div class="article-line" onclick="toggleArticleContent(this, \'' + fav.Link + '\', \'loved\')">' +
                                   '<span class="article-date">' + fav.Date + '</span>' +
                                   '<span class="article-source">' + fav.Source + '</span>' +
                                   '<span class="article-title">' + fav.Title + '</span>' +
                                   '<button type="button" class="save-button saved" onclick="event.stopPropagation(); removeFavoriteFromLoved(\'' + fav.Link + '\')">SAVED</button>' +
                                   '</div>' +
                                   '<div id="' + contentId + '" class="article-content" style="display: none;">' +
                                   '<div class="expanded-meta">' +
                                   '<span class="meta-date">' + fav.Date + '</span>' +
                                   '<span class="meta-source">' + fav.Source + '</span>' +
                                   '</div>' +
                                   '<div class="action-buttons">' +
                                   '<button type="button" class="action-save saved" onclick="removeFavoriteFromLoved(\'' + fav.Link + '\')">SAVED</button>' +
                                   '<a href="' + fav.Link + '" target="_blank" class="read-original">🔗 LEER ORIGINAL</a>' +
                                   '</div>' +
                                   '<div class="article-description">' + cleanDescription + '</div>' +
                                   '</div>' +
                                   '</div>';
                    });
                    lovedList.innerHTML = listHTML;
                })
                .catch(error => {
                    lovedList.innerHTML = '<p style="color: #ff6b6b; text-align: center; padding: 40px;">Error loading favorites.</p>';
                    console.error('Error loading favorites:', error);
                });
        }
        
        function updateSavedList() {
            const savedList = document.getElementById('saved-articles-list');
            
            // Usar la misma lógica que updateLovedList para consistencia
            fetch('/api/favorites')
                .then(response => response.json())
                .then(favorites => {
                    // Actualizar contador en la pestaña
                    const savedCount = document.getElementById('saved-count');
                    if (savedCount) {
                        savedCount.textContent = favorites.length > 0 ? '(' + favorites.length + ')' : '';
                    }
                    
                    if (favorites.length === 0) {
                        savedList.innerHTML = '<p style="color: #888; text-align: center; padding: 40px;">No saved articles yet.</p>';
                        return;
                    }
                    
                    let listHTML = '';
                    favorites.forEach((fav, index) => {
                        const contentId = 'content-saved-' + index;
                        // Limpiar descripción
                        let cleanDescription = (fav.Description || 'No hay descripción disponible.');
                        cleanDescription = cleanDescription.replace(/<[^>]*>/g, '');
                        cleanDescription = cleanDescription.replace(/&[^;]+;/g, ' ');
                        cleanDescription = cleanDescription.replace(/\s+/g, ' ').trim();
                        cleanDescription = cleanDescription.replace(/"/g, '&quot;').replace(/'/g, '&#39;');
                        
                        listHTML += '<div class="article-container">' +
                                   '<div class="article-line" onclick="toggleArticleContent(this, \'' + fav.Link + '\', \'saved\')">' +
                                   '<span class="article-date">' + fav.Date + '</span>' +
                                   '<span class="article-source">' + fav.Source + '</span>' +
                                   '<span class="article-title">' + fav.Title + '</span>' +
                                   '<button type="button" class="save-button saved" onclick="event.stopPropagation(); removeFavoriteFromSaved(\'' + fav.Link + '\')">SAVED</button>' +
                                   '</div>' +
                                   '<div id="' + contentId + '" class="article-content" style="display: none;">' +
                                   '<div class="expanded-meta">' +
                                   '<span class="meta-date">' + fav.Date + '</span>' +
                                   '<span class="meta-source">' + fav.Source + '</span>' +
                                   '</div>' +
                                   '<div class="action-buttons">' +
                                   '<button type="button" class="action-save saved" onclick="removeFavoriteFromSaved(\'' + fav.Link + '\')">SAVED</button>' +
                                   '<a href="' + fav.Link + '" target="_blank" class="read-original">🔗 LEER ORIGINAL</a>' +
                                   '</div>' +
                                   '<div class="article-description">' + cleanDescription + '</div>' +
                                   '</div>' +
                                   '</div>';
                    });
                    savedList.innerHTML = listHTML;
                })
                .catch(error => {
                    savedList.innerHTML = '<p style="color: #ff6b6b; text-align: center; padding: 40px;">Error loading saved articles.</p>';
                    console.error('Error loading saved articles:', error);
                });
        }
        
        // Funciones auxiliares para remover favoritos
        function removeFavoriteFromLoved(link) {
            removeFavoriteFromAPI(link, function() {
                updateLovedList();
            });
        }
        
        function removeFavoriteFromSaved(link) {
            removeFavoriteFromAPI(link, function() {
                updateSavedList(); 
            });
        }
        
        function removeFavoriteFromAPI(link, callback) {
            // Hacer petición al servidor para remover favorito
            // Por ahora, simular la remoción
            console.log('Removing favorite:', link);
            if (callback) callback();
        }
        
        // Función mejorada para toggle article content
        function toggleArticleContent(element, url, context) {
            const container = element.parentElement;
            const content = container.querySelector('.article-content');
            
            // Si hay otro artículo abierto, cerrarlo primero
            document.querySelectorAll('.article-line').forEach(line => {
                if (line !== element && line.classList.contains('article-selected')) {
                    const otherContent = line.parentElement.querySelector('.article-content');
                    if (otherContent) {
                        otherContent.style.display = 'none';
                    }
                    line.classList.remove('article-selected');
                }
            });
            
            if (content.style.display === 'none' || content.style.display === '') {
                // Abrir artículo
                content.style.display = 'block';
                element.classList.add('article-selected');
                
                // Actualizar el índice seleccionado según contexto
                if (context === 'saved' || context === 'loved') {
                    const articles = Array.from(container.parentElement.querySelectorAll('.article-line'));
                    selectedIndex = articles.indexOf(element);
                }
            } else {
                // Cerrar artículo
                content.style.display = 'none';
                element.classList.remove('article-selected');
                
                // Si estamos en saved o loved y presionamos J, marcar como leído
                if (context === 'saved' || context === 'loved') {
                    const link = element.querySelector('.article-title').textContent;
                    // Aquí podríamos implementar lógica de marcado como leído
                    console.log('Article read in', context, ':', link);
                }
            }
        }
                    if (button) {
                        button.textContent = '[GUARDAR]';
                        button.style.color = '#00ff00';
                    }
                }
            });
            updateSavedList();
        }
        
        function shareArticle(index) {
            const article = articles[index];
            
            if (navigator.share) {
                navigator.share({
                    title: article.title,
                    text: article.title,
                    url: article.link
                });
            } else {
                // Fallback: copy to clipboard
                navigator.clipboard.writeText(article.link).then(() => {
                    alert('Link copied to clipboard');
                });
            }
        }
        
        function changeButtonsPosition() {
            const select = document.getElementById('buttonsConfig');
            const modalSelect = document.getElementById('buttonsConfigModal');
            
            let newValue;
            if (select && select.value) {
                newValue = select.value;
                if (modalSelect) modalSelect.value = newValue;
            } else if (modalSelect && modalSelect.value) {
                newValue = modalSelect.value;
                if (select) select.value = newValue;
            }
            
            if (newValue) {
                localStorage.setItem('buttonsPosition', newValue);
                
                // Recargar contenido de artículos expandidos
                document.querySelectorAll('.article-content.expanded').forEach((content, index) => {
                    const articleIndex = parseInt(content.id.replace('content-', ''));
                    loadArticleContent(articleIndex);
                });
            }
        }
        
        console.log('=== toggleArticle definida correctamente ===');
        
        function showPage(pageName, event) {
            console.log('showPage llamada con:', pageName, event);
            console.log('Event target:', event ? event.target : 'No event');
            if (event) {
                event.preventDefault();
            }
            
            // Actualizar la página actual
            currentPage = pageName;
            
            // Ocultar todos los contenidos
            const contentIds = ['feeds-content', 'import-content', 'saved-content', 'loved-content', 'config-content'];
            contentIds.forEach(id => {
                const element = document.getElementById(id);
                console.log('Elemento', id, ':', element);
                if (element) element.style.display = 'none';
            });
            
            // Quitar clase activa de todas las pestañas
            document.querySelectorAll('.tab').forEach(tab => {
                tab.classList.remove('tab-active');
            });
            
            // Mostrar el contenido solicitado
            const targetContent = document.getElementById(pageName + '-content');
            console.log('Target content para', pageName + '-content', ':', targetContent);
            if (targetContent) {
                targetContent.style.display = 'block';
                console.log('Contenido mostrado para:', pageName);
            } else {
                console.error('No se encontró el contenido para:', pageName + '-content');
            }
            
            // Activar la pestaña correspondiente
            if (event && event.target) {
                event.target.classList.add('tab-active');
            } else {
                // Si no hay event.target, buscar la pestaña por el pageName
                const tabs = document.querySelectorAll('.tab');
                tabs.forEach(tab => {
                    if (tab.textContent.toLowerCase().includes(pageName.toLowerCase())) {
                        tab.classList.add('tab-active');
                        console.log('Tab activada:', tab.textContent);
                    }
                });
            }
            
            // Actualizar título
            document.title = 'ANCAP WEB - ' + pageName.toUpperCase();
            
            // Actualizar listas y reiniciar selección
            if (pageName === 'saved') {
                updateSavedList();
                selectedIndex = 0;
                setTimeout(highlightSelected, 100);
            } else if (pageName === 'loved') {
                updateLovedList();
                selectedIndex = 0;
                setTimeout(highlightSelected, 100);
            } else if (pageName === 'feeds') {
                selectedIndex = 0;
                setTimeout(highlightSelected, 100);
            } else if (pageName === 'config') {
                selectedIndex = -1;
                loadFeedsManagement(); // Cargar lista de feeds
            } else {
                selectedIndex = -1; // Para otras páginas sin artículos
            }
        }
        
        console.log('=== showPage definida correctamente ===');
        
        function closeAllModals() {
            // Esta función ya no es necesaria para páginas, pero la mantenemos por compatibilidad
            document.title = 'ANCAP WEB';
        }
        
        function closeModal(windowName) {
            // Ya no es necesaria, pero la mantenemos por compatibilidad
            showPage('feeds');
        }
        
        // Variables para gestión de feeds
        let feeds = [];
        let selectedFeedIndex = -1;
        
        // Función para cargar la gestión de feeds
        function loadFeedsManagement() {
            fetch('/api/feeds')
                .then(response => response.json())
                .then(data => {
                    feeds = data;
                    
                    // Renderizar lista inmediatamente con estado "checking"
                    feeds.forEach(feed => {
                        feed.isWorking = null; // null = checking
                    });
                    renderFeedsList();
                    
                    // Verificar estado en segundo plano
                    checkAllFeedsStatus();
                })
                .catch(error => {
                    console.error('Error loading feeds:', error);
                    feeds = [];
                    renderFeedsList();
                });
        }
        
        // Función para verificar el estado de todos los feeds
        function checkAllFeedsStatus() {
            let completedChecks = 0;
            const totalFeeds = feeds.length;
            
            // Verificar feeds en lotes para mejor rendimiento
            const batchSize = 5;
            for (let i = 0; i < totalFeeds; i += batchSize) {
                const batch = feeds.slice(i, i + batchSize);
                
                batch.forEach((feed, index) => {
                    const globalIndex = i + index;
                    checkFeedStatus(feed.url)
                        .then(isWorking => {
                            feeds[globalIndex].isWorking = isWorking;
                            completedChecks++;
                            
                            // Re-renderizar cuando se complete cada lote
                            if (completedChecks % batchSize === 0 || completedChecks === totalFeeds) {
                                renderFeedsList();
                            }
                        })
                        .catch(() => {
                            feeds[globalIndex].isWorking = false;
                            completedChecks++;
                            
                            // Re-renderizar cuando se complete cada lote
                            if (completedChecks % batchSize === 0 || completedChecks === totalFeeds) {
                                renderFeedsList();
                            }
                        });
                });
            }
        }
        
        // Función para verificar si un feed está funcionando
        function checkFeedStatus(url) {
            return fetch('/api/check-feed', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ url: url })
            })
            .then(response => response.json())
            .then(data => data.working)
            .catch(() => false);
        }
        
        // Función para renderizar la lista de feeds
        function renderFeedsList() {
            const feedsList = document.getElementById('feeds-list');
            if (!feedsList) return;
            
            // Separar feeds por estado
            const brokenFeeds = feeds.filter(feed => feed.isWorking === false);
            const workingFeeds = feeds.filter(feed => feed.isWorking === true);
            const checkingFeeds = feeds.filter(feed => feed.isWorking === null);
            
            let html = '';
            
            // Mostrar feeds rotos primero
            brokenFeeds.forEach((feed, index) => {
                const feedTitle = extractFeedTitle(feed.url);
                const globalIndex = feeds.indexOf(feed);
                html += '<div class="feed-item broken" data-index="' + globalIndex + '">' +
                       '<div class="feed-status">BROKEN</div>' +
                       '<div class="feed-title">' + feedTitle + '</div>' +
                       '<div class="feed-url">' + feed.url + '</div>' +
                       '</div>';
            });
            
            // Mostrar feeds en verificación
            checkingFeeds.forEach((feed, index) => {
                const feedTitle = extractFeedTitle(feed.url);
                const globalIndex = feeds.indexOf(feed);
                html += '<div class="feed-item checking" data-index="' + globalIndex + '">' +
                       '<div class="feed-status">CHECK...</div>' +
                       '<div class="feed-title">' + feedTitle + '</div>' +
                       '<div class="feed-url">' + feed.url + '</div>' +
                       '</div>';
            });
            
            // Mostrar feeds funcionando al final
            workingFeeds.forEach((feed, index) => {
                const feedTitle = extractFeedTitle(feed.url);
                const globalIndex = feeds.indexOf(feed);
                html += '<div class="feed-item working" data-index="' + globalIndex + '">' +
                       '<div class="feed-status">WORKING</div>' +
                       '<div class="feed-title">' + feedTitle + '</div>' +
                       '<div class="feed-url">' + feed.url + '</div>' +
                       '</div>';
            });
            
            feedsList.innerHTML = html;
            
            // Mantener selección si existe, o seleccionar el primero
            const feedItems = document.querySelectorAll('.feed-item');
            if (feedItems.length > 0) {
                let selectedItem = null;
                
                // Buscar si hay un item ya seleccionado
                feedItems.forEach(item => {
                    if (parseInt(item.getAttribute('data-index')) === selectedFeedIndex) {
                        selectedItem = item;
                    }
                });
                
                // Si no hay selección previa, seleccionar el primero
                if (!selectedItem) {
                    selectedItem = feedItems[0];
                    selectedFeedIndex = parseInt(selectedItem.getAttribute('data-index'));
                }
                
                selectedItem.classList.add('selected');
            }
        }
        
        // Función para extraer título del feed desde la URL
        function extractFeedTitle(url) {
            try {
                const urlObj = new URL(url);
                let title = urlObj.hostname;
                
                // Casos especiales para mejorar la legibilidad
                if (title.includes('youtube.com')) {
                    return 'YouTube Channel';
                } else if (title.includes('reddit.com')) {
                    return 'Reddit Feed';
                } else if (title.includes('news.ycombinator.com')) {
                    return 'Hacker News';
                } else if (title.includes('xkcd.com')) {
                    return 'XKCD';
                } else if (title.includes('brave.com')) {
                    return 'Brave Blog';
                } else if (title.includes('firstshowing.net')) {
                    return 'FirstShowing.net';
                }
                
                // Limpiar www. y subdominios comunes
                title = title.replace(/^www\./, '');
                return title;
            } catch {
                return 'Unknown Feed';
            }
        }
        
        // Función para eliminar un feed
        function deleteFeed(index) {
            if (index >= 0 && index < feeds.length) {
                const feedToDelete = feeds[index];
                
                fetch('/api/delete-feed', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ url: feedToDelete.url })
                })
                .then(response => response.json())
                .then(data => {
                    if (data.success) {
                        feeds.splice(index, 1);
                        // Ajustar índice seleccionado
                        if (selectedFeedIndex >= feeds.length) {
                            selectedFeedIndex = feeds.length - 1;
                        }
                        renderFeedsList();
                    } else {
                        alert('Error deleting feed: ' + data.error);
                    }
                })
                .catch(error => {
                    console.error('Error deleting feed:', error);
                    alert('Error deleting feed');
                });
            }
        }
        
        // Navegación con teclado para feeds (solo cuando estamos en config)
        function handleConfigKeyDown(event) {
            if (currentPage !== 'config') return;
            
            const feedItems = document.querySelectorAll('.feed-item');
            const totalFeeds = feedItems.length;
            
            if (totalFeeds === 0) return;
            
            switch(event.code) {
                case 'KeyJ':
                case 'ArrowDown':
                    event.preventDefault();
                    // Navegar hacia abajo en la lista visual
                    let currentVisualIndex = -1;
                    feedItems.forEach((item, index) => {
                        if (item.classList.contains('selected')) {
                            currentVisualIndex = index;
                        }
                    });
                    
                    if (currentVisualIndex < totalFeeds - 1) {
                        // Quitar selección actual
                        feedItems.forEach(item => item.classList.remove('selected'));
                        
                        // Seleccionar siguiente
                        const nextItem = feedItems[currentVisualIndex + 1];
                        nextItem.classList.add('selected');
                        selectedFeedIndex = parseInt(nextItem.getAttribute('data-index'));
                        
                        // Scroll si es necesario
                        nextItem.scrollIntoView({ behavior: 'smooth', block: 'center' });
                    }
                    break;
                    
                case 'KeyK':
                case 'ArrowUp':
                    event.preventDefault();
                    // Navegar hacia arriba en la lista visual
                    let currentVisualIndexUp = -1;
                    feedItems.forEach((item, index) => {
                        if (item.classList.contains('selected')) {
                            currentVisualIndexUp = index;
                        }
                    });
                    
                    if (currentVisualIndexUp > 0) {
                        // Quitar selección actual
                        feedItems.forEach(item => item.classList.remove('selected'));
                        
                        // Seleccionar anterior
                        const prevItem = feedItems[currentVisualIndexUp - 1];
                        prevItem.classList.add('selected');
                        selectedFeedIndex = parseInt(prevItem.getAttribute('data-index'));
                        
                        // Scroll si es necesario
                        prevItem.scrollIntoView({ behavior: 'smooth', block: 'center' });
                    }
                    break;
                    
                case 'KeyD':
                    event.preventDefault();
                    if (selectedFeedIndex >= 0 && confirm('Delete selected feed?')) {
                        deleteFeed(selectedFeedIndex);
                    }
                    break;
            }
        }
        
        function setupImportHandlers() {
            // Handler para mostrar nombre del archivo seleccionado
            const fileInput = document.getElementById('opml-file');
            if (fileInput) {
                fileInput.addEventListener('change', function() {
                    const fileName = this.files[0] ? this.files[0].name : '';
                    const fileNameSpan = document.getElementById('file-name');
                    if (fileNameSpan) {
                        fileNameSpan.textContent = fileName;
                    }
                });
            }
            
            // Handler para formulario OPML
            const opmlForm = document.getElementById('opml-upload-form');
            if (opmlForm) {
                opmlForm.addEventListener('submit', async function(e) {
                    e.preventDefault();
                    
                    if (!fileInput.files[0]) {
                        alert('Please select an OPML file');
                        return;
                    }
                    
                    const formData = new FormData();
                    formData.append('opml', fileInput.files[0]);
                    
                    try {
                        const response = await fetch('/upload-opml', {
                            method: 'POST',
                            body: formData
                        });
                        
                        if (response.ok) {
                            const result = await response.text();
                            alert('✅ ' + result);
                            fileInput.value = '';
                            document.getElementById('file-name').textContent = '';
                            switchTab('feeds');
                            setTimeout(() => window.location.reload(), 1000);
                        } else {
                            const text = await response.text();
                            alert('Error: ' + text);
                        }
                    } catch (error) {
                        alert('Error importing feeds');
                    }
                });
            }
            
            // Handler para formulario manual de feeds
            const manualForm = document.getElementById('manual-feed-form');
            if (manualForm) {
                manualForm.addEventListener('submit', async function(e) {
                    e.preventDefault();
                    
                    const feedUrl = document.getElementById('feed-url').value;
                    const formData = new FormData();
                    formData.append('url', feedUrl);
                    
                    try {
                        const response = await fetch('/add', {
                            method: 'POST',
                            body: formData
                        });
                        
                        if (response.ok) {
                            alert('✅ Feed added successfully!');
                            document.getElementById('feed-url').value = '';
                            switchTab('feeds');
                            setTimeout(() => window.location.reload(), 1000);
                        } else {
                            const text = await response.text();
                            alert('Error: ' + text);
                        }
                    } catch (error) {
                        alert('Error adding feed');
                    }
                });
            }
        }
        
        function exportFeeds() {
            window.open('/export-opml', '_blank');
        }
        
        function clearCache() {
            fetch('/clear-cache', { method: 'POST' })
                .then(response => response.text())
                .then(data => {
                    alert('✅ Cache cleared successfully');
                })
                .catch(error => {
                    alert('Error clearing cache');
                });
        }
        
        // Verificar que las funciones importantes estén disponibles al final
        console.log('=== Verificando funciones globales ===');
        console.log('toggleArticle defined:', typeof toggleArticle);
        console.log('showPage defined:', typeof showPage);
        console.log('toggleArticleContent defined:', typeof toggleArticleContent);
        */
    </script>
    <!-- FIN DEL JAVASCRIPT PROBLEMÁTICO COMENTADO -->
</head>
<body>
    <div class="container">
        <div class="main-header">
            <div class="datetime-display">
                <div id="current-date"></div>
                <div id="current-time"></div>
            </div>
            <div class="ascii-logo">
▄▀█ █▄ █ █▀▀ ▄▀█ █▀▄
█▀█ █ ▀█ █▄▄ █▀█ █▀▀
            </div>
            <div class="subtitle">» A LIBERTARIAN RSS READER «</div>
            <div style="position: absolute; top: 8px; right: 10px;">
                <a href="/logout" style="color: #ffff00; text-decoration: none; font-size: 10px;">[LOGOUT]</a>
            </div>
        </div>
        <div class="tabs">
            <a class="tab tab-active" href="#" onclick="showPage('feeds', event)">FEEDS <span id="feeds-count"></span> <span class="tab-shortcut">(F1)</span></a>
            <a class="tab" href="#" onclick="showPage('saved', event)">SAVED <span id="saved-count"></span> <span class="tab-shortcut">(F2)</span></a>
            <a class="tab" href="#" onclick="showPage('loved', event)">LOVED <span id="loved-count"></span> <span class="tab-shortcut">(F3)</span></a>
            <a class="tab" href="#" onclick="showPage('config', event)">CONFIG <span class="tab-shortcut">(F4)</span></a>
        </div>
        
        <!-- Contenido principal de feeds -->
        <div id="feeds-content" class="page-content" style="display: block;">
            <div style="height: 1px; background: #000; margin-bottom: 0px;"></div>`

	for i, article := range data.Articles {
		if i >= 50 {
			break
		}
		// Limpiar y escapar caracteres especiales para HTML/JavaScript
		description := article.Description

		// Limpiar HTML tags y contenido problemático antes de escapar
		description = strings.ReplaceAll(description, "&lt;", "<")
		description = strings.ReplaceAll(description, "&gt;", ">")
		description = strings.ReplaceAll(description, "&amp;", "&")

		// Remover contenido problemático común en feeds
		description = strings.ReplaceAll(description, "Comments\"", "")
		description = strings.ReplaceAll(description, "[...]\"", "")

		// Escapar caracteres especiales para HTML
		description = strings.ReplaceAll(description, "&", "&amp;")
		description = strings.ReplaceAll(description, "<", "&lt;")
		description = strings.ReplaceAll(description, ">", "&gt;")
		description = strings.ReplaceAll(description, `"`, "&quot;")
		description = strings.ReplaceAll(description, `'`, "&#39;")
		description = strings.ReplaceAll(description, "\n", " ")
		description = strings.ReplaceAll(description, "\r", " ")
		description = strings.ReplaceAll(description, "\t", " ")

		// Limpiar título también
		title := article.Title
		title = strings.ReplaceAll(title, "&", "&amp;")
		title = strings.ReplaceAll(title, "<", "&lt;")
		title = strings.ReplaceAll(title, ">", "&gt;")
		title = strings.ReplaceAll(title, `"`, "&quot;")
		title = strings.ReplaceAll(title, `'`, "&#39;")

		// Limitar longitud para evitar atributos muy largos, pero permitir más espacio para imágenes
		if len(description) > 2000 {
			description = description[:2000] + "..."
		}

		html += `<div class="article-container">
            <div class="article-line" onclick="toggleArticle(` + fmt.Sprintf("%d", i) + `, event)">
                <span class="full-line-link" data-link="` + article.Link + `" data-description="` + description + `">
                    <span class="meta">` + article.Date + ` | <span style="color: #ffff00;">` + article.Source + `</span> | </span>
                    <span class="title">` + title + `</span>
                </span>
            </div>
            <div id="content-` + fmt.Sprintf("%d", i) + `" class="article-content"></div>
        </div>`
	}

	html += `        </div>
        
        <!-- Saved Articles Page -->
        <div id="saved-content" class="page-content" style="display: none;">
            <div class="page-header">SAVED ARTICLES (F2)</div>
            <div style="height: 1px; background: #000; margin-bottom: 0px;"></div>
            <div id="saved-articles-list">
                <p style="color: #888; text-align: center; padding: 40px;">Loading saved articles...</p>
            </div>
        </div>
        
        <!-- Loved Page -->
        <div id="loved-content" class="page-content" style="display: none;">
            <div class="page-header">LOVED ARTICLES (F3)</div>
            <div style="height: 1px; background: #000; margin-bottom: 0px;"></div>
            <div id="loved-articles-list">
                <p style="color: #888; text-align: center; padding: 40px;">Loading loved articles...</p>
            </div>
        </div>
        
        <!-- Config Page -->
        <div id="config-content" class="page-content" style="display: none;">
            <div class="page-header">CONFIGURATION (F4)</div>
            
            <div class="config-section">
                <h3>📥 IMPORT FEEDS</h3>
                <div class="import-section">
                    <form id="opml-upload-form" enctype="multipart/form-data">
                        <div class="upload-area">
                            <input type="file" id="opml-file" name="opml" accept=".opml,.xml" style="display: none;">
                            <button type="button" onclick="document.getElementById('opml-file').click()" class="upload-button">
                                📁 SELECT OPML FILE
                            </button>
                            <span id="file-name" class="file-name"></span>
                        </div>
                        <button type="submit" class="import-button">📥 IMPORT FEEDS</button>
                    </form>
                    
                    <div class="manual-add">
                        <h4>➕ ADD INDIVIDUAL FEED</h4>
                        <form id="manual-feed-form">
                            <input type="url" id="feed-url" placeholder="https://example.com/rss.xml" required>
                            <button type="submit">ADD FEED</button>
                        </form>
                    </div>
                </div>
            </div>
            
            <div class="config-section">
                <h3>📋 FEED MANAGEMENT</h3>
                <div class="feeds-management">
                    <div class="feeds-stats">
                        <span id="total-feeds">Total: --</span>
                        <span id="active-feeds">Active: --</span>
                    </div>
                    <div class="management-actions">
                        <button onclick="exportFeeds()" class="export-button">📤 EXPORT OPML</button>
                        <button onclick="clearCache()" class="clear-cache-button">🗑️ CLEAR CACHE</button>
                    </div>
                </div>
            </div>

            <div class="config-section">
                <h3>RSS FEEDS MANAGEMENT</h3>
                <p style="color: #888; margin-bottom: 20px;">Navigate with J/K, delete with D</p>
                
                <div id="feeds-list-container">
                    <div id="feeds-list"></div>
                </div>
            </div>
            
            <div class="config-section">
                <h3>Interface Settings</h3>
                <label class="config-label">Article buttons position:</label>
                <select id="buttonsConfigModal" onchange="changeButtonsPosition()" class="config-select">
                    <option value="right">Right</option>
                    <option value="left">Left</option>
                </select>
            </div>
            
            <div class="config-section">
                <h3>Keyboard shortcuts</h3>
                <p><strong>F1-F4:</strong> Switch between pages</p>
                <p><strong>↑/↓, J/K:</strong> Navigate articles</p>
                <p><strong>Space/Enter:</strong> Expand article</p>
                <p><strong>S/L/C:</strong> Save/Favorite/Share</p>
                <p><strong>Escape:</strong> Return to FEEDS</p>
            </div>
        </div>
    </div>
    
    <script>
        // DEFINICIONES GLOBALES - FUNCIONES DE NAVEGACIÓN
        
        // Variables globales necesarias
        let selectedIndex = 0;
        let currentPage = 'feeds';
        const articles = [];
        
        // Función principal de navegación entre pestañas
        window.showPage = function(pageName, event) {
            if (event) {
                event.preventDefault();
            }
            
            // Actualizar la página actual
            currentPage = pageName;
            
            // Ocultar todos los contenidos
            const contentIds = ['feeds-content', 'saved-content', 'loved-content', 'config-content'];
            contentIds.forEach(id => {
                const element = document.getElementById(id);
                if (element) element.style.display = 'none';
            });
            
            // Quitar clase activa de todas las pestañas
            document.querySelectorAll('.tab').forEach(tab => {
                tab.classList.remove('tab-active');
            });
            
            // Mostrar el contenido solicitado
            const targetContent = document.getElementById(pageName + '-content');
            if (targetContent) {
                targetContent.style.display = 'block';
            }
            
            // Activar la pestaña correspondiente
            if (event && event.target) {
                event.target.classList.add('tab-active');
            } else {
                // Si no hay event.target, buscar la pestaña por el nombre
                const tabs = document.querySelectorAll('.tab');
                tabs.forEach(tab => {
                    if (tab.textContent.toLowerCase().includes(pageName)) {
                        tab.classList.add('tab-active');
                    }
                });
            }
            
            selectedIndex = 0;
        };
        
        // Función para toggle de artículos en feeds principales
        window.toggleArticle = function(index, event) {
            console.log('🎯 DEBUG toggleArticle - Index:', index);
            
            if (event) {
                event.preventDefault();
                event.stopPropagation();
            }
            
            // MÉTODO 1: Buscar por ID directo (content-X)
            const contentById = document.getElementById('content-' + index);
            console.log('🔍 Buscando content-' + index + ':', !!contentById);
            
            if (contentById) {
                const isCurrentlyExpanded = contentById.classList.contains('expanded');
                console.log('📄 Contenido expandido actualmente:', isCurrentlyExpanded);
                
                // Cerrar todos los artículos
                document.querySelectorAll('.article-content').forEach(el => {
                    el.classList.remove('expanded');
                });
                
                if (!isCurrentlyExpanded) {
                    // Abrir el artículo
                    contentById.classList.add('expanded');
                    selectedIndex = index;
                    console.log('✅ Artículo expandido - clases:', contentById.className);
                    console.log('📝 Contenido HTML:', contentById.innerHTML.substring(0, 200) + '...');
                    
                    // Scroll más suave y posicionado al contenido expandido
                    setTimeout(() => {
                        contentById.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
                    }, 100);
                } else {
                    console.log('📄 Artículo cerrado');
                }
                return;
            }
            
            // MÉTODO 2: Buscar usando la estructura de containers
            const containers = document.querySelectorAll('#feeds-content .article-container');
            console.log('📦 Total containers encontrados:', containers.length);
            if (containers[index]) {
                console.log('📦 Container [' + index + '] encontrado');
                const content = containers[index].querySelector('.article-content');
                console.log('📄 Content encontrado en container:', !!content);
                
                if (content) {
                    const isCurrentlyExpanded = content.classList.contains('expanded');
                    console.log('📄 Expandido (método 2):', isCurrentlyExpanded);
                    
                    // Cerrar todos los artículos
                    document.querySelectorAll('.article-content').forEach(el => {
                        el.classList.remove('expanded');
                    });
                    
                    if (!isCurrentlyExpanded) {
                        // Abrir el artículo
                        content.classList.add('expanded');
                        console.log('✅ Expandido (método 2) - HTML:', content.innerHTML.substring(0, 200) + '...');
                        
                        // Scroll más suave
                        setTimeout(() => {
                            content.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
                        }, 100);
                    }
                    return;
                }
            } else {
                console.log('❌ No se encontró container para index:', index);
            }
            
            // MÉTODO 3: Simular click en el artículo seleccionado
            const selectedArticleLine = document.querySelector('.article-line.article-selected');
            console.log('🖱️ ArticleLine seleccionado encontrado:', !!selectedArticleLine);
            if (selectedArticleLine) {
                const fullLineLink = selectedArticleLine.querySelector('.full-line-link');
                console.log('🔗 FullLineLink encontrado:', !!fullLineLink);
                if (fullLineLink) {
                    console.log('🖱️ Haciendo click en full-line-link');
                    fullLineLink.click();
                    return;
                }
                
                console.log('🖱️ Haciendo click directo en article-line');
                selectedArticleLine.click();
                return;
            }
            
            console.log('❌ NO SE PUDO EXPANDIR EL ARTÍCULO CON NINGÚN MÉTODO');
        };
        
        // Función para toggle de artículos en saved/loved
        window.toggleArticleContent = function(element, url, context) {
            
            const container = element.closest('.article-container');
            if (!container) return;
            
            const content = container.querySelector('.article-content');
            if (!content) return;
            
            const isCurrentlyExpanded = content.classList.contains('expanded');
            
            // Cerrar todos los artículos
            document.querySelectorAll('.article-content').forEach(el => {
                el.classList.remove('expanded');
                el.parentElement.querySelector('.article-line')?.classList.remove('article-selected');
            });
            
            if (!isCurrentlyExpanded) {
                // Abrir el artículo
                content.classList.add('expanded');
                element.classList.add('article-selected');
                
                // Scroll al artículo
                element.scrollIntoView({ behavior: 'smooth', block: 'start' });
            }
        };
        
        // Navegación por teclado - VERSIÓN FINAL LIMPIA
        document.addEventListener('keydown', function(event) {
            // Evitar atajos si hay un input activo
            if (event.target.tagName === 'INPUT' || event.target.tagName === 'TEXTAREA') {
                return;
            }
            
            // Navegación con J/K
            if (event.key === 'j' || event.key === 'J' || event.code === 'KeyJ') {
                event.preventDefault();
                const totalArticles = document.querySelectorAll('#feeds-content .article-container').length;
                if (selectedIndex < totalArticles - 1) {
                    selectedIndex++;
                    highlightSelected();
                }
                return;
            }
            
            if (event.key === 'k' || event.key === 'K' || event.code === 'KeyK') {
                event.preventDefault();
                if (selectedIndex > 0) {
                    selectedIndex--;
                    highlightSelected();
                }
                return;
            }
            
            // Resto de teclas
            const key = event.key.toLowerCase();
            
            switch(key) {
                case 'f1':
                    event.preventDefault();
                    showPage('feeds');
                    break;
                    
                case 'f2':
                    event.preventDefault();
                    showPage('saved');
                    break;
                    
                case 'f3':
                    event.preventDefault();
                    showPage('loved');
                    break;
                    
                case 'f4':
                    event.preventDefault();
                    showPage('config');
                    break;
                    
                case ' ':
                    event.preventDefault();
                    if (currentPage === 'feeds' && selectedIndex >= 0) {
                        toggleArticle(selectedIndex);
                    }
                    break;
                    
                case 'enter':
                    event.preventDefault();
                    if (currentPage === 'feeds' && selectedIndex >= 0) {
                        toggleArticle(selectedIndex);
                    }
                    break;
                    
                case 'arrowdown':
                    event.preventDefault();
                    const totalDown = document.querySelectorAll('#feeds-content .article-container').length;
                    if (selectedIndex < totalDown - 1) {
                        selectedIndex++;
                        highlightSelected();
                    }
                    break;
                    
                case 'arrowup':
                    event.preventDefault();
                    if (selectedIndex > 0) {
                        selectedIndex--;
                        highlightSelected();
                    }
                    break;
            }
        });
        
        // Función para destacar el artículo seleccionado
        function highlightSelected() {
            // Quitar highlight previo
            document.querySelectorAll('.article-line').forEach(el => {
                el.classList.remove('article-selected');
                el.style.backgroundColor = ''; // Limpiar estilos inline
                el.style.color = '';
                el.style.border = '';
            });
            
            // Agregar highlight al actual
            if (currentPage === 'feeds') {
                const containers = document.querySelectorAll('#feeds-content .article-container');
                
                if (containers[selectedIndex]) {
                    const articleLine = containers[selectedIndex].querySelector('.article-line');
                    
                    if (articleLine) {
                        articleLine.classList.add('article-selected');
                        
                        // Añadir highlighting visual más visible
                        articleLine.style.backgroundColor = 'rgba(0, 255, 0, 0.2)'; // Verde translúcido
                        articleLine.style.border = '2px solid #00ff00'; // Borde verde
                        articleLine.style.boxShadow = '0 0 10px rgba(0, 255, 0, 0.5)'; // Sombra verde
                        
                        articleLine.scrollIntoView({ behavior: 'smooth', block: 'center' });
                    }
                }
            }
        }
        
        // Inicializar cuando el DOM esté listo
        document.addEventListener('DOMContentLoaded', function() {
            // Asegurar que la página tenga el foco para recibir eventos de teclado
            window.focus();
            document.body.focus();
            
            // Destacar el primer artículo
            selectedIndex = 0;
            setTimeout(function() {
                highlightSelected();
            }, 500);
        });
    </script>
</body>
</html>`

	w.Write([]byte(html))
	log.Printf("✅ HTML rendered successfully with %d articles", len(data.Articles))
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	username := getUserFromRequest(r)
	feeds := loadFeedsForUser(username)
	var allArticles []Article
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, feed := range feeds {
		if !feed.Active {
			continue
		}
		wg.Add(1)
		go func(feedURL string) {
			defer wg.Done()
			articles := getCachedOrFetch(feedURL)
			mu.Lock()
			allArticles = append(allArticles, articles...)
			mu.Unlock()
		}(feed.URL)
	}
	wg.Wait()

	for i := range allArticles {
		allArticles[i].IsFav = isArticleFavorite(allArticles[i].Link)
	}

	// Sort articles by date and title to ensure consistent order
	sort.Slice(allArticles, func(i, j int) bool {
		if allArticles[i].Date == allArticles[j].Date {
			return allArticles[i].Title < allArticles[j].Title
		}
		return allArticles[i].Date > allArticles[j].Date
	})

	if len(allArticles) > 50 {
		allArticles = allArticles[:50]
	}

	// Precargar contenido completo de los primeros artículos en segundo plano
	go preloadArticleContent(allArticles)

	data := TemplateData{
		Articles: allArticles,
	}

	elapsed := time.Since(startTime)
	log.Printf("⚡ Home handler completed in %v with %d articles (CACHE + PARALLEL)", elapsed, len(allArticles))
	renderHomePage(w, data)
}

func preloadArticleContent(articles []Article) {
	log.Printf("🔄 Iniciando precarga de contenido para %d artículos", len(articles))

	// Limitar a los primeros 10 artículos para no sobrecargar
	maxArticles := 10
	if len(articles) < maxArticles {
		maxArticles = len(articles)
	}

	var wg sync.WaitGroup
	for i := 0; i < maxArticles; i++ {
		article := articles[i]

		// Verificar si ya está en cache
		articleContentCache.mutex.RLock()
		cached, exists := articleContentCache.articles[article.Link]
		articleContentCache.mutex.RUnlock()

		// Solo precargar si no existe en cache o es muy antiguo
		if !exists || time.Since(cached.Timestamp) > 30*time.Minute {
			wg.Add(1)
			go func(url string, title string) {
				defer wg.Done()

				content, err := scrapeArticleContent(url)
				success := err == nil

				// Si el scraping falla, usar contenido vacío pero marcar como intentado
				if !success {
					content = ""
				}

				// Guardar en cache
				articleContentCache.mutex.Lock()
				articleContentCache.articles[url] = CachedArticleContent{
					Content:   content,
					Timestamp: time.Now(),
					Success:   success,
				}
				articleContentCache.mutex.Unlock()

				if success {
					log.Printf("✅ Precargado: %s", title)
				} else {
					log.Printf("⚠️ Falló precarga: %s", title)
				}
			}(article.Link, article.Title)
		}
	}
	wg.Wait()
	log.Printf("🎯 Precarga de contenido completada")
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

		// Mejorar el nombre de la fuente
		sourceName := feed.Title
		if sourceName == "" || sourceName == "YouTube" || strings.Contains(sourceName, "uploads by") {
			// Para YouTube, usar el título del feed si está disponible
			if strings.Contains(feedURL, "youtube.com") || strings.Contains(feedURL, "youtu.be") {
				if feed.Title != "" && !strings.Contains(feed.Title, "uploads by") {
					// Usar el título del feed directamente si es bueno
					sourceName = feed.Title
				} else {
					// Extraer información de la URL como fallback
					if strings.Contains(feedURL, "/channel/") {
						channelMatch := regexp.MustCompile(`/channel/([^/\?]+)`).FindStringSubmatch(feedURL)
						if len(channelMatch) > 1 {
							channelID := channelMatch[1]
							if len(channelID) > 12 {
								channelID = channelID[:12]
							}
							sourceName = fmt.Sprintf("YT %s", channelID)
						} else {
							sourceName = "YouTube Channel"
						}
					} else if strings.Contains(feedURL, "channel_id=") {
						channelMatch := regexp.MustCompile(`channel_id=([^&]+)`).FindStringSubmatch(feedURL)
						if len(channelMatch) > 1 {
							channelID := channelMatch[1]
							if len(channelID) > 12 {
								channelID = channelID[:12]
							}
							sourceName = fmt.Sprintf("YT %s", channelID)
						} else {
							sourceName = "YouTube Channel"
						}
					} else if strings.Contains(feedURL, "user=") {
						userMatch := regexp.MustCompile(`user=([^&]+)`).FindStringSubmatch(feedURL)
						if len(userMatch) > 1 {
							sourceName = fmt.Sprintf("YouTube @%s", userMatch[1])
						} else {
							sourceName = "YouTube Channel"
						}
					} else {
						sourceName = "YouTube Channel"
					}
				}
			}
		}

		// Limpiar y acortar nombres muy largos
		if len(sourceName) > 30 {
			sourceName = sourceName[:27] + "..."
		}

		article := Article{
			Title:       item.Title,
			Link:        item.Link,
			Date:        date,
			Source:      sourceName,
			Description: description,
		}
		articles = append(articles, article)
	}

	return articles
}

func addHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("➕ Add handler called")
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	feedURL := r.FormValue("url")
	if feedURL == "" {
		http.Error(w, "URL required", http.StatusBadRequest)
		return
	}

	username := getUserFromRequest(r)
	feed := Feed{URL: feedURL, Active: true}
	if err := saveFeedForUser(feed, username); err != nil {
		log.Printf("❌ Error saving feed: %v", err)
		http.Error(w, "Error saving feed", http.StatusInternalServerError)
		return
	}

	log.Printf("✅ Feed added for user %s: %s", username, feedURL)
	w.Write([]byte("Feed added successfully"))
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

func clearCacheHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("🧹 Clear cache handler called")
	globalCache.mutex.Lock()
	globalCache.feeds = make(map[string]CachedFeed)
	globalCache.mutex.Unlock()
	log.Printf("✅ Cache cleared successfully")
	w.Write([]byte("Cache cleared successfully"))
}

func scrapeArticleHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var request struct {
		URL string `json:"url"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		log.Printf("❌ Error decoding scrape request: %v", err)
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	log.Printf("🔍 Attempting to scrape article: %s", request.URL)

	// Verificar primero si está en cache
	articleContentCache.mutex.RLock()
	cached, exists := articleContentCache.articles[request.URL]
	articleContentCache.mutex.RUnlock()

	var content string
	var err error

	if exists && cached.Success && time.Since(cached.Timestamp) < 30*time.Minute {
		// Usar contenido del cache
		content = cached.Content
		log.Printf("🟢 Cache HIT para artículo: %s", request.URL)
	} else {
		// Hacer scraping y guardar en cache
		content, err = scrapeArticleContent(request.URL)
		if err != nil {
			log.Printf("❌ Error scraping article: %v", err)
			http.Error(w, "Could not scrape article", http.StatusInternalServerError)
			return
		}

		// Guardar en cache
		articleContentCache.mutex.Lock()
		articleContentCache.articles[request.URL] = CachedArticleContent{
			Content:   content,
			Timestamp: time.Now(),
			Success:   true,
		}
		articleContentCache.mutex.Unlock()
		log.Printf("🔴 Cache MISS - scraped y guardado: %s", request.URL)
	}

	response := struct {
		Content string `json:"content"`
		URL     string `json:"url"`
	}{
		Content: content,
		URL:     request.URL,
	}

	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Printf("❌ Error encoding scrape response: %v", err)
		http.Error(w, "Error encoding response", http.StatusInternalServerError)
		return
	}

	log.Printf("✅ Successfully scraped article content (%d characters)", len(content))
}

func scrapeArticleContent(url string) (string, error) {
	// Crear cliente HTTP con timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Hacer request a la URL del artículo
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("error fetching URL: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("non-200 status code: %d", resp.StatusCode)
	}

	// Leer el contenido HTML
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading response body: %v", err)
	}

	// Convertir a string
	html := string(body)

	// Extraer el contenido principal usando selectores comunes
	content := extractMainContent(html)

	// Limpiar y formatear el contenido
	content = cleanScrapedContent(content)

	// Ser más tolerante con el contenido corto
	if len(content) < 50 {
		// Intentar extraer al menos el título y algo de contenido
		titleContent := extractTitleAndMeta(html)
		if len(titleContent) > 20 {
			content = titleContent
		} else {
			return "", fmt.Errorf("extracted content too short (%d chars), probably failed", len(content))
		}
	}

	return content, nil
}

func extractMainContent(html string) string {
	// Selectores comunes para contenido principal
	contentSelectors := []string{
		`<article[^>]*>([\s\S]*?)</article>`,
		`<div[^>]*class[^>]*["'].*?content.*?["'][^>]*>([\s\S]*?)</div>`,
		`<div[^>]*class[^>]*["'].*?post.*?["'][^>]*>([\s\S]*?)</div>`,
		`<div[^>]*class[^>]*["'].*?entry.*?["'][^>]*>([\s\S]*?)</div>`,
		`<div[^>]*class[^>]*["'].*?main.*?["'][^>]*>([\s\S]*?)</div>`,
		`<main[^>]*>([\s\S]*?)</main>`,
	}

	// Intentar cada selector
	for _, selector := range contentSelectors {
		re := regexp.MustCompile(selector)
		matches := re.FindStringSubmatch(html)
		if len(matches) > 1 && len(matches[1]) > 100 {
			return matches[1]
		}
	}

	// Fallback: extraer entre <body> tags
	bodyRe := regexp.MustCompile(`<body[^>]*>([\s\S]*?)</body>`)
	matches := bodyRe.FindStringSubmatch(html)
	if len(matches) > 1 {
		return matches[1]
	}

	return html
}

func extractTitleAndMeta(html string) string {
	// Fallback: extraer al menos título y meta descripción
	var content strings.Builder

	// Extraer título
	titleRe := regexp.MustCompile(`<title[^>]*>([^<]+)</title>`)
	if matches := titleRe.FindStringSubmatch(html); len(matches) > 1 {
		content.WriteString("# ")
		content.WriteString(strings.TrimSpace(matches[1]))
		content.WriteString("\n\n")
	}

	// Extraer meta descripción
	metaRe := regexp.MustCompile(`<meta[^>]*name=["\']description["\'][^>]*content=["\']([^"\']+)["\']`)
	if matches := metaRe.FindStringSubmatch(html); len(matches) > 1 {
		content.WriteString(strings.TrimSpace(matches[1]))
		content.WriteString("\n\n")
	}

	// Extraer algunos párrafos del contenido
	pRe := regexp.MustCompile(`<p[^>]*>([^<]{20,200})</p>`)
	matches := pRe.FindAllStringSubmatch(html, 3)
	for _, match := range matches {
		if len(match) > 1 {
			cleanP := strings.TrimSpace(match[1])
			if len(cleanP) > 20 {
				content.WriteString(cleanP)
				content.WriteString("\n\n")
			}
		}
	}

	return content.String()
}

func cleanScrapedContent(content string) string {
	// Remover scripts y styles
	scriptRe := regexp.MustCompile(`<script[^>]*>[\s\S]*?</script>`)
	content = scriptRe.ReplaceAllString(content, "")

	styleRe := regexp.MustCompile(`<style[^>]*>[\s\S]*?</style>`)
	content = styleRe.ReplaceAllString(content, "")

	// Remover comentarios HTML
	commentRe := regexp.MustCompile(`<!--[\s\S]*?-->`)
	content = commentRe.ReplaceAllString(content, "")

	// Remover navegación y elementos comunes no deseados
	unwantedSelectors := []string{
		`<nav[^>]*>[\s\S]*?</nav>`,
		`<header[^>]*>[\s\S]*?</header>`,
		`<footer[^>]*>[\s\S]*?</footer>`,
		`<div[^>]*class[^>]*["'].*?nav.*?["'][^>]*>[\s\S]*?</div>`,
		`<div[^>]*class[^>]*["'].*?menu.*?["'][^>]*>[\s\S]*?</div>`,
		`<div[^>]*class[^>]*["'].*?sidebar.*?["'][^>]*>[\s\S]*?</div>`,
		`<div[^>]*class[^>]*["'].*?advertisement.*?["'][^>]*>[\s\S]*?</div>`,
		`<div[^>]*class[^>]*["'].*?ads.*?["'][^>]*>[\s\S]*?</div>`,
	}

	for _, selector := range unwantedSelectors {
		re := regexp.MustCompile(selector)
		content = re.ReplaceAllString(content, "")
	}

	// Convertir algunas etiquetas HTML a texto plano
	content = strings.ReplaceAll(content, "<br>", "\n")
	content = strings.ReplaceAll(content, "<br/>", "\n")
	content = strings.ReplaceAll(content, "<br />", "\n")
	content = strings.ReplaceAll(content, "</p>", "\n\n")
	content = strings.ReplaceAll(content, "</div>", "\n")
	content = strings.ReplaceAll(content, "</h1>", "\n\n")
	content = strings.ReplaceAll(content, "</h2>", "\n\n")
	content = strings.ReplaceAll(content, "</h3>", "\n\n")
	content = strings.ReplaceAll(content, "</h4>", "\n\n")
	content = strings.ReplaceAll(content, "</h5>", "\n\n")
	content = strings.ReplaceAll(content, "</h6>", "\n\n")

	// Remover todas las etiquetas HTML restantes
	htmlTagRe := regexp.MustCompile(`<[^>]*>`)
	content = htmlTagRe.ReplaceAllString(content, "")

	// Decodificar entidades HTML comunes
	content = strings.ReplaceAll(content, "&amp;", "&")
	content = strings.ReplaceAll(content, "&lt;", "<")
	content = strings.ReplaceAll(content, "&gt;", ">")
	content = strings.ReplaceAll(content, "&quot;", "\"")
	content = strings.ReplaceAll(content, "&#39;", "'")
	content = strings.ReplaceAll(content, "&nbsp;", " ")

	// Limpiar espacios en blanco excesivos
	spaceRe := regexp.MustCompile(`\s+`)
	content = spaceRe.ReplaceAllString(content, " ")

	// Limpiar saltos de línea excesivos
	newlineRe := regexp.MustCompile(`\n\s*\n\s*\n`)
	content = newlineRe.ReplaceAllString(content, "\n\n")

	return strings.TrimSpace(content)
}

func staticHandler(w http.ResponseWriter, r *http.Request) {
	// Servir archivos estáticos
	http.ServeFile(w, r, "."+r.URL.Path)
}

func preloadFeedsHandler(w http.ResponseWriter, r *http.Request) {
	log.Printf("🔄 Preload feeds handler called")

	// Solo permitir GET
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Cargar feeds en segundo plano y llenar cache
	feeds := loadFeeds()
	var wg sync.WaitGroup

	for _, feed := range feeds {
		wg.Add(1)
		go func(feedURL string) {
			defer wg.Done()
			// Esto llenará el cache
			fetchFeedArticles(feedURL)
		}(feed.URL)
	}

	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	response := map[string]interface{}{
		"status":      "success",
		"message":     "Feeds precargados en cache",
		"feeds_count": len(feeds),
	}

	json.NewEncoder(w).Encode(response)
	log.Printf("✅ Feeds precargados exitosamente (%d feeds)", len(feeds))
}

func loginPageHandler(w http.ResponseWriter, r *http.Request) {
	// Verificar si ya tiene sesión válida
	cookie, err := r.Cookie("session_id")
	if err == nil {
		if _, valid := validateSession(cookie.Value); valid {
			http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
			return
		}
	}

	// Renderizar página de login (usar la misma lógica que home pero solo login)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	// Solo mostrar la pantalla de login
	html := `<!DOCTYPE html>
<html lang="es">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>ANCAP WEB - LOGIN</title>
    <style>
        @font-face {
            font-family: 'JetBrains Mono';
            src: url('/static/fonts/JetBrainsMonoNerdFont-Regular.woff2') format('woff2'),
                 url('/static/fonts/JetBrainsMono/JetBrainsMonoNerdFont-Regular.ttf') format('truetype');
            font-weight: 400;
            font-style: normal;
            font-display: swap;
        }
        body { 
            background: #000; 
            color: #00ff00; 
            font-family: 'JetBrains Mono', 'Courier New', monospace; 
            margin: 0; 
            padding: 20px;
            height: 100vh;
            display: flex;
            justify-content: center;
            align-items: center;
            background-image: 
                radial-gradient(rgba(0, 255, 0, 0.03) 1px, transparent 1px);
            background-size: 20px 20px;
            padding-top: 2vh;
        }
        
        .login-container {
            text-align: center;
            max-width: 400px;
            width: 100%;
            display: flex;
            flex-direction: column;
            align-items: center;
        }
        
        .login-brand {
            font-size: 20px;
            font-weight: bold;
            margin-bottom: 12px;
            color: #00aa00;
            text-align: center;
            font-family: 'Courier New', monospace;
            white-space: pre;
            line-height: 1.0;
            text-shadow: 0 0 10px #00aa00, 0 0 20px #00aa00;
            animation: glow 2s ease-in-out infinite alternate;
        }
        
        @keyframes glow {
            from { text-shadow: 0 0 10px #00aa00, 0 0 20px #00aa00; }
            to { text-shadow: 0 0 15px #00aa00, 0 0 30px #00aa00; }
        }
        
        .login-subtitle {
            font-size: 14px;
            color: #ffff00;
            margin-top: 5px;
            margin-bottom: 35px;
            letter-spacing: 2px;
            text-transform: uppercase;
        }
        
        .login-content {
            background: transparent;
            padding: 0;
            width: 100%;
        }
        
        .login-form {
            margin-top: 0;
            width: 100%;
            display: flex;
            flex-direction: column;
            align-items: center;
        }
        
        .login-input {
            background: rgba(0, 0, 0, 0.9);
            border: 2px dashed #00ff00;
            border-radius: 12px;
            color: #00ff00;
            font-family: 'JetBrains Mono', monospace;
            font-size: 18px;
            padding: 12px 18px;
            margin: 8px 0;
            width: 100%;
            max-width: 320px;
            box-sizing: border-box;
            text-align: center;
            letter-spacing: 1px;
            transition: all 0.3s ease;
            backdrop-filter: blur(5px);
            box-shadow: 
                0 0 20px rgba(0, 255, 0, 0.2),
                inset 0 0 20px rgba(0, 255, 0, 0.05);
        }
        
        .login-input::placeholder {
            color: #006600;
            opacity: 0.8;
        }
        
        .login-input:focus {
            outline: none;
            border-color: #00ff00;
            box-shadow: 
                0 0 25px rgba(0, 255, 0, 0.6),
                inset 0 0 15px rgba(0, 255, 0, 0.1);
            transform: scale(1.03);
        }
        
        .login-button {
            background: rgba(0, 0, 0, 0.9);
            border: 2px solid #00ff00;
            border-radius: 12px;
            color: #00ff00;
            font-family: 'JetBrains Mono', monospace;
            font-size: 16px;
            font-weight: bold;
            padding: 12px 25px;
            cursor: pointer;
            margin-top: 15px;
            width: 100%;
            max-width: 320px;
            transition: all 0.3s ease;
            letter-spacing: 2px;
            text-transform: uppercase;
            backdrop-filter: blur(5px);
        }
        
        .login-button:hover {
            background: #00ff00;
            color: #000;
            box-shadow: 0 0 30px rgba(0, 255, 0, 0.7);
            transform: translateY(-2px);
        }
        
        .login-button:active {
            transform: translateY(0);
        }
        
        .login-error {
            color: #ff3333;
            margin-top: 20px;
            font-size: 14px;
            background: rgba(255, 0, 0, 0.1);
            border: 1px dashed #ff3333;
            border-radius: 10px;
            padding: 10px;
        }
        
        .login-info {
            color: #666;
            font-size: 11px;
            margin-top: 30px;
            line-height: 1.6;
            border-top: 1px dashed #333;
            padding-top: 20px;
        }
        
        .loading-dots {
            animation: loadingDots 1.5s infinite;
        }
        
        @keyframes loadingDots {
            0%, 20% { opacity: 0; }
            50% { opacity: 1; }
            80%, 100% { opacity: 0; }
        }
    </style>
    <script>
        function handleLogin(event) {
            event.preventDefault();
            
            const username = document.getElementById('username').value;
            const password = document.getElementById('password').value;
            const errorDiv = document.getElementById('login-error');
            
            if (!username || !password) {
                errorDiv.textContent = 'Please enter username and password';
                errorDiv.style.display = 'block';
                return;
            }
            
            errorDiv.style.display = 'none';
            
            const button = document.getElementById('login-button');
            button.innerHTML = 'LOADING<span class="loading-dots">...</span>';
            button.disabled = true;
            
            fetch('/api/login', {
                method: 'POST',
                headers: {
                    'Content-Type': 'application/json',
                },
                body: JSON.stringify({
                    username: username,
                    password: password
                })
            })
            .then(response => {
                if (response.ok) {
                    return response.json();
                } else {
                    throw new Error('Credenciales inválidas');
                }
            })
            .then(data => {
                if (data.success) {
                    // Si los feeds están precargados, la página cargará más rápido
                    if (feedsPreloaded) {
                        console.log('🚀 Login exitoso con feeds precargados - carga rápida');
                    } else {
                        console.log('✅ Login exitoso - cargando feeds...');
                    }
                    window.location.href = '/';
                } else {
                    throw new Error(data.message || 'Authentication error');
                }
            })
            .catch(error => {
                errorDiv.textContent = error.message;
                errorDiv.style.display = 'block';
                button.textContent = 'LOGIN';
                button.disabled = false;
                document.getElementById('password').value = '';
                document.getElementById('password').focus();
            });
        }
        
        function handleKeyPress(event) {
            if (event.key === 'Enter') {
                handleLogin(event);
            }
        }
        
        // Variable para controlar la precarga
        let preloadTimer = null;
        let feedsPreloaded = false;
        
        function preloadFeeds(username) {
            if (feedsPreloaded) return;
            
            console.log('🔄 Precargando feeds para usuario:', username);
            
            // Hacer precarga de feeds usando el endpoint específico
            fetch('/api/preload-feeds', {
                method: 'GET',
                credentials: 'same-origin'
            }).then(response => {
                if (response.ok) {
                    return response.json();
                }
                throw new Error('Error en precarga');
            }).then(data => {
                console.log('✅ Feeds precargados:', data);
                feedsPreloaded = true;
                
                // Mostrar indicador visual sutil
                const usernameInput = document.getElementById('username');
                usernameInput.style.borderColor = '#00aa00';
                usernameInput.style.boxShadow = '0 0 15px rgba(0, 255, 0, 0.3)';
                
                setTimeout(() => {
                    usernameInput.style.borderColor = '#00ff00';
                    usernameInput.style.boxShadow = '0 0 20px rgba(0, 255, 0, 0.2), inset 0 0 20px rgba(0, 255, 0, 0.05)';
                }, 2000);
            }).catch(error => {
                console.log('❌ Error precargando feeds:', error);
            });
        }
        
        function handleUsernameInput(event) {
            const username = event.target.value.trim();
            
            // Cancelar timer anterior si existe
            if (preloadTimer) {
                clearTimeout(preloadTimer);
            }
            
            // Si el usuario escribió algo, esperar 800ms y precargar
            if (username.length >= 3) {
                preloadTimer = setTimeout(() => {
                    preloadFeeds(username);
                }, 800);
            }
        }
        
        document.addEventListener('DOMContentLoaded', function() {
            const usernameInput = document.getElementById('username');
            usernameInput.focus();
            
            // Agregar evento de input para detectar cuando se escribe el usuario
            usernameInput.addEventListener('input', handleUsernameInput);
        });
    </script>
</head>
<body>
    <div class="login-container">
        <div class="login-brand">
▄▀█ █▄ █ █▀▀ ▄▀█ █▀▄
█▀█ █ ▀█ █▄▄ █▀█ █▀▀
        </div>
        <div class="login-subtitle">» A LIBERTARIAN RSS READER «</div>
        
        <div class="login-content">
            <form class="login-form" onsubmit="handleLogin(event)">
                <input 
                    type="text" 
                    id="username" 
                    class="login-input" 
                    placeholder="USUARIO" 
                    onkeypress="handleKeyPress(event)"
                    autocomplete="username">
                <input 
                    type="password" 
                    id="password" 
                    class="login-input" 
                    placeholder="CONTRASEÑA" 
                    onkeypress="handleKeyPress(event)"
                    autocomplete="current-password">
                <button type="submit" id="login-button" class="login-button">
                    ACCEDER
                </button>
                <div id="login-error" class="login-error" style="display: none;"></div>
                <div class="login-info">
                    Credenciales por defecto:<br>
                    <strong>admin</strong> / admin123<br>
                    <strong>ancap</strong> / libertad
                </div>
            </form>
        </div>
    </div>
</body>
</html>`

	w.Write([]byte(html))
}

func loginAPIHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var loginReq struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&loginReq); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if validateLogin(loginReq.Username, loginReq.Password) {
		sessionID := createSession(loginReq.Username)

		// Crear cookie de sesión
		cookie := &http.Cookie{
			Name:     "session_id",
			Value:    sessionID,
			Expires:  time.Now().Add(SESSION_DURATION),
			HttpOnly: true,
			Path:     "/",
		}
		http.SetCookie(w, cookie)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"message": "Login exitoso",
		})

		log.Printf("✅ Login exitoso para usuario: %s", loginReq.Username)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"message": "Credenciales inválidas",
		})

		log.Printf("❌ Login fallido para usuario: %s", loginReq.Username)
	}
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_id")
	if err == nil {
		sessionMutex.Lock()
		delete(sessions, cookie.Value)
		sessionMutex.Unlock()
	}

	// Eliminar cookie
	deleteCookie := &http.Cookie{
		Name:     "session_id",
		Value:    "",
		Expires:  time.Now().Add(-1 * time.Hour),
		HttpOnly: true,
		Path:     "/",
	}
	http.SetCookie(w, deleteCookie)

	http.Redirect(w, r, "/login", http.StatusTemporaryRedirect)
}

// Handler para obtener la lista de feeds
func feedsAPIHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	username := getUserFromRequest(r)
	feeds := loadFeedsForUser(username)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(feeds)
}

// Handler para verificar el estado de un feed
func checkFeedHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request struct {
		URL string `json:"url"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Intentar obtener el feed
	working := true
	_, err := fetchFeed(request.URL)
	if err != nil {
		working = false
	}

	response := struct {
		Working bool `json:"working"`
	}{
		Working: working,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// Handler para importar feeds desde OPML
func uploadOPMLHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	username := getUserFromRequest(r)
	log.Printf("🔄 OPML import started for user: %s", username)

	if username == "" {
		log.Printf("❌ No authenticated user found")
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}

	file, header, err := r.FormFile("opml")
	if err != nil {
		log.Printf("❌ Error getting OPML file: %v", err)
		http.Error(w, "Error getting file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	log.Printf("📁 Processing OPML file: %s", header.Filename)

	// Leer el contenido del archivo
	data, err := io.ReadAll(file)
	if err != nil {
		log.Printf("❌ Error reading OPML file: %v", err)
		http.Error(w, "Error reading file", http.StatusInternalServerError)
		return
	}

	log.Printf("📄 OPML file size: %d bytes", len(data))

	// Parsear el OPML
	var opml OPML
	if err := xml.Unmarshal(data, &opml); err != nil {
		log.Printf("❌ Error parsing OPML: %v", err)
		preview := string(data)
		if len(data) > 200 {
			preview = string(data[:200]) + "..."
		}
		log.Printf("📄 OPML content preview: %s", preview)
		http.Error(w, "Invalid OPML file", http.StatusBadRequest)
		return
	}

	log.Printf("✅ OPML parsed successfully, found %d top-level outlines", len(opml.Body.Outlines))

	// Cargar feeds existentes del usuario
	feeds := loadFeedsForUser(username)
	existingUrls := make(map[string]bool)
	for _, feed := range feeds {
		existingUrls[feed.URL] = true
	}

	log.Printf("📋 User %s has %d existing feeds", username, len(feeds))

	// Recopilar todos los feeds de forma recursiva
	var allFeeds []string
	for _, outline := range opml.Body.Outlines {
		collectFeedsRecursive(outline, &allFeeds)
	}

	log.Printf("🔍 Found %d total feeds in OPML (including nested)", len(allFeeds))

	// Importar feeds
	imported := 0
	skipped := 0
	errors := 0

	for _, feedURL := range allFeeds {
		if !existingUrls[feedURL] {
			feed := Feed{
				URL:    feedURL,
				Active: true,
			}
			if err := saveFeedForUser(feed, username); err != nil {
				log.Printf("❌ Error saving imported feed %s: %v", feedURL, err)
				errors++
			} else {
				log.Printf("✅ Imported feed: %s", feedURL)
				imported++
				existingUrls[feedURL] = true
			}
		} else {
			log.Printf("⏭️  Skipped existing feed: %s", feedURL)
			skipped++
		}
	}

	log.Printf("🎯 OPML import completed for user %s: %d imported, %d skipped, %d errors", username, imported, skipped, errors)

	result := fmt.Sprintf("Successfully imported %d feeds (%d skipped, %d errors)", imported, skipped, errors)
	w.Write([]byte(result))
}

// Función recursiva para recopilar todos los feeds del OPML
func collectFeedsRecursive(outline Outline, feeds *[]string) {
	// Si este outline tiene un xmlUrl, es un feed
	if outline.XMLURL != "" {
		*feeds = append(*feeds, outline.XMLURL)
		log.Printf("🔗 Found feed: %s (%s)", outline.Title, outline.XMLURL)
	}

	// Procesar sub-outlines recursivamente
	for _, subOutline := range outline.Outlines {
		collectFeedsRecursive(subOutline, feeds)
	}
} // Handler para exportar feeds a OPML
func exportOPMLHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	username := getUserFromRequest(r)
	feeds := loadFeedsForUser(username)

	// Crear estructura OPML
	opml := OPML{
		Version: "2.0",
		Head: Head{
			Title: "RSS Feeds Export",
		},
		Body: Body{},
	}

	// Agregar feeds al OPML
	for _, feed := range feeds {
		if feed.Active {
			outline := Outline{
				Type:   "rss",
				XMLURL: feed.URL,
				Title:  feed.URL, // Podríamos mejorar esto obteniendo el título real
			}
			opml.Body.Outlines = append(opml.Body.Outlines, outline)
		}
	}

	// Convertir a XML
	xmlData, err := xml.MarshalIndent(opml, "", "  ")
	if err != nil {
		log.Printf("❌ Error creating OPML: %v", err)
		http.Error(w, "Error creating OPML", http.StatusInternalServerError)
		return
	}

	// Configurar headers para descarga
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"feeds_%s.opml\"", username))

	// Escribir XML con declaración
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` + "\n"))
	w.Write(xmlData)

	log.Printf("✅ OPML export completed for user %s: %d feeds exported", username, len(opml.Body.Outlines))
}

// Handler para eliminar un feed
func deleteFeedHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var request struct {
		URL string `json:"url"`
	}

	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	username := getUserFromRequest(r)
	// Cargar feeds actuales del usuario
	feeds := loadFeedsForUser(username)

	// Filtrar el feed a eliminar
	var updatedFeeds []Feed
	found := false
	for _, feed := range feeds {
		if feed.URL != request.URL {
			updatedFeeds = append(updatedFeeds, feed)
		} else {
			found = true
		}
	}

	if !found {
		response := struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}{
			Success: false,
			Error:   "Feed not found",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	// Guardar feeds actualizados para el usuario
	if err := saveFeedsForUser(updatedFeeds, username); err != nil {
		response := struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
		}{
			Success: false,
			Error:   "Failed to save feeds: " + err.Error(),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
		return
	}

	response := struct {
		Success bool `json:"success"`
	}{
		Success: true,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)

	log.Printf("🗑️  Feed deleted for user %s: %s", username, request.URL)
}

func main() {
	// Limpiar sesiones expiradas cada hora
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			clearExpiredSessions()
		}
	}()

	mux := http.NewServeMux()

	// Rutas públicas (sin autenticación)
	mux.HandleFunc("/login", loginPageHandler)
	mux.HandleFunc("/api/login", loginAPIHandler)
	mux.HandleFunc("/api/preload-feeds", preloadFeedsHandler)
	mux.HandleFunc("/static/", staticHandler)

	// Rutas protegidas (con autenticación)
	mux.Handle("/", authMiddleware(http.HandlerFunc(homeHandler)))
	mux.Handle("/add", authMiddleware(http.HandlerFunc(addHandler)))
	mux.Handle("/favorite", authMiddleware(http.HandlerFunc(favoriteHandler)))
	mux.Handle("/api/favorites", authMiddleware(http.HandlerFunc(apiFavoritesHandler)))
	mux.Handle("/api/scrape-article", authMiddleware(http.HandlerFunc(scrapeArticleHandler)))
	mux.Handle("/api/feeds", authMiddleware(http.HandlerFunc(feedsAPIHandler)))
	mux.Handle("/api/check-feed", authMiddleware(http.HandlerFunc(checkFeedHandler)))
	mux.Handle("/api/delete-feed", authMiddleware(http.HandlerFunc(deleteFeedHandler)))
	mux.Handle("/upload-opml", authMiddleware(http.HandlerFunc(uploadOPMLHandler)))
	mux.Handle("/export-opml", authMiddleware(http.HandlerFunc(exportOPMLHandler)))
	mux.Handle("/clear-cache", authMiddleware(http.HandlerFunc(clearCacheHandler)))
	mux.Handle("/logout", authMiddleware(http.HandlerFunc(logoutHandler)))

	log.Println("🚀 Starting ANCAP WEB Server with Authentication...")
	log.Println("🌐 Server running at http://localhost:8080")
	log.Println("🔐 Default users: admin/admin123, ancap/libertad")

	if err := http.ListenAndServe(":8080", gzipMiddleware(mux)); err != nil {
		log.Fatalf("❌ Server failed to start: %v", err)
	}
}
