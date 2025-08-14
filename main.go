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

type SavedArticle struct {
	Title  string `json:"title"`
	Link   string `json:"link"`
	Source string `json:"source"`
	User   string `json:"user"`
}

type LovedArticle struct {
	Title  string `json:"title"`
	Link   string `json:"link"`
	Source string `json:"source"`
	User   string `json:"user"`
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
        @import url('https://fonts.googleapis.com/css2?family=Roboto:wght@300;400;500;700&display=swap');
        @import url('https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@300;400;700&display=swap');
        /* Usar Google Fonts para JetBrains Mono y evitar rutas locales 404 */
        body { 
            background: #000; 
            color: #00ff00; 
            font-family: 'JetBrains Mono', monospace; 
            font-size: 14px; 
            font-weight: 300;
            margin: 0; 
            padding: 0 20px 20px 20px; /* sin padding-top para evitar hueco bajo el header */
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
            width: 100vw;
            background: #000 !important; /* Fondo negro simple */
            z-index: 999999 !important;
            padding: 8px 0 0 0; /* Sin espacio extra inferior */
            border-bottom: none; /* Sin línea visible */
            min-height: 50px; /* Altura muy compacta */
            overflow: hidden;
        }
        
        .header-content {
            max-width: 720px;
            margin: 0 auto;
            padding: 0 20px;
            text-align: center;
            display: flex;
            flex-direction: column;
            align-items: center;
        }
        .header-ascii {
            font-family: 'JetBrains Mono', monospace;
            color: #00ff00;
            white-space: pre;
            line-height: 1.0;
            margin-bottom: 4px;
            text-align: center;
        }
        
        .header-subtitle {
            font-size: 12px;
            color: #ffff00;
            font-family: 'JetBrains Mono', monospace;
            letter-spacing: 2px;
            text-transform: uppercase;
            margin-bottom: 3px; /* Espacio mínimo */
            font-weight: 400;
        }
        
        .header-info {
            font-size: 11px;
            color: #ffff00;
            font-family: 'JetBrains Mono', monospace;
            letter-spacing: 1px;
            text-align: center;
        }
        .content-wrapper {
            margin-top: 0; /* se ajusta dinámicamente con JS al alto real del header */
        }
        .tabs {
            display: flex;
            width: 100%;
            margin-bottom: 0; /* Sin separación */
            border-bottom: none; /* Sin línea de separación */
            position: relative;
        }
        
        /* Eliminado pseudo-elemento: el header ya cubre con padding inferior */
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
            border-bottom: none; /* sin línea inferior para evitar doble línea bajo las pestañas */
        }
        .tab:hover {
            background: #111;
        }
        .tab.active {
            background: #000;
            color: #00ff00;
            font-weight: bold; /* mantener negrita solo en pestañas activas */
            border-bottom: none; /* quitar la línea inferior cuando está activa */
        }
        .tab-shortcut {
            font-size: 10px;
            color: #888;
            margin-left: 5px;
        }
        .tab-content {
            display: none;
            width: 100%;
            position: relative; /* Para que ::before funcione */
        }
        .tab-content.active {
            display: block;
        }
        /* Eliminar posibles líneas dobles bajo tabs */
        .tabs { border-bottom: none; }

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
        .action-link {
            color: #00ff00;
            text-decoration: none;
            font-family: 'JetBrains Mono', monospace;
            font-size: 12px;
        }
        .action-link:hover { text-decoration: underline; }
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
            color: #ffffff; 
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
        .article-content {
            display: none;
            background: #111;
            color: #00ff00;
            padding: 15px;
            margin: 5px 0;
            font-family: 'Roboto', sans-serif; /* Cambio a Roboto para contenido */
            font-size: 14px;
            font-weight: 400;
            line-height: 1.4;
            border-left: 3px solid #00ff00;
            white-space: normal;
            word-wrap: break-word;
            max-height: none; /* cargar completo */
            overflow: visible; /* sin scrollbar interno */
            position: relative; /* para actions absolutas */
        }
        
        /* Asegurar que todos los artículos empiecen cerrados */
        .article-content:not(.expanded) {
            display: none !important;
        }
        .article-content img {
            max-width: 100%;
            height: auto;
            display: block;
            margin: 10px 0;
            border: 1px solid #333;
        }
        .article-content p {
            margin: 15px 0;
            line-height: 1.7;
            text-align: justify;
            word-spacing: 0.1em;
            font-family: 'Roboto', sans-serif; /* Roboto para párrafos */
            font-weight: 400;
        }
        .article-content p:first-child {
            margin-top: 0;
        }
        .article-content p:last-child {
            margin-bottom: 0;
        }
        .article-description {
            font-family: 'Roboto', sans-serif; /* Roboto para descripción */
            font-weight: 400;
            line-height: 1.6;
        }
        .article-title-full {
            font-family: 'Roboto', sans-serif; /* Roboto para título completo */
            font-weight: 700;
        }
        .article-content br {
            margin: 6px 0;
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
            color: #ffff00; /* Amarillo para contenido expandido */
            margin-bottom: 20px; /* Línea vacía debajo del artículo expandido */
        }
        .article-actions {
            position: absolute;
            top: 10px;
            right: 10px;
            display: flex;
            flex-direction: column;
            align-items: flex-end;
            gap: 6px;
        }
        .action-button {
            background: transparent;
            border: none;
            color: #ffff00;
            font-family: 'JetBrains Mono', monospace;
            font-size: 12px;
            cursor: pointer;
            padding: 0;
        }
        .action-button:hover { color: #ffff88; }
        .article-container {
            margin-bottom: 1px;
        }
        .article-line {
            cursor: pointer;
        }
        .article-line.selected {
            background: #333; /* Fondo gris más claro que el negro */
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
            font-weight: 400;
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
        let readArticles = new Set();
        const READ_KEY = 'readArticles';
        function loadReadSet() {
            try {
                const arr = JSON.parse(localStorage.getItem(READ_KEY) || '[]');
                if (Array.isArray(arr)) readArticles = new Set(arr);
            } catch(e) { readArticles = new Set(); }
        }
        function saveReadSet() {
            try { localStorage.setItem(READ_KEY, JSON.stringify(Array.from(readArticles))); } catch(e) {}
        }
        function persistRead(url) {
            if (!url) return;
            readArticles.add(url);
            saveReadSet();
        }
        function applyReadState() {
            document.querySelectorAll('.article-line').forEach(line => {
                const url = line?.dataset?.url;
                if (url && readArticles.has(url)) line.classList.add('read');
            });
        }

        loadReadSet();

        // 🔄 LiveReload por SSE (solo desarrollo). Si el servidor reinicia, el stream se corta y re-conecta => recarga.
        (function(){
            if (!('EventSource' in window)) return;
            try {
                const es = new EventSource('/dev/reload');
                let sawError = false;
                es.onopen = function(){ if (sawError) { location.reload(); } };
                es.onerror = function(){ sawError = true; };
            } catch(e) { /* ignore */ }
        })();
        
// ==========================================================================================================
// 🎯 SISTEMA DE NAVEGACIÓN PRINCIPAL - DOCUMENTACIÓN COMPLETA
// ==========================================================================================================
// 
// ✅ FUNCIONAMIENTO VERIFICADO Y FUNCIONANDO CORRECTAMENTE - 13/08/2025
// 
// FUNCIONAMIENTO:
// 1. J/K navegan por la lista completa de artículos manteniendo el estado abierto/cerrado
// 2. Si un artículo está ABIERTO y navegas (J/K), el siguiente artículo se abre automáticamente
// 3. Si un artículo está CERRADO y navegas (J/K), el siguiente artículo permanece cerrado
// 4. Space abre/cierra el artículo actual sin cambiar posición
// 5. El artículo seleccionado SIEMPRE aparece en la primera línea visible (debajo del header)
//
// VARIABLES CLAVE:
// - allArticles[]: Array con todos los artículos en orden original
// - currentPosition: Índice del artículo actualmente seleccionado
// - isNavigating: Flag para prevenir navegación múltiple simultánea
// - lastNavigationTime: Control de throttle (50ms mínimo entre navegaciones)
//
// ORDEN DE OPERACIONES EN NAVEGACIÓN:
// 1. Detectar si artículo actual está abierto
// 2. Cerrar TODOS los artículos para evitar problemas de scroll
// 3. Cambiar currentPosition al nuevo índice
// 4. Highlight del nuevo artículo
// 5. Scroll INSTANT (sin animación) para posicionar línea en top
// 6. Si el artículo anterior estaba abierto, abrir el nuevo (con delay 10ms)
//
// 🔧 CLAVES DEL ÉXITO - MODELO PARA REHACER:
// ✅ DOM INITIALIZATION: setTimeout(200ms) para esperar carga completa del DOM
// ✅ EVENT LISTENERS: Configurar DESPUÉS de initializeArticlesList() y verificar allArticles.length > 0
// ✅ NAVIGATION FLAG: isNavigating se resetea en navigateToPosition() al inicio, medio y final
// ✅ MOUSE EVENTS: Click navega a posición + highlight + scroll + toggle en una secuencia
// ✅ KEYBOARD EVENTS: J/K llaman navigateToPosition() que maneja todo el flujo completo
// ✅ NO SETTIMEOUT INNECESARIOS: Los setTimeout en keydown events eran redundantes y causaban problemas
//
// ==========================================================================================================        // Variables para navegación simple de lista
        let allArticles = []; // Lista completa de todos los artículos en orden original
        let currentPosition = 0; // Posición actual en la lista
        let isNavigating = false; // Flag para prevenir navegación múltiple simultánea
        let lastNavigationTime = 0; // Timestamp de la última navegación

        function initializeArticlesList() {
            console.log('🔍 initializeArticlesList called');
            
            // Capturar artículos SOLO del tab activo
            const activeContent = document.querySelector('.tab-content.active');
            const containers = activeContent 
                ? activeContent.querySelectorAll('.article-container')
                : document.querySelectorAll('.article-container');
            console.log('🔍 DOM containers found: ' + containers.length);
            
            if (containers.length === 0) {
                console.log('❌ CRITICAL: No containers found with selector .article-container');
                // Test other selectors
                const divs = document.querySelectorAll('div');
                console.log('🔍 Total divs in document:', divs.length);
                return;
            }
            console.log('🔍 DOM containers found: ' + containers.length);
            
            allArticles = Array.from(containers).map((container, index) => {
                const line = container.querySelector('.article-line');
                if (!line) {
                    console.log('❌ No article-line found in container ' + index);
                    return null;
                }
                const url = line.dataset.url;
                const titleElement = line.querySelector('.title');
                const title = titleElement ? titleElement.textContent : 'No title';
                console.log('📄 Article ' + index + ': ' + title.substring(0, 50) + '...');
                const src = container.querySelector('.source-name')?.textContent || '';
                return {
                    element: container,
                    url: url,
                    originalIndex: index,
                    title: title,
                    searchText: (src + ' ' + title).toLowerCase()
                };
            }).filter(article => article !== null);
            
            currentPosition = 0;
            console.log('✅ Initialized articles list with ' + allArticles.length + ' articles (scoped to active tab)');
            
            if (allArticles.length === 0) {
                console.log('❌ NO ARTICLES FOUND IN DOM!');
                return;
            }
            
            // Buscar el primer artículo no leído para posicionarse ahí
            // SIMPLIFICADO: Empezar en posición 0 por ahora
            currentPosition = 0;
            console.log('🎯 Starting at position 0 (simplified)');
            
            console.log('🥇 Current article: ' + allArticles[currentPosition]?.title?.substring(0, 50) + '...');
            
            // Highlight initial article
            highlightCurrentArticle();
            // Configurar interacción por clic para el tab activo
            setupArticleInteractionHandlers();
        }

        function highlightCurrentArticle() {
            // FUNCIÓN: Resalta visualmente el artículo actual
            // - Remueve la clase 'selected' de todos los artículos
            // - Agrega la clase 'selected' solo al artículo en currentPosition
            // - La clase 'selected' tiene background #333 (definido en CSS)
            
            // Remover highlight de todos los artículos
            document.querySelectorAll('.article-line').forEach(line => {
                line.classList.remove('selected');
            });
            
            // Highlight del artículo actual
            if (allArticles[currentPosition]) {
                const currentArticle = allArticles[currentPosition];
                const line = currentArticle.element.querySelector('.article-line');
                if (line) {
                    line.classList.add('selected');
                }
            }
        }

        function setupArticleInteractionHandlers() {
            if (allArticles.length === 0) return;
            allArticles.forEach((article, index) => {
                const line = article.element.querySelector('.article-line');
                if (line) {
                    line.onclick = () => {
                        currentPosition = index;
                        highlightCurrentArticle();
                        scrollToCurrentArticle();
                        toggleCurrentArticle();
                    };
                }
                const title = article.element.querySelector('.title');
                if (title) {
                    title.style.cursor = 'pointer';
                    title.onclick = (e) => {
                        e.stopPropagation();
                        currentPosition = index;
                        highlightCurrentArticle();
                        scrollToCurrentArticle();
                        toggleCurrentArticle();
                    };
                }
            });
        }

        // Function to load full article content
        async function loadFullContentIfNeeded(contentElement) {
            const fullContentDiv = contentElement.querySelector('.article-full-content');
            const loadingIndicator = contentElement.querySelector('.loading-indicator');
            const descriptionDiv = contentElement.querySelector('.article-description');
            const articleUrl = contentElement.getAttribute('data-article-url');
            
            // Check if content is already loaded
            if (fullContentDiv.innerHTML.trim() !== '') {
                // Content already loaded, just show it
                descriptionDiv.style.display = 'none';
                fullContentDiv.style.display = 'block';
                return;
            }
            
            // Show loading indicator
            loadingIndicator.style.display = 'block';
            
            try {
                const response = await fetch('/api/scrape-article', {
                    method: 'POST',
                    headers: {
                        'Content-Type': 'application/json',
                    },
                    body: JSON.stringify({ url: articleUrl })
                });
                
                if (!response.ok) {
                    throw new Error('HTTP error! status: ' + response.status);
                }
                
                const data = await response.json();
                
                // Hide loading indicator
                loadingIndicator.style.display = 'none';
                
                // Process and format the content
                let formattedContent = data.content;
                
                // Basic formatting improvements
                if (formattedContent) {
                    // Remove extra whitespace and normalize
                    formattedContent = formattedContent
                        .trim()
                        .replace(/\s+/g, ' ')
                        .replace(/\n+/g, '\n');
                    
                    // Split into sentences and create proper paragraphs
                    let sentences = formattedContent.split(/(?<=[.!?])\s+(?=[A-Z])/);
                    
                    // Group sentences into paragraphs (every 3-4 sentences)
                    let paragraphs = [];
                    let currentParagraph = [];
                    
                    sentences.forEach((sentence, index) => {
                        currentParagraph.push(sentence.trim());
                        
                        // Create new paragraph every 3-4 sentences or at natural breaks
                        if (currentParagraph.length >= 3 || 
                            sentence.includes('"') || 
                            sentence.length > 200 ||
                            (index === sentences.length - 1)) {
                            
                            paragraphs.push(currentParagraph.join(' '));
                            currentParagraph = [];
                        }
                    });
                    
                    // Join paragraphs with proper HTML formatting
                    formattedContent = paragraphs
                        .filter(p => p.trim().length > 0)
                        .map(p => '<p>' + p.trim() + '</p>')
                        .join('');
                }
                
                // Hide description and show full content
                descriptionDiv.style.display = 'none';
                fullContentDiv.innerHTML = formattedContent || data.content;
                fullContentDiv.style.display = 'block';
                
            } catch (error) {
                console.error('Error loading full content:', error);
                loadingIndicator.innerHTML = '❌ Error cargando contenido completo. <a href="' + articleUrl + '" target="_blank" style="color: #ffff00;">→ Leer original</a>';
            }
        }

        function scrollToCurrentArticle() {
            // FUNCIÓN CRÍTICA: La línea activa DEBE quedar justo debajo del header (línea 0)
            if (!allArticles[currentPosition]) {
                console.log('❌ No current article to scroll to');
                return;
            }

            const currentArticle = allArticles[currentPosition];
            const line = currentArticle.element.querySelector('.article-line');
            if (!line) {
                console.log('❌ No article line found');
                return;
            }

            const header = document.querySelector('.header-fixed');
            const headerRect = header ? header.getBoundingClientRect() : { bottom: 0 };
            const lineRect = line.getBoundingClientRect();
            const currentScroll = window.scrollY || document.documentElement.scrollTop || 0;

            // Offset 0: el primer ítem queda inmediatamente bajo el header
            const SAFE_OFFSET = 0;

            const delta = lineRect.top - (headerRect.bottom + SAFE_OFFSET);
            const targetScroll = currentScroll + delta;

            console.log('🎯 FORCING article to line 0 - offset:', SAFE_OFFSET, 'delta:', Math.round(delta));

            window.scrollTo({
                top: Math.max(0, Math.round(targetScroll)),
                behavior: 'instant'
            });

            // Micro-corrección en el siguiente frame por redondeos de layout/zoom
            requestAnimationFrame(() => {
                const hr = (header ? header.getBoundingClientRect() : { bottom: 0 });
                const lr = line.getBoundingClientRect();
                const d2 = lr.top - (hr.bottom + SAFE_OFFSET);
                if (Math.abs(d2) > 1) {
                    window.scrollTo({
                        top: Math.max(0, Math.round((window.scrollY || document.documentElement.scrollTop || 0) + d2)),
                        behavior: 'instant'
                    });
                }
                console.log('✅ Article positioned at line 0 (post-correct:', Math.round(d2), 'px)');
            });
        }

        function navigateToPosition(newPosition) {
            console.log('🧭 navigateToPosition called: from', currentPosition, 'to', newPosition);
            
            if (newPosition < 0 || newPosition >= allArticles.length) {
                console.log('❌ Position out of range:', newPosition, '(max:', allArticles.length - 1, ')');
                isNavigating = false; // IMPORTANTE: resetear flag
                return false;
            }

            // PASO 1: Verificar si el artículo actual está abierto
            // LÓGICA: Si está abierto, el nuevo artículo también se abrirá automáticamente
            // Y el artículo actual se marcará como leído
            let wasCurrentArticleOpen = false;
            if (allArticles[currentPosition]) {
                const currentArticle = allArticles[currentPosition];
                const currentContent = currentArticle.element.querySelector('.article-content');
                const currentLine = currentArticle.element.querySelector('.article-line');
                wasCurrentArticleOpen = currentContent.classList.contains('expanded');
                console.log('📊 Current article was:', (wasCurrentArticleOpen ? 'OPEN' : 'CLOSED'));
                
                // Si estaba abierto, marcarlo como leído al navegar
                if (wasCurrentArticleOpen) {
                    currentLine.classList.add('read');
                    console.log('📖 Marking as read:', currentArticle.title.substring(0, 30) + '...');
                }
            }

            // PASO 2: Cambiar posición
            const oldPosition = currentPosition;
            currentPosition = newPosition;
            console.log('🔄 Position updated: from', oldPosition, 'to', currentPosition);
            
            // PASO 3: Cerrar todos los artículos primero para evitar problemas de scroll
            // CRÍTICO: Esto debe hacerse ANTES del scroll para calcular posiciones correctas
            document.querySelectorAll('.article-content').forEach(content => {
                content.classList.remove('expanded');
                content.style.display = 'none';
            });
            
            // PASO 4: Actualizar UI y posicionar
            highlightCurrentArticle();
            scrollToCurrentArticle();
            
            // PASO 5: Si el artículo anterior estaba abierto, abrir el nuevo DESPUÉS del scroll
            // DELAY: 10ms asegura que el scroll instant se complete antes de expandir contenido
                if (wasCurrentArticleOpen && allArticles[currentPosition]) {
                const newArticle = allArticles[currentPosition];
                const newContent = newArticle.element.querySelector('.article-content');
                
                // Usar setTimeout para asegurar que el scroll se complete primero
                setTimeout(() => {
                    newContent.classList.add('expanded');
                    newContent.style.display = 'block';
                    console.log('📰 Auto-abriendo nuevo artículo: ' + newArticle.title.substring(0, 30) + '...');
                        const newLine = newArticle.element.querySelector('.article-line');
                        if (newLine && newLine.dataset && newLine.dataset.url) {
                            newLine.classList.add('read');
                            persistRead(newLine.dataset.url);
                        }
                }, 10);
            }
            
            console.log('✅ Navegación completada a posición: ' + currentPosition);
            console.log('📄 Artículo actual: ' + allArticles[currentPosition].title.substring(0, 50) + '...');
            
            isNavigating = false; // IMPORTANTE: resetear flag al finalizar
            return true;
        }

        function toggleCurrentArticle() {
            // FUNCIÓN: Abre/cierra el artículo actual sin cambiar la posición
            // - Si está cerrado -> lo abre
            // - Si está abierto -> lo cierra Y marca como leído (clase 'read')
            // - NO afecta la navegación ni la posición del scroll
            
            if (allArticles[currentPosition]) {
                const currentArticle = allArticles[currentPosition];
                const container = currentArticle.element;
                const content = container.querySelector('.article-content');
                const line = container.querySelector('.article-line');
                
                if (content.classList.contains('expanded')) {
                    // Si está expandido, lo cierra
                    content.classList.remove('expanded');
                    content.style.display = 'none';
                    line.classList.add('read');
                    if (line && line.dataset && line.dataset.url) persistRead(line.dataset.url);
                    console.log('📖 Article closed and marked as read: ' + currentArticle.title.substring(0, 30) + '...');
                } else {
                    // Si no está expandido, lo abre
                    content.classList.add('expanded');
                    content.style.display = 'block';
                    // Cargar contenido completo si corresponde
                    if (typeof loadFullContentIfNeeded === 'function') {
                        try { loadFullContentIfNeeded(content); } catch (e) { /* ignore */ }
                    }
                    console.log('📰 Article opened: ' + currentArticle.title.substring(0, 30) + '...');
                }
            }
        }

        async function saveCurrent(listName) {
            if (!allArticles[currentPosition]) return;
            const el = allArticles[currentPosition].element;
            const line = el.querySelector('.article-line');
            const title = el.querySelector('.title')?.textContent || '';
            const source = el.querySelector('.source-name')?.textContent || '';
            const link = line?.dataset.url || '';
            if (!link) return;
            try {
                const endpoint = listName === 'loved' ? '/api/save-loved' : '/api/save-saved';
                const res = await fetch(endpoint, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ title, source, link })
                });
                if (!res.ok) throw new Error(await res.text());
                const data = await res.json().catch(()=>({}));
                // Actualizar contadores en pestañas y listas
                if (typeof refreshLists === 'function') {
                    refreshLists();
                }
            } catch(e) {
                console.error('Save failed', e);
            }
        }

        // Guardar desde enlaces de acción en cada artículo
        async function saveToList(listName, el) {
            const container = el.closest('.article-container');
            const line = container?.querySelector('.article-line');
            const title = container?.querySelector('.title')?.textContent || '';
            const source = container?.querySelector('.source-name')?.textContent || '';
            const link = line?.dataset?.url || '';
            if (!link) return;
            // Evitar duplicados en UI
            if (listName === 'saved' && document.querySelector('#saved-list .article-line[data-url="'+link.replace(/"/g,'&quot;')+'"]')) return;
            if (listName === 'loved' && document.querySelector('#favorites-list .article-line[data-url="'+link.replace(/"/g,'&quot;')+'"]')) return;
            try {
                const endpoint = listName === 'loved' ? '/api/save-loved' : '/api/save-saved';
                const res = await fetch(endpoint, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ title, source, link })
                });
                if (!res.ok) throw new Error(await res.text());
                el.textContent = (listName === 'loved' ? 'LOVED' : 'SAVED') + ' ✓';
                if (typeof refreshLists === 'function') refreshLists();
            } catch(e) {
                console.error('Save failed', e);
                el.textContent = 'ERROR';
            }
        }

        // Cargar listas y actualizar contadores
        async function refreshLists() {
            try {
                const [savedRes, lovedRes] = await Promise.all([
                    fetch('/api/list-saved'),
                    fetch('/api/list-loved')
                ]);
                const saved = await savedRes.json();
                const loved = await lovedRes.json();
                const elSaved = document.getElementById('saved-list');
                const elFav   = document.getElementById('favorites-list');
                function renderItem(i){
                    const url = (i.link||'');
                    const readCls = (readArticles && readArticles.has && readArticles.has(url)) ? ' read' : '';
                    return '<div class="article-container">'
                         + '<div class="article-line'+readCls+'" data-url="'+url+'">'
                         + '<span class="source-name">'+(i.source||'')+'</span>&nbsp;'
                         + '<span class="title">'+(i.title||'')+'</span>'
                         + '</div>'
                         + '<div class="article-content" data-article-url="'+url+'">'
                         +   '<div style="height: 15px;"></div>'
                         +   '<div class="article-title-full" style="color: #ffffff; font-weight: 400; font-size: 16px; margin-bottom: 15px; line-height: 1.3;">'+(i.title||'')+'</div>'
                         +   '<div class="article-description"></div>'
                         +   '<div class="article-full-content" style="display: none;"></div>'
                         +   '<div class="loading-indicator" style="display: none; color: #00ff00; margin: 10px 0;">⏳ Cargando contenido completo...</div>'
                         + '</div>'
                         + '</div>';
                }
                if (elSaved) elSaved.innerHTML = saved.map(renderItem).join('');
                if (elFav)   elFav.innerHTML   = loved.map(renderItem).join('');
                const cSaved = document.getElementById('count-saved');
                const cLoved = document.getElementById('count-loved');
                if (cSaved) cSaved.textContent = String(saved.length || 0);
                if (cLoved) cLoved.textContent = String(loved.length || 0);

                // Si estamos en SAVED o LOVED, reconstruir navegación
                const activeTab = document.querySelector('.tab.active')?.dataset?.tab;
                if (activeTab === 'favorites' || activeTab === 'saved') {
                    initializeArticlesList();
                    highlightCurrentArticle();
                    setupArticleInteractionHandlers();
                }
                return { savedCount: saved.length, lovedCount: loved.length };
            } catch(e) {
                console.error('refreshLists failed', e);
                return { savedCount: 0, lovedCount: 0 };
            }
        }

        function reorganizeArticles() {
            // En lugar de reorganizar físicamente, solo actualizamos la navegación
            // Los artículos mantienen su orden original
            // La navegación simplemente salta a los no leídos disponibles
            selectedIndex = 0;
            
            // Buscar el primer artículo no leído para posicionarse ahí
            const containers = document.querySelectorAll('.article-container');
            for (let i = 0; i < containers.length; i++) {
                const line = containers[i].querySelector('.article-line');
                if (!line.classList.contains('read')) {
                    selectedIndex = i;
                    break;
                }
            }
            
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
                l.classList.remove('expanded');
            });
            
            // Toggle current article
            if (content.classList.contains('expanded')) {
                // Closing article - mark as read
                content.classList.remove('expanded');
                content.style.display = 'none';
                line.classList.add('read');
                line.classList.remove('expanded');
                const toggleUrl = line.dataset.url;
                if (toggleUrl) {
                    readArticles.add(toggleUrl);
                    console.log('Marked as read (toggle):', toggleUrl);
                }
                
                // Simply stay in current position without reorganizing
                selectedIndex = index;
                scrollToShowSelected();
            } else {
                // Opening article - expand in place without moving
                content.classList.add('expanded');
                content.style.display = 'block';
                line.classList.add('selected');
                line.classList.add('expanded');
                selectedIndex = index;
                
                // Load full content if not already loaded
                loadFullContentIfNeeded(content);
                scrollToShowSelected();
            }
        }

        function scrollToShowSelected() {
            // DESHABILITADO - usando nuevo sistema de navegación
            // Remove previous highlights
            document.querySelectorAll('.article-line').forEach(line => {
                line.classList.remove('selected');
            });
            
            // Highlight current (solo en posición 0)
            const containers = document.querySelectorAll('.article-container');
            if (containers[0]) {
                const firstLine = containers[0].querySelector('.article-line');
                firstLine.classList.add('selected');
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
            
            // Hide all tab content y quitar 'active'
            document.querySelectorAll('.tab-content').forEach(content => {
                content.style.display = 'none';
                content.classList.remove('active');
            });
            
            // Show selected tab content
            const tabContent = document.getElementById(tabName + '-tab');
            if (tabContent) {
                tabContent.style.display = 'block';
                tabContent.classList.add('active');
            }
            
            // Activate selected tab
            const tab = Array.from(document.querySelectorAll('.tab')).find(t => 
                t.dataset.tab === tabName
            );
            if (tab) {
                tab.classList.add('active');
            }
            
            // Reset article selection if switching to feeds
            if (tabName === 'feeds') {
                selectedIndex = 0;
                scrollToShowSelected();
            }

            if (tabName === 'search') {
                const si = document.getElementById('search-input');
                const host = document.getElementById('search-results');
                if (si && host) {
                    const doSearch = () => {
                        const q = si.value.trim().toLowerCase();
                        if (!q) { host.innerHTML = ''; return; }
                        const results = allArticles.filter(a => a.searchText.includes(q));
                        host.innerHTML = results.map(r => {
                            const line = r.element.querySelector('.article-line').outerHTML;
                            return '<div class="article-container">' + line + '</div>';
                        }).join('');
                    };
                    si.onkeydown = (ev) => {
                        if (ev.key === 'Escape') { si.value = ''; host.innerHTML = ''; }
                        if (ev.key === 'Enter') { ev.preventDefault(); doSearch(); }
                    };
                }
            }

            // Si es SAVED o LOVED, refrescar listas antes de reindexar
            // Al cambiar de pestaña, refrescar y luego reindexar
            if (tabName === 'favorites' || tabName === 'saved') {
                if (typeof refreshLists === 'function') {
                    refreshLists().then(() => {
                        initializeArticlesList();
                        highlightCurrentArticle();
                        setupArticleInteractionHandlers();
                    }).catch(() => {
                        initializeArticlesList();
                        highlightCurrentArticle();
                        setupArticleInteractionHandlers();
                    });
                } else {
                    initializeArticlesList();
                    highlightCurrentArticle();
                    setupArticleInteractionHandlers();
                }
            } else if (tabName === 'feeds' || tabName === 'search') {
                setTimeout(() => {
                    initializeArticlesList();
                    highlightCurrentArticle();
                    setupArticleInteractionHandlers();
                }, 0);
            }
        }

        // Initialize articles array and set first article as selected
        document.addEventListener('DOMContentLoaded', function() {
            console.log('🚀 DOMContentLoaded fired - Initializing ANCAP WEB Reader...');
            console.log('🔍 Document ready state:', document.readyState);
            
            // PASO 1: Esperar un momento para que el DOM esté completamente cargado
            setTimeout(() => {
                console.log('🔧 Starting article initialization after timeout...');
                
                // TEST: Verificar que elementos básicos existen
                const feedsTab = document.querySelector('#feeds-tab');
                const articleContainers = document.querySelectorAll('.article-container');
                console.log('🔍 feeds-tab found:', !!feedsTab);
                console.log('🔍 article-container elements found:', articleContainers.length);

                // Ajuste robusto del offset del contenido para eliminar huecos bajo las pestañas
                const headerEl = document.querySelector('.header-fixed');
                const tabsEl = document.querySelector('.tabs');
                const contentWrapper = document.querySelector('.content-wrapper');

                function adjustContentOffset() {
                    if (!contentWrapper) return;
                    // Asegurar que no haya padding/margen residual
                    contentWrapper.style.paddingTop = '0px';
                    contentWrapper.style.marginTop = '0px';

                    let offsetPx = 0;
                    if (tabsEl) {
                        const rect = tabsEl.getBoundingClientRect();
                        // Altura efectiva desde la parte superior de la ventana
                        offsetPx = Math.ceil(rect.bottom);
                    } else if (headerEl) {
                        offsetPx = Math.ceil(headerEl.getBoundingClientRect().height);
                    }
                    // Dejar exactamente una "línea" negra antes del listado
                    let extraGap = 18; // fallback
                    const sampleLine = document.querySelector('.article-line');
                    if (sampleLine) {
                        const lh = Math.ceil(sampleLine.getBoundingClientRect().height);
                        if (lh > 0 && lh < 60) extraGap = lh; // usar altura de línea si es razonable
                    }
                    offsetPx += extraGap;
                    contentWrapper.style.marginTop = offsetPx + 'px';
                    console.log('📐 Offset aplicado a content-wrapper:', offsetPx);
                }

                adjustContentOffset();
                window.addEventListener('load', adjustContentOffset, { passive: true });
                window.addEventListener('resize', adjustContentOffset, { passive: true });
                if (document.fonts && document.fonts.ready) {
                    document.fonts.ready.then(adjustContentOffset).catch(()=>{});
                }
                
                if (articleContainers.length === 0) {
                    console.log('❌ CRITICAL: No article containers found in DOM!');
                    return;
                }
                
                // LIMPIEZA INICIAL: Cerrar todos los artículos
                document.querySelectorAll('.article-content').forEach(content => {
                    content.classList.remove('expanded');
                    content.style.display = 'none';
                });
                document.querySelectorAll('.article-line').forEach(line => {
                    line.classList.remove('selected', 'expanded');
                });
                // Aplicar estado de leídos desde localStorage
                applyReadState();
                
                // PASO 2: Inicializar la lista de artículos para navegación
                console.log('🔧 Calling initializeArticlesList...');
                initializeArticlesList();
                // Posicionar el primer elemento seleccionado al cargar
                scrollToCurrentArticle();
                
                // PASO 3: Configurar event listeners de click SOLO si hay artículos
                if (allArticles.length > 0) {
                    console.log('🖱️ Setting up mouse event listeners for ' + allArticles.length + ' articles');
                    allArticles.forEach((article, index) => {
                        // Add click handler to entire line
                        const line = article.element.querySelector('.article-line');
                        if (line) {
                            line.addEventListener('click', () => {
                                // Al hacer click, navegar a ese artículo y togglear
                                currentPosition = index;
                                highlightCurrentArticle();
                                scrollToCurrentArticle();
                                toggleCurrentArticle();
                            });
                        }
                        
                        // Add click handler specifically to title for better UX
                        const title = article.element.querySelector('.title');
                        if (title) {
                            title.style.cursor = 'pointer';
                            title.addEventListener('click', (e) => {
                                e.stopPropagation();
                                // Al hacer click en título, navegar a ese artículo y togglear
                                currentPosition = index;
                                highlightCurrentArticle();
                                scrollToCurrentArticle();
                                toggleCurrentArticle();
                            });
                        }
                    });
                    console.log('✅ Mouse event listeners configured successfully');
                } else {
                    console.log('❌ No articles found, skipping mouse event listeners setup');
                }
                
                console.log('✅ ANCAP WEB Reader initialization completed');
            }, 200); // Aumentar timeout para asegurar que el DOM esté listo
            
            // Configurar event listeners de tabs
            document.querySelectorAll('.tab').forEach((tab, index) => {
                tab.addEventListener('click', () => {
                    const tabNames = ['feeds', 'search', 'favorites', 'saved', 'config'];
                    switchTab(tabNames[index]);
                });
            });
            
            // Reloj en vivo con fecha local
            const clockEl = document.getElementById('clock-info');
            const updateClock = () => {
                const now = new Date();
                const date = now.toLocaleDateString();
                const time = now.toLocaleTimeString();
                clockEl.textContent = time + ' [' + allArticles.length + ' artículos] — ' + date;
            };
            setInterval(updateClock, 1000);
            updateClock();

            // Activar tab de feeds por defecto
            switchTab('feeds');
            // Inicializar contadores al cargar
            if (typeof refreshLists === 'function') {
                refreshLists();
            }
            
            // ==========================================================================================================
            // 🎹 CONFIGURACIÓN DE TECLAS - SISTEMA DE NAVEGACIÓN COMPLETO
            // ==========================================================================================================
            console.log('🎹 Setting up keyboard event listeners...');
            
            // TEST: Verificar que el document está listo para event listeners
            console.log('🔍 Document ready for event listeners:', !!document.addEventListener);
            console.log('🔍 allArticles.length:', allArticles.length);
            
            document.addEventListener('keydown', function(e) {
                console.log('🎹 Key pressed:', e.key, 'code:', e.code);
                
                // Ignore if user is typing in an input field
                if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') {
                    console.log('🚫 Ignoring key press in input field');
                    return;
                }
                
                const key = (e.key || '').toLowerCase();

                // Manejo de F1..F5 siempre, para evitar que el navegador capture F1
                if (key === 'f1' || key === 'f2' || key === 'f3' || key === 'f4' || key === 'f5') {
                    e.preventDefault();
                    if (key === 'f1') switchTab('feeds');
                    if (key === 'f2') { 
                        switchTab('search'); 
                        // No enfocar automáticamente para no interferir con F1/F3
                        // El usuario puede hacer clic o presionar '/' para enfocar manualmente
                    }
                    if (key === 'f3') { switchTab('favorites'); refreshLists(); }
                    if (key === 'f4') { switchTab('saved'); refreshLists(); }
                    if (key === 'f5') switchTab('config');
                    return;
                }

                // Si no son teclas de pestañas, requerimos artículos para navegar
                if (allArticles.length === 0) {
                    console.log('❌ No articles available for navigation');
                    return;
                }

                // Permitir key repeat del sistema operativo para mantener presionadas J/K
                const totalArticles = document.querySelectorAll('.article-container').length;
                console.log('🎯 Key pressed: ' + e.key + ', totalArticles: ' + totalArticles);
                
                switch(key) {
                    case 'f1':
                        e.preventDefault();
                        switchTab('feeds');
                        break;

                    case 'f2':
                        e.preventDefault();
                        switchTab('search');
                        const si2 = document.getElementById('search-input');
                        if (si2) si2.focus();
                        break;

                    case 'f3':
                        e.preventDefault();
                        switchTab('favorites');
                        break;

                    case 'f4':
                        e.preventDefault();
                        switchTab('saved');
                        break;

                    case 'f5':
                        e.preventDefault();
                        switchTab('config');
                        break;
                        
                    case 'j':
                    case 'arrowdown':
                        e.preventDefault();
                        console.log('⬇️ J pressed - moving to next article');
                        
                        // Throttle equilibrado para navegación una por una: 30ms
                        const now = Date.now();
                        const timeSinceLastNav = now - lastNavigationTime;
                        
                        if (timeSinceLastNav < 30) {
                            console.log('⏳ Navigation throttled - skipping');
                            return;
                        }
                        
                        // Prevenir navegación múltiple simultánea
                        if (isNavigating) {
                            console.log('🚫 Already navigating - flag is true');
                            return;
                        }
                        
                        isNavigating = true;
                        lastNavigationTime = now;
                        
                        // J: Navegar al siguiente artículo (SIMPLE)
                        const nextPos = currentPosition + 1;
                        
                        if (nextPos < allArticles.length && navigateToPosition(nextPos)) {
                            console.log('📄 Navigated to article', nextPos + 1, 'of', allArticles.length);
                        } else {
                            console.log('🔚 Already at last article');
                            isNavigating = false;
                        }
                        break;
                    
                    case 'k':
                    case 'arrowup':
                        e.preventDefault();
                        console.log('⬆️ K pressed - moving to previous article');
                        
                        // Throttle equilibrado para navegación una por una: 30ms
                        const nowK = Date.now();
                        const timeSinceLastNavK = nowK - lastNavigationTime;
                        
                        if (timeSinceLastNavK < 30) {
                            console.log('⏳ Navigation throttled - skipping');
                            return;
                        }
                        
                        // Prevenir navegación múltiple simultánea
                        if (isNavigating) {
                            console.log('🚫 Already navigating - flag is true');
                            return;
                        }
                        
                        isNavigating = true;
                        lastNavigationTime = nowK;
                        
                        // K: Navegar al artículo anterior (SIMPLE)
                        const prevPos = currentPosition - 1;
                        
                        if (prevPos >= 0 && navigateToPosition(prevPos)) {
                            console.log('📄 Navigated to article', prevPos + 1, 'of', allArticles.length);
                        } else {
                            console.log('🔙 Already at first article');
                            isNavigating = false;
                        }
                        break;
                    
                    case ' ':
                        e.preventDefault();
                        console.log('🔲 Space pressed - toggle current article');
                        toggleCurrentArticle();
                        break;
                    case 's':
                        e.preventDefault();
                        console.log('💾 S pressed - save current');
                        saveCurrent('saved');
                        break;
                    case 'l':
                        e.preventDefault();
                        console.log('❤️ L pressed - love current');
                        saveCurrent('loved');
                        break;
                    
                    case 'escape':
                        e.preventDefault();
                        console.log('🚪 Escape pressed - closing all articles');
                        closeAllArticles();
                        break;
                }
            });
            
            console.log('✅ ANCAP WEB Reader initialized');
        });
    </script>
