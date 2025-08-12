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
// - ancap / ghanima

// ==========================================================================================================
// 🚨🚨🚨 REGLA INQUEBRANTABLE DE FRONTEND - ESTABLECIDA 11/08/2025 🚨🚨🚨
// ==========================================================================================================
// ✅ TODO HTML/CSS/JAVASCRIPT VA HARDCODEADO EN MAIN.GO
// ❌ NUNCA CREAR TEMPLATES SEPARADOS
// ❌ NUNCA MIGRAR A ARCHIVOS EXTERNOS
// ❌ NUNCA REFACTORIZAR A TEMPLATES
// 🎯 RAZÓN: EVITAR ROMPER PROYECTO FUNCIONANDO
// 📅 USUARIO DECIDIÓ: HARDCODE FOREVER
// ==========================================================================================================
// SI ESTÁS LEYENDO ESTO Y QUIERES CAMBIAR FRONTEND: PARA Y PREGUNTA AL USUARIO PRIMERO
// ==========================================================================================================

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
	"strconv"
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
	Timestamp     int64
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

// ==========================================================================================================
// 🚨 FRONTEND HARDCODEADO AQUÍ - NO MIGRAR A TEMPLATES 🚨
// ==========================================================================================================
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
    <title>LIBERTARIAN 2.0 - ` + time.Now().Format("15:04:05") + ` [AIR]</title>
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
            src: url('/static/fonts/JetBrainsMono/JetBrainsMonoNerdFont-Bold.ttf') format('truetype');
            font-weight: 700;
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
            font-family: 'JetBrains Mono', monospace; 
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
        }
        .header-fixed {
            position: fixed;
            top: 0;
            left: 0;
            right: 0;
            background: #000;
            z-index: 1000;
            padding: 20px 0;
            border-bottom: 1px solid #333;
        }
        .header-content {
            max-width: 720px;
            margin: 0 auto;
            padding: 0 20px;
        }
        .content-wrapper {
            margin-top: 120px; /* Espacio para el header fijo - reducido */
        }
        .tabs {
            display: flex;
            width: 100%;
            margin-bottom: 0; /* Sin separación */
            border-bottom: none; /* Sin línea de separación */
        }
        .tab {
            flex: 1;
            text-align: center;
            padding: 10px;
            color: #00ff00;
            text-decoration: none;
            border: 1px solid #333;
            background: #000;
            font-family: 'JetBrains Mono', monospace;
            font-size: 12px;
            cursor: pointer;
            position: relative;
        }
        .tab:hover {
            background: #111;
        }
        .tab.tab-active {
            background: #000;
            color: #00ff00;
            font-weight: bold;
            border-bottom: 1px solid #000;
        }
        .tab-shortcut {
            font-size: 10px;
            color: #888;
            margin-left: 5px;
        }
        .tab-content {
            display: none;
            width: 100%;
        }
        .tab-content.active {
            display: block;
        }
        .tab {
            border: 1px solid #333;
            background: #000;
            font-family: 'JetBrains Mono', monospace;
            font-size: 12px;
            cursor: pointer;
        }
        .tab:hover {
            background: #111;
        }
        .tab.tab-active {
            background: #000;
            color: #00ff00;
            font-weight: bold;
            border-bottom: 1px solid #000;
        }
        h1 { 
            color: #00ff00; 
            margin-bottom: 20px; 
            font-size: 14px;
            text-align: left;
            font-family: 'JetBrains Mono', monospace;
            font-weight: 300;
            line-height: 1.2;
        }
        .info { 
            margin-bottom: 20px; 
            color: #00ff00; 
            font-size: 14px;
            text-align: left;
            font-family: 'JetBrains Mono', monospace;
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
        .article-line.selected {
            background: #222;
            border-left: 3px solid #00ff00;
        }
        .full-line-link {
            color: #00ff00;
            text-decoration: none;
            font-family: 'JetBrains Mono', monospace;
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
        .date-bracket { 
            color: #00ff00; 
            font-family: 'JetBrains Mono', monospace;
            font-weight: 300;
            font-size: 14px;
            line-height: 1.2;
        }
        .source-name { 
            color: #ffff00; 
            font-family: 'JetBrains Mono', monospace;
            font-weight: 300;
            font-size: 14px;
            line-height: 1.2;
        }
        .title { 
            color: #00ff00; 
            font-family: 'JetBrains Mono', monospace;
            font-weight: 300;
            font-size: 14px;
            line-height: 1.2;
            margin-left: 2px;
            overflow: hidden;
            text-overflow: ellipsis;
            white-space: nowrap;
            flex: 1;
            cursor: pointer;
            transition: color 0.2s ease;
        }
        .title:hover {
            color: #ffff00;
            text-decoration: underline;
        }
        }
        .article-content {
            display: none;
            background: #111;
            color: #00ff00;
            padding: 15px;
            margin: 5px 0;
            font-family: 'JetBrains Mono', monospace;
            font-size: 14px;
            line-height: 1.4;
            border-left: 3px solid #00ff00;
            white-space: normal;
            word-wrap: break-word;
            max-height: 300px;
            overflow-y: auto;
            overflow-x: hidden;
            position: relative;
        }
        .article-content img {
            max-width: 100%;
            height: auto;
            display: block;
            margin: 10px 0;
            border: 1px solid #333;
        }
        .article-content iframe,
        .article-content video,
        .article-content embed,
        .article-content object {
            max-width: 100%;
            height: auto;
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
            border-left: 3px solid #00ff00;
        }
        .article-line.read {
            opacity: 0.6;
            color: #666;
        }
        .article-line.read .date-bracket {
            color: #666;
        }
        .article-line.read .source-name {
            color: #999;
        }
        .article-line.read .title {
            color: #666;
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
            font-family: 'JetBrains Mono', monospace;
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
            border: 1px solid #333;
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
            font-family: 'JetBrains Mono', monospace;
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
    </style>
    <script>
        let selectedIndex = 0;
        let articles = [];
        let readArticles = new Set();
        
        function moveToTop(index) {
            const containers = document.querySelectorAll('.article-container');
            const container = containers[index];
            if (!container) return;
            
            const feedsContent = document.getElementById('feeds-tab');
            const info = feedsContent.querySelector('.info');
            
            // Move article to top (after info)
            feedsContent.insertBefore(container, info.nextSibling);
            
            // Update selectedIndex to 0 since moved article is now first
            selectedIndex = 0;
            scrollToShowSelected();
        }
        
        function toggleArticle(index, event) {
            if (event) event.preventDefault();
            
            const containers = document.querySelectorAll('.article-container');
            const container = containers[index];
            if (!container) return;
            
            const content = container.querySelector('.article-content');
            const line = container.querySelector('.article-line');
            
            // Close all other articles
            document.querySelectorAll('.article-content').forEach((c) => {
                c.classList.remove('expanded');
                c.style.display = 'none';
            });
            document.querySelectorAll('.article-line').forEach((l) => {
                l.classList.remove('selected');
            });
            
            // Toggle current article
            if (content.classList.contains('expanded')) {
                // Closing article - mark as read
                content.classList.remove('expanded');
                content.style.display = 'none';
                line.classList.add('read');
                readArticles.add(line.dataset.url);
                selectedIndex = 0; // Stay at top position
                scrollToShowSelected();
            } else {
                // Opening article - move to top and expand
                if (index !== 0) {
                    moveToTop(index);
                }
                content.classList.add('expanded');
                content.style.display = 'block';
                line.classList.add('selected');
                selectedIndex = 0;
            }
        }

        document.addEventListener('keydown', function(e) {
            // Ignore if user is typing in an input field
            if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') {
                return;
            }

            const totalArticles = document.querySelectorAll('.article-container').length;
            
            switch(e.key.toLowerCase()) {
                case 'f1':
                    e.preventDefault();
                    switchTab('feeds');
                    break;
                    
                case 'f2':
                    e.preventDefault();
                    switchTab('favorites');
                    break;
                    
                case 'f3':
                    e.preventDefault();
                    switchTab('saved');
                    break;
                    
                case 'f4':
                    e.preventDefault();
                    switchTab('config');
                    break;
                    
                case 'j':
                case 'arrowdown':
                    e.preventDefault();
                    const totalArticles = document.querySelectorAll('.article-container').length;
                    
                    // J: Si hay un artículo expandido, cierra el actual y abre el siguiente
                    const expandedContent = document.querySelector('.article-content.expanded');
                    if (expandedContent) {
                        // Cierra el artículo actual
                        expandedContent.classList.remove('expanded');
                        expandedContent.style.display = 'none';
                        const currentLine = expandedContent.parentElement.querySelector('.article-line');
                        currentLine.classList.add('read');
                        
                        // Mueve al siguiente artículo
                        if (selectedIndex < totalArticles - 1) {
                            selectedIndex++;
                        }
                        
                        // Abre el nuevo artículo
                        const containers = document.querySelectorAll('.article-container');
                        const newContainer = containers[selectedIndex];
                        if (newContainer) {
                            const newContent = newContainer.querySelector('.article-content');
                            const newLine = newContainer.querySelector('.article-line');
                            
                            newContent.classList.add('expanded');
                            newContent.style.display = 'block';
                            newLine.classList.add('read');
                            readArticles.add(newLine.dataset.url);
                        }
                        
                        scrollToShowSelected();
                    } else {
                        // J simplemente mueve al siguiente artículo en la lista si no hay nada expandido
                        if (selectedIndex < totalArticles - 1) {
                            selectedIndex++;
                            scrollToShowSelected();
                        }
                    }
                    break;
                
                case 'k':
                case 'arrowup':
                    e.preventDefault();
                    
                    // K: Si hay un artículo expandido, cierra el actual y abre el anterior
                    const expandedContentUp = document.querySelector('.article-content.expanded');
                    if (expandedContentUp) {
                        // Cierra el artículo actual
                        expandedContentUp.classList.remove('expanded');
                        expandedContentUp.style.display = 'none';
                        const currentLineUp = expandedContentUp.parentElement.querySelector('.article-line');
                        currentLineUp.classList.add('read');
                        
                        // Mueve al artículo anterior
                        if (selectedIndex > 0) {
                            selectedIndex--;
                        }
                        
                        // Abre el nuevo artículo
                        const containersUp = document.querySelectorAll('.article-container');
                        const newContainerUp = containersUp[selectedIndex];
                        if (newContainerUp) {
                            const newContentUp = newContainerUp.querySelector('.article-content');
                            const newLineUp = newContainerUp.querySelector('.article-line');
                            
                            newContentUp.classList.add('expanded');
                            newContentUp.style.display = 'block';
                            newLineUp.classList.add('read');
                            readArticles.add(newLineUp.dataset.url);
                        }
                        
                        scrollToShowSelected();
                    } else {
                        // K simplemente mueve al artículo anterior en la lista si no hay nada expandido
                        if (selectedIndex > 0) {
                            selectedIndex--;
                            scrollToShowSelected();
                        }
                    }
                    break;
                
                case 'enter':
                    e.preventDefault();
                    if (selectedIndex >= 0) {
                        toggleArticle(selectedIndex);
                    }
                    break;
                    
                case ' ':
                    e.preventDefault();
                    // Space abre/cierra el artículo seleccionado
                    if (selectedIndex >= 0) {
                        const containers = document.querySelectorAll('.article-container');
                        const selectedContainer = containers[selectedIndex];
                        if (selectedContainer) {
                            const content = selectedContainer.querySelector('.article-content');
                            const line = selectedContainer.querySelector('.article-line');
                            
                            if (content.classList.contains('expanded')) {
                                // Si está expandido, lo cierra
                                content.classList.remove('expanded');
                                content.style.display = 'none';
                                line.classList.add('read');
                            } else {
                                // Si no está expandido, lo abre
                                content.classList.add('expanded');
                                content.style.display = 'block';
                                line.classList.add('read');
                                readArticles.add(line.dataset.url);
                            }
                        }
                    }
                    break;
                
                case 'escape':
                    e.preventDefault();
                    closeAllArticles();
                    break;
            }
        });

        function scrollToShowSelected() {
            // Remove previous highlights
            document.querySelectorAll('.article-line').forEach(line => {
                line.classList.remove('selected');
            });
            
            // Highlight current
            const lines = document.querySelectorAll('.article-line');
            if (lines[selectedIndex]) {
                lines[selectedIndex].classList.add('selected');
                
                // Scroll para que el elemento seleccionado aparezca justo debajo del header fijo
                // El header tiene aproximadamente 120px de altura
                const headerOffset = 120;
                const elementTop = lines[selectedIndex].offsetTop;
                
                window.scrollTo({
                    top: elementTop - headerOffset,
                    behavior: 'smooth'
                });
            }
        }

        function closeAllArticles() {
            document.querySelectorAll('.article-content').forEach(content => {
                content.classList.remove('expanded');
                content.style.display = 'none';
            });
            document.querySelectorAll('.article-line').forEach(line => {
                line.classList.remove('selected');
            });
            selectedIndex = -1;
        }

        function switchTab(tabName) {
            // Remove active class from all tabs
            document.querySelectorAll('.tab').forEach(tab => {
                tab.classList.remove('active');
            });
            
            // Hide all tab content
            document.querySelectorAll('.tab-content').forEach(content => {
                content.style.display = 'none';
            });
            
            // Show selected tab content
            const tabContent = document.getElementById(tabName + '-tab');
            if (tabContent) {
                tabContent.style.display = 'block';
            }
            
            // Activate selected tab
            const tab = Array.from(document.querySelectorAll('.tab')).find(t => 
                t.textContent.toLowerCase().includes(tabName.replace('feeds', 'rss feeds').toLowerCase())
            );
            if (tab) {
                tab.classList.add('active');
            }
            
            // Reset article selection if switching to feeds
            if (tabName === 'feeds') {
                selectedIndex = 0;
                scrollToShowSelected();
            }
        }

        // Initialize articles array and set first article as selected
        document.addEventListener('DOMContentLoaded', function() {
            const containers = document.querySelectorAll('.article-container');
            containers.forEach((container, index) => {
                articles.push({
                    element: container,
                    index: index
                });
                
                // Add click handler to entire line
                const line = container.querySelector('.article-line');
                if (line) {
                    line.addEventListener('click', () => toggleArticle(index));
                }
                
                // Add click handler specifically to title for better UX
                const title = container.querySelector('.title');
                if (title) {
                    title.style.cursor = 'pointer';
                    title.addEventListener('click', (e) => {
                        e.stopPropagation(); // Prevent double triggering
                        toggleArticle(index);
                    });
                }
            });
            
            // Add click handlers for tabs
            document.querySelectorAll('.tab').forEach((tab, index) => {
                tab.addEventListener('click', () => {
                    const tabNames = ['feeds', 'favorites', 'saved', 'config'];
                    switchTab(tabNames[index]);
                });
            });
            
            // Select first article by default (but don't expand it)
            if (containers.length > 0) {
                selectedIndex = 0;
                scrollToShowSelected();
            }
            
            // Ensure all articles start closed
            document.querySelectorAll('.article-content').forEach(content => {
                content.classList.remove('expanded');
                content.style.display = 'none';
            });
            
            // Initialize with feeds tab active
            switchTab('feeds');
        });
    </script>
</head>
<body>
    <div class="header-fixed">
        <div class="header-content">
            <h1>LIBERTARIAN 2.0 - ` + time.Now().Format("15:04:05") + ` [` + strconv.Itoa(len(data.Articles)) + ` artículos]</h1>
            
            <!-- Tab Navigation -->
            <div class="tabs">
                <div class="tab tab-active" data-tab="feeds">
                    FEEDS [` + strconv.Itoa(len(data.Articles)) + `] <span class="tab-shortcut">[F1]</span>
                </div>
                <div class="tab" data-tab="favorites">
                    FAVORITOS [0] <span class="tab-shortcut">[F2]</span>
                </div>
                <div class="tab" data-tab="saved">
                    GUARDADOS [0] <span class="tab-shortcut">[F3]</span>
                </div>
                <div class="tab" data-tab="config">
                    CONFIG <span class="tab-shortcut">[F4]</span>
                </div>
            </div>
        </div>
    </div>
    
    <div class="container">
        <div class="content-wrapper">
        
        <!-- Tab Content -->
        <div id="feeds-tab" class="tab-content active">
            <div class="info">Use J/K o ↑↓ para navegar, SPACE/ENTER para expandir, ESC para cerrar, F1-F4 para pestañas</div>`

	for _, article := range data.Articles {
		html += fmt.Sprintf(`
        <div class="article-container">
            <div class="article-line" data-url="%s">
                <span class="date-bracket">%s</span>&nbsp;&nbsp;
                <span class="source-name">%s</span>&nbsp;&nbsp;
                <span class="title">%s</span>
            </div>
            <div class="article-content">
                <div style="margin-bottom: 10px; color: #888; font-size: 12px;">%s  %s</div>
                <div>%s</div>
                <br>
                <a href="%s" target="_blank" style="color: #ffff00;">→ Leer artículo completo</a>
            </div>
        </div>`,
			article.Link,
			article.Date,
			article.Source,
			article.Title,
			article.Date,
			article.Source,
			article.Description,
			article.Link)
	}

	html += `
        </div>
        
        <!-- Favorites Tab -->
        <div id="favorites-tab" class="tab-content">
            <div class="page-header">ARTÍCULOS FAVORITOS</div>
            <div class="info">Aquí aparecerán los artículos que guardes como favoritos</div>
            <div id="favorites-list">
                <!-- Contenido cargado dinámicamente -->
            </div>
        </div>
        
        <!-- Saved Tab -->
        <div id="saved-tab" class="tab-content">
            <div class="page-header">ARTÍCULOS GUARDADOS</div>
            <div class="info">Artículos guardados para leer más tarde</div>
            <div id="saved-list">
                <!-- Contenido cargado dinámicamente -->
            </div>
        </div>
        
        <!-- Config Tab -->
        <div id="config-tab" class="tab-content">
            <div class="page-header">CONFIGURACIÓN</div>
            <div class="config-section">
                <h3>Atajos de teclado</h3>
                <p><strong>F1:</strong> Feeds | <strong>F2:</strong> Favoritos | <strong>F3:</strong> Guardados | <strong>F4:</strong> Config</p>
                <p><strong>J/K o ↑/↓:</strong> Navegar artículos</p>
                <p><strong>Space/Enter:</strong> Expandir artículo</p>
                <p><strong>ESC:</strong> Cerrar artículos</p>
            </div>
            <div class="config-section">
                <h3>Información del sistema</h3>
                <p>Servidor: LIBERTARIAN 2.0</p>
                <p>Artículos cargados: ` + strconv.Itoa(len(data.Articles)) + `</p>
                <p>Última actualización: ` + time.Now().Format("15:04:05") + `</p>
            </div>
        </div>
        </div> <!-- /content-wrapper -->
    </div> <!-- /container -->
</body>
</html>`

	w.Write([]byte(html))
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
            font-family: 'JetBrains Mono', monospace; 
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
            font-family: 'JetBrains Mono', monospace;
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
	log.Println("🌐 Server running at http://localhost:8082")
	log.Println("🔐 Default users: admin/admin123, ancap/libertad")

	if err := http.ListenAndServe(":8082", gzipMiddleware(mux)); err != nil {
		log.Fatalf("❌ Server failed to start: %v", err)
	}
}