</head>
<body>
    <div class="header-fixed">
        <div class="header-content">
            <div class="header-ascii"> </div>
            <div class="header-ascii">▄▀█ █▄ █ █▀▀ ▄▀█ █▀▄
█▀█ █ ▀█ █▄▄ █▀█ █▀▀</div>
            <div class="header-ascii"> </div>
            <div class="header-subtitle">» A LIBERTARIAN RSS READER «</div>
            <div class="header-ascii"> </div>
            <div class="header-info" id="clock-info"></div>
            <div class="header-ascii"> </div>
            
            <!-- Tab Navigation -->
            <div class="tabs">
                <div class="tab tab-active" data-tab="feeds">
                    FEEDS [` + strconv.Itoa(len(data.Articles)) + `] <span class="tab-shortcut">[F1]</span>
                </div>
                <div class="tab" data-tab="search">
                    SEARCH <span class="tab-shortcut">[F2]</span>
                </div>
                <div class="tab" data-tab="favorites">
                    SAVED [<span id="count-saved">0</span>] <span class="tab-shortcut">[F3]</span>
                </div>
                <div class="tab" data-tab="saved">
                    LOVED [<span id="count-loved">0</span>] <span class="tab-shortcut">[F4]</span>
                </div>
                <div class="tab" data-tab="config">
                    CONFIG <span class="tab-shortcut">[F5]</span>
                </div>
            </div>
            <div class="header-ascii"> </div>
        </div>
    </div>
    
    <div class="container">
        <div class="content-wrapper">
        
        <!-- Tab Content -->
        <div id="feeds-tab" class="tab-content active">`

	for _, article := range data.Articles {
		html += fmt.Sprintf(`
        <div class="article-container">
            <div class="article-line" data-url="%s">
                <span class="source-name">%s</span>&nbsp;
                <span class="title">%s</span>
            </div>
            <div class="article-content" data-article-url="%s">
                <div style="height: 15px;"></div>
                <div class="article-title-full" style="color: #ffffff; font-weight: 400; font-size: 16px; margin-bottom: 15px; line-height: 1.3;">%s</div>
                <div class="article-description">%s</div>
            <div class="article-actions" style="margin-top:8px;">
                    <a href="#" class="action-link" onclick="event.preventDefault(); saveToList('loved', this)">LOVE [L]</a>
                    <span style="margin:0 8px; color:#333;">|</span>
                    <a href="#" class="action-link" onclick="event.preventDefault(); saveToList('saved', this)">SAVE [S]</a>
                    <span style="margin:0 8px; color:#333;">|</span>
                    <a href="#" class="action-link" onclick="event.preventDefault(); shareArticle(this)">SHARE</a>
                </div>
                <div class="article-full-content" style="display: none;"></div>
                <div class="loading-indicator" style="display: none; color: #00ff00; margin: 10px 0;">⏳ Cargando contenido completo...</div>
            </div>
        </div>`,
			article.Link,
			article.Source,
			article.Title,
			article.Link,  // data-article-url for JS
			article.Title, // Título completo en blanco
			article.Description)
	}

	html += `
        </div>
        
        <!-- Favorites Tab -->
        <div id="favorites-tab" class="tab-content">
            <div class="page-header">SAVED</div>
            <div class="info">Aquí aparecerán los artículos que guardes como favoritos</div>
            <div id="favorites-list">
                <!-- Contenido cargado dinámicamente -->
            </div>
        </div>
        
        <!-- Saved Tab -->
        <div id="saved-tab" class="tab-content">
            <div class="page-header">LOVED</div>
            <div class="info">Artículos guardados para leer más tarde</div>
            <div id="saved-list">
                <!-- Contenido cargado dinámicamente -->
            </div>
        </div>
        
        <!-- Search Tab -->
        <div id="search-tab" class="tab-content">
            <div class="page-header">SEARCH</div>
            <div class="info">Busca por título o fuente. Presiona Enter para filtrar, ESC para limpiar.</div>
            <input id="search-input" class="config-input" placeholder="Buscar..." style="width: 100%; max-width: 720px;"/>
            <div id="search-results"></div>
        </div>

        <!-- Config Tab -->
        <div id="config-tab" class="tab-content">
            <div class="page-header">CONFIGURACIÓN</div>
            <div class="config-section">
                <h3>Atajos de teclado</h3>
                <p><strong>F1:</strong> Feeds | <strong>F2:</strong> Saved | <strong>F3:</strong> Loved | <strong>F4:</strong> Config</p>
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
	log.Printf("🔍 Loading home for user: %s, feeds count: %d", username, len(feeds))

	var allArticles []Article
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, feed := range feeds {
		if !feed.Active {
			log.Printf("⏭️ Skipping inactive feed: %s", feed.URL)
			continue
		}
		log.Printf("📡 Processing active feed: %s", feed.URL)
		wg.Add(1)
		go func(feedURL string) {
			defer wg.Done()
			articles := getCachedOrFetch(feedURL)
			log.Printf("📰 Fetched %d articles from %s", len(articles), feedURL)
			mu.Lock()
			// Tomar sólo las 10 últimas por feed (asumimos orden descendente en getCachedOrFetch)
			if len(articles) > 10 {
				articles = articles[:10]
			}
			allArticles = append(allArticles, articles...)
			mu.Unlock()
		}(feed.URL)
	}
	wg.Wait()

	// Filtrar artículos que ya se mostraron en sesiones anteriores del usuario
	if username == "" {
		username = getUserFromRequest(r)
	}
	loaded := loadLoadedArticlesSet(username)
	filtered := make([]Article, 0, len(allArticles))
	for _, a := range allArticles {
		if !loaded[a.Link] {
			filtered = append(filtered, a)
			loaded[a.Link] = true
		}
	}
	allArticles = filtered
	// Persistir conjunto actualizado
	saveLoadedArticlesSet(username, loaded)

	log.Printf("📊 Total articles before processing: %d", len(allArticles))

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

	// Mostrar todos los últimos (no limitar a 50)

	// Precargar contenido completo de los primeros artículos en segundo plano
	go preloadArticleContent(allArticles)

	data := TemplateData{
		Articles: allArticles,
	}

	log.Printf("📊 Final article count being sent to template: %d", len(allArticles))
	if len(allArticles) > 0 {
		log.Printf("📰 First article: %s - %s", allArticles[0].Title, allArticles[0].Source)
	} else {
		log.Printf("❌ NO ARTICLES TO DISPLAY!")
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

// Persistencia simple por usuario
func appendJSONItem(path string, item any) error {
	var arr []any
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &arr)
	}
	arr = append(arr, item)
	b, _ := json.MarshalIndent(arr, "", "  ")
	return os.WriteFile(path, b, 0644)
}

func listJSONItems(path string) []map[string]any {
	var arr []map[string]any
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &arr)
	}
	return arr
}

// ==========================
// Persistencia de artículos ya "cargados" por sesión (por usuario)
// ==========================
func loadLoadedArticlesSet(username string) map[string]bool {
	path := username + "_loaded.json"
	var arr []string
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &arr)
	}
	set := make(map[string]bool, len(arr))
	for _, s := range arr {
		set[s] = true
	}
	return set
}

func saveLoadedArticlesSet(username string, set map[string]bool) {
	arr := make([]string, 0, len(set))
	for k := range set {
		arr = append(arr, k)
	}
	b, _ := json.MarshalIndent(arr, "", "  ")
	_ = os.WriteFile(username+"_loaded.json", b, 0644)
}

func saveListHandler(listName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		username := getUserFromRequest(r)
		var req struct{ Title, Link, Source string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Link == "" {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		path := username + "_" + listName + ".json"
		item := map[string]any{"title": req.Title, "link": req.Link, "source": req.Source, "user": username}
		if err := appendJSONItem(path, item); err != nil {
			http.Error(w, "Failed to save", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	}
}

func listHandler(listName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := getUserFromRequest(r)
		path := username + "_" + listName + ".json"
		items := listJSONItems(path)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(items)
	}
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

	// Convertir algunas etiquetas HTML a texto plano con mejor formato
	content = strings.ReplaceAll(content, "<br>", "\n")
	content = strings.ReplaceAll(content, "<br/>", "\n")
	content = strings.ReplaceAll(content, "<br />", "\n")
	content = strings.ReplaceAll(content, "</p>", "\n\n")
	content = strings.ReplaceAll(content, "<p>", "")
	content = strings.ReplaceAll(content, "</div>", "\n")
	content = strings.ReplaceAll(content, "</h1>", "\n\n")
	content = strings.ReplaceAll(content, "</h2>", "\n\n")
	content = strings.ReplaceAll(content, "</h3>", "\n\n")
	content = strings.ReplaceAll(content, "</h4>", "\n\n")
	content = strings.ReplaceAll(content, "</h5>", "\n\n")
	content = strings.ReplaceAll(content, "</h6>", "\n\n")
	content = strings.ReplaceAll(content, "<h1>", "\n\n")
	content = strings.ReplaceAll(content, "<h2>", "\n\n")
	content = strings.ReplaceAll(content, "<h3>", "\n\n")
	content = strings.ReplaceAll(content, "<h4>", "\n\n")
	content = strings.ReplaceAll(content, "<h5>", "\n\n")
	content = strings.ReplaceAll(content, "<h6>", "\n\n")

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
	spaceRe := regexp.MustCompile(`[ \t]+`)
	content = spaceRe.ReplaceAllString(content, " ")

	// Limpiar saltos de línea excesivos pero mantener párrafos
	newlineRe := regexp.MustCompile(`\n\s*\n\s*\n+`)
	content = newlineRe.ReplaceAllString(content, "\n\n")

	// Remover espacios al inicio y final de líneas
	lineCleanRe := regexp.MustCompile(`(?m)^[ \t]+|[ \t]+$`)
	content = lineCleanRe.ReplaceAllString(content, "")

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
        @import url('https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@300;400;700&display=swap');
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
            color: #00ff00; /* Verde más brillante */
            text-align: center;
            font-family: 'JetBrains Mono', monospace;
            white-space: pre;
            line-height: 1.0;
            text-shadow: 0 0 15px #00ff00, 0 0 25px #00ff00, 0 0 35px #00ff00; /* Brillo más intenso */
            animation: glow 2s ease-in-out infinite alternate;
        }
        
        @keyframes glow {
            from { text-shadow: 0 0 15px #00ff00, 0 0 25px #00ff00, 0 0 35px #00ff00; }
            to { text-shadow: 0 0 25px #00ff00, 0 0 40px #00ff00, 0 0 55px #00ff00; } /* Brillo más intenso */
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
            border: 1px dashed #00ff00;
            border-radius: 8px;
            color: #00ff00;
            font-family: 'JetBrains Mono', monospace;
            font-size: 14px;
            padding: 8px 12px;
            margin: 8px 0;
            width: 90%;
            max-width: 260px;
            box-sizing: border-box;
            text-align: center;
            letter-spacing: 1px;
            transition: all 0.3s ease;
            backdrop-filter: blur(5px);
            box-shadow: 
                0 0 12px rgba(0, 255, 0, 0.15),
                inset 0 0 12px rgba(0, 255, 0, 0.05);
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
            border: 1px solid #00ff00;
            border-radius: 8px;
            color: #00ff00;
            font-family: 'JetBrains Mono', monospace;
            font-size: 14px;
            font-weight: bold;
            padding: 8px 18px;
            cursor: pointer;
            margin-top: 15px;
            width: 100%;
            max-width: 260px;
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
        
        async function registerUser(username, password) {
            const res = await fetch('/api/register', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ username, password })
            });
            if (!res.ok) {
                const msg = await res.text();
                throw new Error(msg || 'Registration failed');
            }
            return res.json();
        }



        async function saveToList(listName, el) {
            const container = el.closest('.article-container');
            const line = container.querySelector('.article-line');
            const title = container.querySelector('.title')?.textContent || '';
            const source = container.querySelector('.source-name')?.textContent || '';
            const link = line?.dataset.url || '';
            try {
                const endpoint = listName === 'loved' ? '/api/save-loved' : '/api/save-saved';
                const res = await fetch(endpoint, {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ title, source, link })
                });
                if (!res.ok) throw new Error(await res.text());
                el.textContent = (listName === 'loved' ? 'LOVED' : 'SAVED') + ' ✓';
                // Refrescar listas si estamos en sus pestañas
                if (document.getElementById('favorites-list')) {
                    refreshLists();
                }
            } catch(e) {
                console.error('Save failed', e);
                el.textContent = 'ERROR';
            }
        }

        async function refreshLists() {
            try {
                const [savedRes, lovedRes] = await Promise.all([
                    fetch('/api/list-saved'),
                    fetch('/api/list-loved')
                ]);
                const saved = await savedRes.json();
                const loved = await lovedRes.json();
                const elSaved = document.getElementById('saved-list');
                const elFav   = document.getElementById('favorites-list');
                if (elSaved) elSaved.innerHTML = saved.map(function(i){ return '<div class="article-line"><span class="source-name">'+(i.source||'')+'</span>&nbsp;<span class="title">'+(i.title||'')+'</span></div>'; }).join('');
                if (elFav)   elFav.innerHTML   = loved.map(function(i){ return '<div class="article-line"><span class="source-name">'+(i.source||'')+'</span>&nbsp;<span class="title">'+(i.title||'')+'</span></div>'; }).join('');
                const cSaved = document.getElementById('count-saved');
                const cLoved = document.getElementById('count-loved');
                if (cSaved) cSaved.textContent = String(saved.length || 0);
                if (cLoved) cLoved.textContent = String(loved.length || 0);

                // Si estamos en una de las pestañas de listas, reconstruir navegación
                const activeTab = document.querySelector('.tab.active')?.dataset?.tab;
                if (activeTab === 'favorites' || activeTab === 'saved') {
                    initializeArticlesList();
                    highlightCurrentArticle();
                }
            } catch(e) {
                console.error('refreshLists failed', e);
            }
        }

        function shareArticle(btn) {
            const container = btn.closest('.article-container');
            const link = container.querySelector('.article-line')?.dataset.url || '';
            if (navigator.share) {
                navigator.share({ url: link }).catch(()=>{});
            } else {
                navigator.clipboard.writeText(link).catch(()=>{});
                btn.textContent = 'COPIED ✓';
            }
        }

        document.addEventListener('DOMContentLoaded', function() {
            const usernameInput = document.getElementById('username');
            usernameInput.focus();
            
            // Agregar evento de input para detectar cuando se escribe el usuario
            usernameInput.addEventListener('input', handleUsernameInput);

            const registerBtn = document.getElementById('register-button');
            registerBtn.addEventListener('click', async () => {
                const username = document.getElementById('username').value.trim();
                const password = document.getElementById('password').value.trim();
                const errorDiv = document.getElementById('login-error');
                if (!username || !password) {
                    errorDiv.textContent = 'Please enter username and password to register';
                    errorDiv.style.display = 'block';
                    return;
                }
                errorDiv.style.display = 'none';
                const btn = document.getElementById('register-button');
                btn.textContent = 'REGISTERING...';
                btn.disabled = true;
                try {
                    await registerUser(username, password);
                    errorDiv.style.display = 'block';
                    errorDiv.style.color = '#00ff00';
                    errorDiv.textContent = '✅ Account created. You can login now.';
                    document.getElementById('login-button').focus();
                } catch (e) {
                    errorDiv.style.display = 'block';
                    errorDiv.style.color = '#ff3333';
                    errorDiv.textContent = e.message;
                } finally {
                    btn.textContent = 'REGISTRAR';
                    btn.disabled = false;
                }
            });
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
                <button type="button" id="register-button" class="login-button" style="margin-top:8px;">
                    REGISTRAR
                </button>
                <div id="login-error" class="login-error" style="display: none;"></div>
                <div class="login-info">
                    Credenciales por defecto:<br>
                    <strong>admin</strong> / admin123<br>
                    <strong>ancap</strong> / ghanima
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

// Registro de usuario simple (almacenado en users.json)
func registerAPIHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	req.Username = strings.TrimSpace(req.Username)
	req.Password = strings.TrimSpace(req.Password)
	if req.Username == "" || req.Password == "" {
		http.Error(w, "Username and password required", http.StatusBadRequest)
		return
	}

	users := loadUsers()
	for _, u := range users {
		if u.Username == req.Username {
			http.Error(w, "User already exists", http.StatusConflict)
			return
		}
	}

	users = append(users, User{Username: req.Username, Password: req.Password})
	if err := saveUsers(users); err != nil {
		http.Error(w, "Failed to save user", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true})
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

	// Endpoint de recarga automática (SSE). Air reinicia el binario -> la conexión se corta -> el cliente recarga al reconectar.
	mux.HandleFunc("/dev/reload", func(w http.ResponseWriter, r *http.Request) {
		// Headers SSE
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}
		// Enviar pings periódicos; cuando el proceso se reinicie, la conexión se cortará
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case t := <-ticker.C:
				_, _ = fmt.Fprintf(w, "data: %d\n\n", t.Unix())
				flusher.Flush()
			}
		}
	})

	// Rutas públicas (sin autenticación)
	mux.HandleFunc("/login", loginPageHandler)
	mux.HandleFunc("/api/login", loginAPIHandler)
	mux.HandleFunc("/api/register", registerAPIHandler)
	// listas por usuario
	mux.Handle("/api/save-saved", authMiddleware(http.HandlerFunc(saveListHandler("saved"))))
	mux.Handle("/api/save-loved", authMiddleware(http.HandlerFunc(saveListHandler("loved"))))
	mux.Handle("/api/list-saved", authMiddleware(http.HandlerFunc(listHandler("saved"))))
	mux.Handle("/api/list-loved", authMiddleware(http.HandlerFunc(listHandler("loved"))))
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
	log.Println("🔐 Default users: admin/admin123, ancap/ghanima")

	if err := http.ListenAndServe(":8082", gzipMiddleware(mux)); err != nil {
		log.Fatalf("❌ Server failed to start: %v", err)
	}
}
